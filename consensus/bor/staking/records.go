// Package staking holds the records Heimdall serves for the Polygon staking
// contracts: validator status and participation reports.
package staking

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// ErrRecordsUnsupported is returned by Heimdall clients that can't fetch
// staking records yet.
var ErrRecordsUnsupported = errors.New("staking records are not supported by this heimdall client")

// maxAmountDigits is the length of the largest uint256 in decimal.
const maxAmountDigits = 78

// ValidatorStatus is one validator's full current status.
type ValidatorStatus struct {
	ValidatorID   uint64 `json:"validator_id,string"`
	InSet         bool   `json:"in_set"`
	AcceptedNonce uint64 `json:"accepted_nonce,string"`
}

// ValidatorStatusRecord lists the validators whose status changed in one
// Heimdall block.
type ValidatorStatusRecord struct {
	ID         uint64            `json:"id,string"`
	RecordTime time.Time         `json:"record_time"`
	Validators []ValidatorStatus `json:"validators"`
}

// Participation is one validator's entry in a participation report. Stake
// amounts are in wei.
type Participation struct {
	ValidatorID     uint64
	MilestonesVoted uint64
	EthereumStake   *big.Int
	PolygonStake    *big.Int
	InTransit       *big.Int
	VotingPower     uint64
}

// ParticipationReport covers the milestones since the previous report.
type ParticipationReport struct {
	ID         uint64          `json:"id,string"`
	RecordTime time.Time       `json:"record_time"`
	StartBlock uint64          `json:"start_block,string"`
	EndBlock   uint64          `json:"end_block,string"`
	Milestones uint64          `json:"milestones,string"`
	Validators []Participation `json:"validators"`
}

// ValidatorStatusResponse is Heimdall's answer to the validator status query.
type ValidatorStatusResponse struct {
	Records []ValidatorStatusRecord `json:"validator_status"`
}

// ParticipationReportsResponse is Heimdall's answer to the participation
// reports query.
type ParticipationReportsResponse struct {
	Records []ParticipationReport `json:"participation_reports"`
}

type participationJSON struct {
	ValidatorID     uint64 `json:"validator_id,string"`
	MilestonesVoted uint64 `json:"milestones_voted,string"`
	EthereumStake   string `json:"ethereum_stake"`
	PolygonStake    string `json:"polygon_stake"`
	InTransit       string `json:"in_transit"`
	VotingPower     uint64 `json:"voting_power,string"`
}

// UnmarshalJSON decodes the amounts from decimal strings. A negative voting
// power fails here because the field decodes as unsigned.
func (p *Participation) UnmarshalJSON(data []byte) error {
	var w participationJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	ethereumStake, err := parseAmount(w.EthereumStake)
	if err != nil {
		return fmt.Errorf("ethereum_stake of validator %d: %w", w.ValidatorID, err)
	}

	polygonStake, err := parseAmount(w.PolygonStake)
	if err != nil {
		return fmt.Errorf("polygon_stake of validator %d: %w", w.ValidatorID, err)
	}

	inTransit, err := parseAmount(w.InTransit)
	if err != nil {
		return fmt.Errorf("in_transit of validator %d: %w", w.ValidatorID, err)
	}

	*p = Participation{
		ValidatorID:     w.ValidatorID,
		MilestonesVoted: w.MilestonesVoted,
		EthereumStake:   ethereumStake,
		PolygonStake:    polygonStake,
		InTransit:       inTransit,
		VotingPower:     w.VotingPower,
	}

	return nil
}

// MarshalJSON is the inverse of UnmarshalJSON.
func (p Participation) MarshalJSON() ([]byte, error) {
	return json.Marshal(participationJSON{
		ValidatorID:     p.ValidatorID,
		MilestonesVoted: p.MilestonesVoted,
		EthereumStake:   amountString(p.EthereumStake),
		PolygonStake:    amountString(p.PolygonStake),
		InTransit:       amountString(p.InTransit),
		VotingPower:     p.VotingPower,
	})
}

// parseAmount reads a non-negative decimal that fits in a uint256. Every node
// has to turn the same string into the same call data, so nothing else is
// accepted: no sign, no hex, no whitespace.
func parseAmount(s string) (*big.Int, error) {
	if s == "" || len(s) > maxAmountDigits {
		return nil, fmt.Errorf("invalid amount %q", s)
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid amount %q", s)
		}
	}

	amount, ok := new(big.Int).SetString(s, 10)
	if !ok || amount.BitLen() > 256 {
		return nil, fmt.Errorf("invalid amount %q", s)
	}

	return amount, nil
}

func amountString(amount *big.Int) string {
	if amount == nil {
		return "0"
	}

	return amount.String()
}
