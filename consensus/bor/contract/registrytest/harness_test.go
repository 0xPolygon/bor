package registrytest

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/registryreader"
	"github.com/stretchr/testify/require"
)

func TestHarness_DeploysAndAnswersQueries(t *testing.T) {
	h := NewHarness(t)

	require.True(t, h.Reader.HasReservedRegistry(), "deployed registry must report HasReservedRegistry")

	got, err := h.Reader.IsReservedAddress(nil, 0, common.Hash{}, h.ReservedAddr)
	require.NoError(t, err)
	require.True(t, got, "address registered via createClient must be reserved")

	got, err = h.Reader.IsReservedAddress(nil, 0, common.Hash{}, h.UnreservedAddr)
	require.NoError(t, err)
	require.False(t, got, "unregistered address must not be reserved")

	lookup, err := h.Reader.ReservedClientForAddress(nil, 0, common.Hash{}, h.ReservedAddr)
	require.NoError(t, err)
	require.True(t, lookup.Active)
	require.Equal(t, uint64(10_000_000), lookup.GasQuota)
	require.NotNil(t, lookup.ClientID)
	require.Equal(t, int64(1), lookup.ClientID.Int64(), "createClient assigns id starting at 1")
}

func TestSnapshot_BuildsFromRegistry(t *testing.T) {
	h := NewHarness(t)

	snap, err := registryreader.BuildSnapshot(h.Reader, nil, 1, common.Hash{})
	require.NoError(t, err)
	require.NotNil(t, snap)

	// The whitelisted address classifies reserved; the other does not.
	require.True(t, snap.IsReserved(h.ReservedAddr, 1))
	require.False(t, snap.IsReserved(h.UnreservedAddr, 1))

	// Snapshot mirrors the registry: feeMode free (0), capacity = the one
	// client's quota, and a non-zero root it can be cached against.
	require.Equal(t, uint8(0), snap.FeeMode(h.ReservedAddr))
	require.Equal(t, uint64(10_000_000), snap.Capacity())
	require.NotEqual(t, common.Hash{}, snap.Root())

	// nil snapshot (no registry) classifies nothing and is safe.
	var none *registryreader.Snapshot
	require.False(t, none.IsReserved(h.ReservedAddr, 1))
	require.Equal(t, uint64(0), none.Capacity())
}
