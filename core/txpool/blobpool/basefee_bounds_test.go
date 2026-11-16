package blobpool

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func newBoundsTestChain(t *testing.T) *testBlockChain {
	t.Helper()

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	return &testBlockChain{
		config:  testChainConfig,
		basefee: uint256.NewInt(params.InitialBaseFee),
		blobfee: uint256.NewInt(params.BlobTxMinBlobGasprice),
		statedb: statedb,
	}
}

// oversizedHead returns a child of parent whose next base fee needs 257 bits.
func oversizedHead(parent *types.Header) *types.Header {
	head := types.CopyHeader(parent)
	head.ParentHash = parent.Hash()
	head.Number = new(big.Int).Add(parent.Number, big.NewInt(1))
	head.BaseFee = new(big.Int).Lsh(big.NewInt(1), 257)
	return head
}

func TestHeadBaseFeeBounds(t *testing.T) {
	t.Parallel()

	chain := newBoundsTestChain(t)
	pool := New(Config{Datadir: t.TempDir()}, chain, nil)
	head := chain.CurrentBlock()

	fee, err := pool.headBaseFee(head)
	require.NoError(t, err)
	require.False(t, fee.IsZero())

	// An empty 256-bit head lowers the next base fee, so it still fits.
	atBound := types.CopyHeader(head)
	atBound.BaseFee = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	_, err = pool.headBaseFee(atBound)
	require.NoError(t, err)

	_, err = pool.headBaseFee(oversizedHead(head))
	require.ErrorContains(t, err, "bitlen 257")
}

func TestInitRejectsOversizedBaseFee(t *testing.T) {
	t.Parallel()

	chain := newBoundsTestChain(t)
	pool := New(Config{Datadir: t.TempDir()}, chain, nil)
	err := pool.Init(1, oversizedHead(chain.CurrentBlock()), newReserver())
	require.ErrorContains(t, err, "bitlen 257")
}

func TestResetAndClearKeepRunningOnOversizedBaseFee(t *testing.T) {
	t.Parallel()

	chain := newBoundsTestChain(t)
	pool := New(Config{Datadir: t.TempDir()}, chain, nil)
	head := chain.CurrentBlock()
	require.NoError(t, pool.Init(1, head, newReserver()))
	t.Cleanup(func() { require.NoError(t, pool.Close()) })

	before := pool.evict.basefeeJumps
	bad := oversizedHead(head)
	pool.Reset(head, bad)
	require.Equal(t, bad.Hash(), pool.head.Load().Hash())
	require.Equal(t, before, pool.evict.basefeeJumps)

	pool.Clear()
	require.Equal(t, dynamicFeeJumps(new(uint256.Int)), pool.evict.basefeeJumps)

	// A representable head restores the fees.
	pool.Reset(bad, head)
	require.Equal(t, before, pool.evict.basefeeJumps)
}
