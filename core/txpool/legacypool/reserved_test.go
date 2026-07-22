package legacypool

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/registryreader"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// fakeRegistry is a state-independent registryreader.Reader: the reserved set is
// fixed at construction. It lets the pool build a snapshot without deploying the
// real contract into the test state, exercising the snapshot→isReserved path.
type fakeRegistry struct {
	reserved map[common.Address]registryreader.ClientLookup
	capacity uint64
}

func newFakeRegistry(reserved ...common.Address) *fakeRegistry {
	f := &fakeRegistry{reserved: make(map[common.Address]registryreader.ClientLookup)}
	for i, a := range reserved {
		f.reserved[a] = registryreader.ClientLookup{
			ClientID: big.NewInt(int64(i + 1)), GasQuota: 30_000_000, Active: true,
		}
		f.capacity += 30_000_000
	}
	return f
}

func (f *fakeRegistry) HasReservedRegistry() bool { return true }
func (f *fakeRegistry) IsReservedAddress(_ *state.StateDB, _ uint64, _ common.Hash, a common.Address) (bool, error) {
	c, ok := f.reserved[a]
	return ok && c.Active, nil
}
func (f *fakeRegistry) ReservedClientForAddress(_ *state.StateDB, _ uint64, _ common.Hash, a common.Address) (registryreader.ClientLookup, error) {
	return f.reserved[a], nil
}
func (f *fakeRegistry) Root(_ *state.StateDB, _ uint64, _ common.Hash) (common.Hash, error) {
	return common.HexToHash("0x1"), nil
}
func (f *fakeRegistry) WhitelistedAddresses(_ *state.StateDB, _ uint64, _ common.Hash) ([]common.Address, error) {
	addrs := make([]common.Address, 0, len(f.reserved))
	for a := range f.reserved {
		addrs = append(addrs, a)
	}
	return addrs, nil
}
func (f *fakeRegistry) TotalReservedGas(_ *state.StateDB, _ uint64, _ common.Hash) (uint64, error) {
	return f.capacity, nil
}

func reservedChainConfig() *params.ChainConfig {
	cfg := *params.BorUnittestChainConfig // London active at 0
	bor := *cfg.Bor
	bor.ReservedBlockspaceBlock = big.NewInt(0) // fork gate; the reserved set comes from the registry
	cfg.Bor = &bor
	return &cfg
}

func setupReservedPool(reserved common.Address) *LegacyPool {
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg := reservedChainConfig()
	bc := newTestBlockChain(cfg, 10_000_000, statedb, new(event.Feed))
	pool := New(testTxPoolConfig, bc)
	if err := pool.Init(testTxPoolConfig.PriceLimit, bc.CurrentBlock(), newReserver()); err != nil {
		panic(err)
	}
	<-pool.initDoneCh
	// A realistic positive tip floor (PIP-35 ~25 gwei). Reserved senders must
	// bypass it; everyone else is held to it.
	pool.SetGasTip(big.NewInt(30_000_000_000))
	// Wire the registry source (post-Init, as the backend does) and build the snapshot.
	pool.SetReservedRegistry(newFakeRegistry(reserved))
	return pool
}

func zeroFeeTx(t *testing.T, cfg *params.ChainConfig, key *ecdsa.PrivateKey, nonce uint64, to common.Address) *types.Transaction {
	t.Helper()
	tx, err := types.SignNewTx(key, types.LatestSigner(cfg), &types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		Gas:       100_000,
		To:        &to,
		Value:     big.NewInt(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestReservedZeroFeeTxAdmittedAndPending verifies the core behaviour:
// a zero-fee tx from a reserved sender is admitted past the tip floor, kept in
// the pool, and surfaced by Pending even under a high miner MinTip — whereas the
// same tx from a non-reserved sender is rejected at admission.
func TestReservedZeroFeeTxAdmittedAndPending(t *testing.T) {
	t.Parallel()

	reservedKey, _ := crypto.GenerateKey()
	reservedAddr := crypto.PubkeyToAddress(reservedKey.PublicKey)
	pool := setupReservedPool(reservedAddr)
	defer pool.Close()

	otherKey, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)

	testAddBalance(pool, reservedAddr, big.NewInt(1_000_000))
	testAddBalance(pool, otherAddr, big.NewInt(1_000_000))

	cfg := pool.chainconfig

	// Reserved sender: zero-fee tx must be admitted.
	rtx := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0x42})
	if err := pool.Add([]*types.Transaction{rtx}, true)[0]; err != nil {
		t.Fatalf("reserved zero-fee tx rejected: %v", err)
	}
	if pool.Get(rtx.Hash()) == nil {
		t.Fatal("reserved zero-fee tx not kept in pool")
	}

	// Non-reserved sender: identical zero-fee tx must be rejected at the floor.
	otx := zeroFeeTx(t, cfg, otherKey, 0, common.Address{0x42})
	if err := pool.Add([]*types.Transaction{otx}, true)[0]; err == nil {
		t.Fatal("non-reserved zero-fee tx should be rejected at the tip floor")
	}

	// The reserved tx must survive a high miner tip filter so it reaches the miner.
	pending := pool.Pending(txpool.PendingFilter{
		MinTip:  uint256.NewInt(30_000_000_000),
		BaseFee: uint256.NewInt(1_000_000_000),
	}, nil)
	if got := pending[reservedAddr]; len(got) != 1 || got[0].Hash != rtx.Hash() {
		t.Fatalf("reserved zero-fee tx missing from Pending under high MinTip: %v", got)
	}
}

// TestReservedZeroFeeReplacement pins the replacement rule:
// same-nonce replacement is priced entirely through the fallback-fee fields
// under the standard bump rule. A zero-fee tx can never replace a zero-fee tx
// ("10% over zero is still zero" — the strict-increase check rejects it),
// while strictly positive fallback fees win the slot. This keeps same-nonce
// pool content convergent across nodes regardless of arrival order.
func TestReservedZeroFeeReplacement(t *testing.T) {
	t.Parallel()

	reservedKey, _ := crypto.GenerateKey()
	reservedAddr := crypto.PubkeyToAddress(reservedKey.PublicKey)
	pool := setupReservedPool(reservedAddr)
	defer pool.Close()

	testAddBalance(pool, reservedAddr, big.NewInt(1_000_000))
	cfg := pool.chainconfig

	first := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0xaa})
	if err := pool.Add([]*types.Transaction{first}, true)[0]; err != nil {
		t.Fatalf("first reserved tx rejected: %v", err)
	}

	// Same nonce, still zero fee: rejected — zero cannot out-bid zero.
	second := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0xbb})
	if err := pool.Add([]*types.Transaction{second}, true)[0]; err == nil {
		t.Fatal("zero-fee replacement of a zero-fee tx must be rejected")
	}
	if pool.Get(first.Hash()) == nil {
		t.Fatal("incumbent zero-fee tx must remain in the pool")
	}

	// Same nonce with positive fallback fees: wins the slot under the standard
	// bump rule (strictly above zero clears both the strict-increase check and
	// the zero threshold). The fallback fees stay below the 30 gwei tip floor,
	// which reserved senders bypass at admission.
	third, err := types.SignNewTx(reservedKey, types.LatestSigner(cfg), &types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
		Gas:       100_000,
		To:        &common.Address{0xcc},
		Value:     big.NewInt(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Add([]*types.Transaction{third}, true)[0]; err != nil {
		t.Fatalf("fallback-fee replacement rejected: %v", err)
	}
	if pool.Get(third.Hash()) == nil {
		t.Fatal("fallback-fee replacement not in pool")
	}
	if pool.Get(first.Hash()) != nil {
		t.Fatal("replaced zero-fee tx should have been evicted")
	}

	// And a zero-fee tx cannot displace the fallback-fee incumbent either.
	fourth := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0xdd})
	if err := pool.Add([]*types.Transaction{fourth}, true)[0]; err == nil {
		t.Fatal("zero-fee tx must not replace a fallback-fee incumbent")
	}
}
