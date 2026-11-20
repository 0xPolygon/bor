package heimdallgrpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/staking"
)

func TestStakingRecords_Unsupported(t *testing.T) {
	t.Parallel()

	client := &HeimdallGRPCClient{}

	reports, err := client.ParticipationReports(t.Context(), 1, 100)
	require.ErrorIs(t, err, staking.ErrRecordsUnsupported)
	require.Nil(t, reports)

	records, err := client.ValidatorStatusRecords(t.Context(), 1, 100)
	require.ErrorIs(t, err, staking.ErrRecordsUnsupported)
	require.Nil(t, records)
}
