package registrytest

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
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
