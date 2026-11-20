package contract

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/bor/staking"
)

// stakingFixture pairs each record Heimdall serves with the call data Bor
// has to build from it.
type stakingFixture struct {
	ValidatorStatus []struct {
		Record   staking.ValidatorStatusRecord `json:"record"`
		Calldata hexutil.Bytes                 `json:"calldata"`
	} `json:"validator_status"`
	ParticipationReports []struct {
		Record   staking.ParticipationReport `json:"record"`
		Calldata hexutil.Bytes               `json:"calldata"`
	} `json:"participation_reports"`
}

func loadStakingFixture(t *testing.T) stakingFixture {
	t.Helper()

	data, err := os.ReadFile("../staking/testdata/records.json")
	require.NoError(t, err)

	var fixture stakingFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.ValidatorStatus)
	require.NotEmpty(t, fixture.ParticipationReports)

	return fixture
}

func TestPackValidatorStatus_MatchesFixture(t *testing.T) {
	t.Parallel()

	for _, entry := range loadStakingFixture(t).ValidatorStatus {
		got, err := PackValidatorStatus(&entry.Record)
		require.NoError(t, err)
		require.Equal(t, entry.Calldata, hexutil.Bytes(got), "record %d", entry.Record.ID)
	}
}

func TestPackParticipationReport_MatchesFixture(t *testing.T) {
	t.Parallel()

	for _, entry := range loadStakingFixture(t).ParticipationReports {
		got, err := PackParticipationReport(&entry.Record)
		require.NoError(t, err)
		require.Equal(t, entry.Calldata, hexutil.Bytes(got), "report %d", entry.Record.ID)
	}
}

func TestPackParticipationReport_MissingAmount(t *testing.T) {
	t.Parallel()

	_, err := PackParticipationReport(&staking.ParticipationReport{
		ID:         1,
		Validators: []staking.Participation{{ValidatorID: 7}},
	})
	require.ErrorContains(t, err, "validator 7")
}
