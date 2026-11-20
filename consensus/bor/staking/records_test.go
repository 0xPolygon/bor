package staking

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAmount(t *testing.T) {
	t.Parallel()

	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	tests := []struct {
		name    string
		in      string
		want    *big.Int
		wantErr bool
	}{
		{name: "zero", in: "0", want: big.NewInt(0)},
		{name: "small", in: "42", want: big.NewInt(42)},
		{name: "leading zeros", in: "007", want: big.NewInt(7)},
		{name: "largest uint256", in: maxUint256.String(), want: maxUint256},
		{name: "one above the largest uint256", in: new(big.Int).Add(maxUint256, big.NewInt(1)).String(), wantErr: true},
		{name: "more digits than a uint256 has", in: strings.Repeat("1", maxAmountDigits+1), wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "negative", in: "-1", wantErr: true},
		{name: "plus sign", in: "+1", wantErr: true},
		{name: "hex", in: "0x10", wantErr: true},
		{name: "decimal point", in: "1.5", wantErr: true},
		{name: "space", in: " 1", wantErr: true},
		{name: "character just below the digits", in: "1/", wantErr: true},
		{name: "character just above the digits", in: "1:", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseAmount(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.Zero(t, tt.want.Cmp(got), "got %s", got)
		})
	}
}

func TestParticipation_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	in := Participation{
		ValidatorID:     7,
		MilestonesVoted: 16,
		EthereumStake:   big.NewInt(300),
		PolygonStake:    big.NewInt(20),
		InTransit:       big.NewInt(1),
		VotingPower:     321,
	}

	enc, err := json.Marshal(in)
	require.NoError(t, err)
	require.JSONEq(t, `{"validator_id":"7","milestones_voted":"16","ethereum_stake":"300","polygon_stake":"20","in_transit":"1","voting_power":"321"}`, string(enc))

	var out Participation
	require.NoError(t, json.Unmarshal(enc, &out))
	require.Equal(t, in, out)
}

// A missing amount is written as zero, which is what it packs to.
func TestParticipation_MarshalNilAmounts(t *testing.T) {
	t.Parallel()

	enc, err := json.Marshal(Participation{ValidatorID: 1})
	require.NoError(t, err)
	require.JSONEq(t, `{"validator_id":"1","milestones_voted":"0","ethereum_stake":"0","polygon_stake":"0","in_transit":"0","voting_power":"0"}`, string(enc))
}

func TestParticipation_UnmarshalErrors(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"validator_id":     `"9"`,
		"milestones_voted": `"1"`,
		"ethereum_stake":   `"1"`,
		"polygon_stake":    `"2"`,
		"in_transit":       `"3"`,
		"voting_power":     `"4"`,
	}

	tests := []struct {
		name    string
		field   string
		value   string
		wantErr string
	}{
		{"bad ethereum stake", "ethereum_stake", `"-1"`, "ethereum_stake of validator 9"},
		{"bad polygon stake", "polygon_stake", `"0x1"`, "polygon_stake of validator 9"},
		{"bad in transit", "in_transit", `""`, "in_transit of validator 9"},
		{"negative voting power", "voting_power", `"-4"`, "voting_power"},
		{"unquoted validator id", "validator_id", `9`, "unquoted value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fields := make([]string, 0, len(valid))
			for name, value := range valid {
				if name == tt.field {
					value = tt.value
				}

				fields = append(fields, `"`+name+`":`+value)
			}

			var p Participation
			err := json.Unmarshal([]byte("{"+strings.Join(fields, ",")+"}"), &p)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, Participation{}, p, "nothing is set when decoding fails")
		})
	}
}

func TestRecords_Unmarshal(t *testing.T) {
	t.Parallel()

	var status ValidatorStatusResponse
	require.NoError(t, json.Unmarshal([]byte(`{"validator_status":[{"id":"3","record_time":"2026-11-18T00:00:00Z","validators":[{"validator_id":"1","in_set":true,"accepted_nonce":"5"}]}]}`), &status))
	require.Len(t, status.Records, 1)
	require.Equal(t, uint64(3), status.Records[0].ID)
	require.Equal(t, []ValidatorStatus{{ValidatorID: 1, InSet: true, AcceptedNonce: 5}}, status.Records[0].Validators)

	var reports ParticipationReportsResponse
	require.NoError(t, json.Unmarshal([]byte(`{"participation_reports":[{"id":"2","record_time":"2026-11-18T00:00:00Z","start_block":"10","end_block":"20","milestones":"4","validators":[]}]}`), &reports))
	require.Len(t, reports.Records, 1)
	require.Equal(t, uint64(2), reports.Records[0].ID)
	require.Equal(t, uint64(10), reports.Records[0].StartBlock)
	require.Equal(t, uint64(20), reports.Records[0].EndBlock)
	require.Equal(t, uint64(4), reports.Records[0].Milestones)
}
