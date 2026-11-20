package heimdallapp

import (
	"context"

	"github.com/ethereum/go-ethereum/consensus/bor/staking"
)

// ParticipationReports is not served by this client yet.
func (h *HeimdallAppClient) ParticipationReports(context.Context, uint64, int64) ([]*staking.ParticipationReport, error) {
	return nil, staking.ErrRecordsUnsupported
}

// ValidatorStatusRecords is not served by this client yet.
func (h *HeimdallAppClient) ValidatorStatusRecords(context.Context, uint64, int64) ([]*staking.ValidatorStatusRecord, error) {
	return nil, staking.ErrRecordsUnsupported
}
