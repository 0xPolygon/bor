package core

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/registryreader"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// fakeReservedReader is a state-independent registryreader.Reader for the
// stateless-path tests: it reports an empty (but present) reserved set.
type fakeReservedReader struct{ has bool }

func (f fakeReservedReader) HasReservedRegistry() bool { return f.has }
func (f fakeReservedReader) IsReservedAddress(*state.StateDB, uint64, common.Hash, common.Address) (bool, error) {
	return false, nil
}
func (f fakeReservedReader) ReservedClientForAddress(*state.StateDB, uint64, common.Hash, common.Address) (registryreader.ClientLookup, error) {
	return registryreader.ClientLookup{}, nil
}
func (f fakeReservedReader) Root(*state.StateDB, uint64, common.Hash) (common.Hash, error) {
	return common.Hash{}, nil
}
func (f fakeReservedReader) WhitelistedAddresses(*state.StateDB, uint64, common.Hash) ([]common.Address, error) {
	return nil, nil
}
func (f fakeReservedReader) TotalReservedGas(*state.StateDB, uint64, common.Hash) (uint64, error) {
	return 0, nil
}

func reservedActiveConfig(t *testing.T) *params.ChainConfig {
	t.Helper()
	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.ReservedBlockspaceBlock = big.NewInt(0) // active from genesis
	if bor.ReservedRegistryContract == "" {
		bor.ReservedRegistryContract = params.DefaultReservedRegistryContract
	}
	cfg.Bor = &bor
	return &cfg
}

// TestReservedSnapshotForBlock_StatelessHeaderChain pins the wiring that
// ExecuteStateless depends on: the reserved-blockspace reader must reach the
// classification path through a bare HeaderChain (the stateless/witness backend).
// Without a wired reader, a post-fork block with a configured registry is a hard
// error (a silent empty set would split the state root); with one wired, the
// snapshot builds.
func TestReservedSnapshotForBlock_StatelessHeaderChain(t *testing.T) {
	t.Parallel()

	activeCfg := reservedActiveConfig(t)
	header := &types.Header{Number: big.NewInt(10)}

	t.Run("wired reader classifies", func(t *testing.T) {
		hc := &HeaderChain{config: activeCfg, reservedRegistry: fakeReservedReader{has: true}}
		snap, err := ReservedSnapshotForBlock(hc, nil, header)
		require.NoError(t, err)
		require.NotNil(t, snap, "a wired reader must yield a snapshot, even an empty one")
	})

	t.Run("missing reader is a hard error", func(t *testing.T) {
		// A bare HeaderChain (the pre-fix stateless case): exposes ReservedRegistry()
		// but returns nil, so classification cannot proceed safely.
		hc := &HeaderChain{config: activeCfg}
		_, err := ReservedSnapshotForBlock(hc, nil, header)
		require.Error(t, err)
	})

	t.Run("pre-fork skips classification", func(t *testing.T) {
		cfg := *activeCfg
		bor := *cfg.Bor
		bor.ReservedBlockspaceBlock = big.NewInt(1000) // not yet active at #10
		cfg.Bor = &bor
		hc := &HeaderChain{config: &cfg} // no reader, but none needed pre-fork
		snap, err := ReservedSnapshotForBlock(hc, nil, header)
		require.NoError(t, err)
		require.Nil(t, snap)
	})

	t.Run("no registry configured stays dark", func(t *testing.T) {
		cfg := *activeCfg
		bor := *cfg.Bor
		bor.ReservedRegistryContract = "" // fork active but feature dark
		cfg.Bor = &bor
		hc := &HeaderChain{config: &cfg}
		snap, err := ReservedSnapshotForBlock(hc, nil, header)
		require.NoError(t, err)
		require.Nil(t, snap)
	})
}
