package nativectf_test

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
)

// benchInputs is a fixed, reproducible set of (conditionId, indexSet) inputs
// (parent==0) shared across all three benchmark modes so the per-op averages
// reflect the same real iteration-count distribution.
func benchInputs(n int) ([][32]byte, []*big.Int) {
	r := rand.New(rand.NewSource(1))
	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	cis := make([][32]byte, n)
	idxs := make([]*big.Int, n)
	for i := 0; i < n; i++ {
		var ci [32]byte
		r.Read(ci[:])
		cis[i] = ci
		idxs[i] = new(big.Int).Rand(r, maxU)
	}
	return cis, idxs
}

func benchOracle(b *testing.B, switchDispatch bool) {
	cfg := new(runtime.Config)
	cfg.State, _ = state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg.GasLimit = 50_000_000
	cfg.EVMConfig = vm.Config{EnableEVMSwitchDispatch: switchDispatch}
	dest := common.BytesToAddress([]byte("ctf"))
	cfg.State.CreateAccount(dest)
	cfg.State.SetCode(dest, ctfCode(b), tracing.CodeChangeUnspecified)

	cis, idxs := benchInputs(512)
	sel := common.FromHex(selector)
	var zero [32]byte

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var idx [32]byte
		idxs[i%len(idxs)].FillBytes(idx[:])
		data := append(append(append(append([]byte{}, sel...), zero[:]...), cis[i%len(cis)][:]...), idx[:]...)
		if _, _, err := runtime.Call(dest, data, cfg); err != nil {
			b.Fatalf("call: %v", err)
		}
	}
}

// BenchmarkGCI_Interp: stock interpreter executing the real CTF bytecode.
func BenchmarkGCI_Interp(b *testing.B) { benchOracle(b, false) }

// BenchmarkGCI_Switch: switch-dispatch interpreter executing the same bytecode.
func BenchmarkGCI_Switch(b *testing.B) { benchOracle(b, true) }

// BenchmarkGCI_Native: native Go (gnark bn254/fp) computing the same result.
func BenchmarkGCI_Native(b *testing.B) {
	cis, idxs := benchInputs(512)
	var zero [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nativectf.GetCollectionId(zero, cis[i%len(cis)], idxs[i%len(idxs)])
	}
}
