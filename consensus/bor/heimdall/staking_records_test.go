package heimdall

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/staking"
)

// stakingFixture is the shared example of what Heimdall serves and the call
// data Bor builds from it.
type stakingFixture struct {
	ValidatorStatus []struct {
		Record json.RawMessage `json:"record"`
	} `json:"validator_status"`
	ParticipationReports []struct {
		Record json.RawMessage `json:"record"`
	} `json:"participation_reports"`
}

func loadStakingFixture(t *testing.T) stakingFixture {
	t.Helper()

	data, err := os.ReadFile("../staking/testdata/records.json")
	require.NoError(t, err)

	var fixture stakingFixture
	require.NoError(t, json.Unmarshal(data, &fixture))

	return fixture
}

func stakingServer(t *testing.T, path, key string, records []json.RawMessage, wantFromID string) *httptest.Server {
	t.Helper()

	body, err := json.Marshal(map[string][]json.RawMessage{key: records})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, path, r.URL.Path)
		require.Equal(t, wantFromID, r.URL.Query().Get("from_id"))
		require.Equal(t, "50", r.URL.Query().Get("pagination.limit"))
		require.NotEmpty(t, r.URL.Query().Get("to_time"))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestValidatorStatusRecords(t *testing.T) {
	t.Parallel()

	fixture := loadStakingFixture(t)
	// Out of order, so the client has to sort by ID.
	records := []json.RawMessage{fixture.ValidatorStatus[1].Record, fixture.ValidatorStatus[0].Record}
	after := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).Unix()

	tests := []struct {
		name    string
		fromID  uint64
		to      int64
		wantIDs []uint64
	}{
		{"all records in ID order", 1, after, []uint64{1, 2}},
		{"records below fromID dropped", 2, after, []uint64{2}},
		{"records at or after the cutoff dropped", 1, time.Date(2026, 11, 18, 0, 30, 0, 0, time.UTC).Unix(), []uint64{1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := stakingServer(t, "/stake/validator-status/time", "validator_status", records, new(big.Int).SetUint64(tt.fromID).String())
			client := NewHeimdallClient(srv.URL, 5*time.Second)
			defer client.Close()

			got, err := client.ValidatorStatusRecords(t.Context(), tt.fromID, tt.to)
			require.NoError(t, err)
			require.Len(t, got, len(tt.wantIDs))

			for i, id := range tt.wantIDs {
				require.Equal(t, id, got[i].ID)
			}
		})
	}

	t.Run("fields", func(t *testing.T) {
		t.Parallel()

		srv := stakingServer(t, "/stake/validator-status/time", "validator_status", records, "1")
		client := NewHeimdallClient(srv.URL, 5*time.Second)
		defer client.Close()

		got, err := client.ValidatorStatusRecords(t.Context(), 1, after)
		require.NoError(t, err)
		require.Equal(t, time.Date(2026, 11, 18, 0, 30, 0, 0, time.UTC), got[1].RecordTime.UTC())
		require.Equal(t, []staking.ValidatorStatus{
			{ValidatorID: 1, InSet: true, AcceptedNonce: 1},
			{ValidatorID: 1_000_000, InSet: true, AcceptedNonce: 2},
		}, got[1].Validators)
	})
}

func TestParticipationReports(t *testing.T) {
	t.Parallel()

	fixture := loadStakingFixture(t)
	srv := stakingServer(t, "/stake/participation-reports/time", "participation_reports",
		[]json.RawMessage{fixture.ParticipationReports[0].Record}, "1")
	client := NewHeimdallClient(srv.URL, 5*time.Second)
	defer client.Close()

	got, err := client.ParticipationReports(t.Context(), 1, time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).Unix())
	require.NoError(t, err)
	require.Len(t, got, 1)

	wei := func(pol int64) *big.Int {
		return new(big.Int).Mul(big.NewInt(pol), big.NewInt(1e18))
	}

	report := got[0]
	require.Equal(t, uint64(1), report.ID)
	require.Equal(t, uint64(25600), report.StartBlock)
	require.Equal(t, uint64(25855), report.EndBlock)
	require.Equal(t, uint64(16), report.Milestones)
	require.Equal(t, []staking.Participation{
		{ValidatorID: 1, MilestonesVoted: 16, EthereumStake: wei(3_000_000), PolygonStake: wei(500_000), InTransit: wei(0), VotingPower: 3_500_000},
		{ValidatorID: 2, MilestonesVoted: 12, EthereumStake: wei(1_000_000), PolygonStake: wei(0), InTransit: wei(250_000), VotingPower: 1_250_000},
		{ValidatorID: 1_000_000, MilestonesVoted: 16, EthereumStake: wei(0), PolygonStake: wei(200_000), InTransit: wei(0), VotingPower: 200_000},
	}, report.Validators)
}

func TestStakingRecords_Empty(t *testing.T) {
	t.Parallel()

	srv := stakingServer(t, "/stake/participation-reports/time", "participation_reports", nil, "7")
	client := NewHeimdallClient(srv.URL, 5*time.Second)
	defer client.Close()

	got, err := client.ParticipationReports(t.Context(), 7, 100)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestStakingRecords_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, ""},
		{"query not served", http.StatusNotImplemented, ""},
		{"malformed body", http.StatusOK, "{"},
		{"negative amount", http.StatusOK, `{"participation_reports":[{"id":"1","record_time":"2026-11-18T00:00:00Z","start_block":"1","end_block":"2","milestones":"1","validators":[{"validator_id":"1","milestones_voted":"1","ethereum_stake":"-1","polygon_stake":"0","in_transit":"0","voting_power":"1"}]}]}`},
		{"hex amount", http.StatusOK, `{"participation_reports":[{"id":"1","record_time":"2026-11-18T00:00:00Z","start_block":"1","end_block":"2","milestones":"1","validators":[{"validator_id":"1","milestones_voted":"1","ethereum_stake":"0x10","polygon_stake":"0","in_transit":"0","voting_power":"1"}]}]}`},
		{"negative voting power", http.StatusOK, `{"participation_reports":[{"id":"1","record_time":"2026-11-18T00:00:00Z","start_block":"1","end_block":"2","milestones":"1","validators":[{"validator_id":"1","milestones_voted":"1","ethereum_stake":"1","polygon_stake":"0","in_transit":"0","voting_power":"-1"}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := NewHeimdallClient(srv.URL, 5*time.Second)
			defer client.Close()

			reports, err := client.ParticipationReports(t.Context(), 1, 100)
			require.Error(t, err)
			require.Nil(t, reports)
		})
	}
}

func TestStakingRecords_InvalidURL(t *testing.T) {
	t.Parallel()

	client := NewHeimdallClient("http://[::1", 5*time.Second)
	defer client.Close()

	reports, err := client.ParticipationReports(t.Context(), 1, 100)
	require.Error(t, err)
	require.Nil(t, reports)

	records, err := client.ValidatorStatusRecords(t.Context(), 1, 100)
	require.Error(t, err)
	require.Nil(t, records)
}

func TestValidatorStatusRecords_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewHeimdallClient(srv.URL, 5*time.Second)
	defer client.Close()

	records, err := client.ValidatorStatusRecords(t.Context(), 1, 100)
	require.ErrorContains(t, err, "fetching validator status records")
	require.Nil(t, records)
}
