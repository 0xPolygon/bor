package nativectf

import (
	"crypto/rand"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
)

const selector = "0x856296f7" // getCollectionId(bytes32,bytes32,uint256)

func ctfCode(t testing.TB) []byte {
	raw, err := os.ReadFile("testdata/ctf_code.hex")
	if err != nil {
		t.Fatalf("read ctf code: %v", err)
	}
	return common.FromHex(strings.TrimSpace(string(raw)))
}

type oracle struct {
	cfg  *runtime.Config
	dest common.Address
}

func newOracle(t testing.TB) *oracle {
	cfg := new(runtime.Config)
	cfg.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg.GasLimit = 50_000_000
	dest := common.BytesToAddress([]byte("ctf"))
	cfg.State.CreateAccount(dest)
	cfg.State.SetCode(dest, ctfCode(t), tracing.CodeChangeUnspecified)
	return &oracle{cfg: cfg, dest: dest}
}

func (o *oracle) call(parent, conditionId [32]byte, indexSet *big.Int) ([]byte, uint64) {
	var idx [32]byte
	indexSet.FillBytes(idx[:])
	data := append([]byte{}, common.FromHex(selector)...)
	data = append(data, parent[:]...)
	data = append(data, conditionId[:]...)
	data = append(data, idx[:]...)
	ret, gasLeft, err := runtime.Call(o.dest, data, o.cfg)
	if err != nil {
		panic(err)
	}
	return ret, o.cfg.GasLimit - gasLeft
}

func TestGetCollectionId_Equivalence_Parent0(t *testing.T) {
	o := newOracle(t)
	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	var zero [32]byte

	check := func(ci [32]byte, idx *big.Int) {
		ret, _ := o.call(zero, ci, idx)
		if len(ret) != 32 {
			t.Fatalf("oracle returned %d bytes (revert?) ci=%x idx=%s", len(ret), ci, idx)
		}
		got, _, _, _ := GetCollectionId(zero, ci, idx)
		if [32]byte(ret) != got {
			t.Fatalf("MISMATCH ci=%x idx=%s: oracle=%x native=%x", ci, idx, ret, got)
		}
	}
	var kc [32]byte
	copy(kc[:], crypto.Keccak256([]byte("samplecondition")))
	check(kc, big.NewInt(1))
	check([32]byte{}, big.NewInt(0))
	check([32]byte{}, maxU)
	var allf [32]byte
	for i := range allf {
		allf[i] = 0xff
	}
	check(allf, maxU)
	for i := 0; i < 3000; i++ {
		var ci [32]byte
		rand.Read(ci[:])
		idx, _ := rand.Int(rand.Reader, maxU)
		check(ci, idx)
	}
}

func TestGetCollectionId_GasEquivalence_Parent0(t *testing.T) {
	o := newOracle(t)
	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	var zero [32]byte
	for i := 0; i < 4000; i++ {
		var ci [32]byte
		rand.Read(ci[:])
		idx, _ := rand.Int(rand.Reader, maxU)
		_, gas := o.call(zero, ci, idx)
		_, iters, pf, b254 := GetCollectionId(zero, ci, idx)
		if got := ExternalCallGas(iters, pf, b254); got != gas {
			t.Fatalf("gas MISMATCH ci=%x idx=%s: oracle=%d native=%d (iters=%d pf=%v b254=%v)", ci, idx, gas, got, iters, pf, b254)
		}
	}
}
