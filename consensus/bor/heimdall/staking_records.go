package heimdall

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/staking"
)

const (
	fetchParticipationReportsPath = "stake/participation-reports/time"
	fetchValidatorStatusPath      = "stake/validator-status/time"
)

// ParticipationReports fetches one page of participation reports with ID at or
// above fromID and record time before to, in ID order.
func (h *HeimdallClient) ParticipationReports(ctx context.Context, fromID uint64, to int64) ([]*staking.ParticipationReport, error) {
	url, err := stakingRecordsURL(h.urlString, fetchParticipationReportsPath, fromID, to)
	if err != nil {
		return nil, err
	}

	ctx = WithRequestType(ctx, ParticipationReportsRequest)

	response, err := Fetch[staking.ParticipationReportsResponse](ctx, &Request{client: h.client, url: url, start: time.Now()})
	if err != nil {
		return nil, fmt.Errorf("fetching participation reports: %w", err)
	}

	cutoff := time.Unix(to, 0)
	reports := make([]*staking.ParticipationReport, 0, len(response.Records))

	for i := range response.Records {
		if r := &response.Records[i]; r.ID >= fromID && r.RecordTime.Before(cutoff) {
			reports = append(reports, r)
		}
	}

	sort.SliceStable(reports, func(i, j int) bool { return reports[i].ID < reports[j].ID })

	return reports, nil
}

// ValidatorStatusRecords fetches one page of validator status records with ID
// at or above fromID and record time before to, in ID order.
func (h *HeimdallClient) ValidatorStatusRecords(ctx context.Context, fromID uint64, to int64) ([]*staking.ValidatorStatusRecord, error) {
	url, err := stakingRecordsURL(h.urlString, fetchValidatorStatusPath, fromID, to)
	if err != nil {
		return nil, err
	}

	ctx = WithRequestType(ctx, ValidatorStatusRequest)

	response, err := Fetch[staking.ValidatorStatusResponse](ctx, &Request{client: h.client, url: url, start: time.Now()})
	if err != nil {
		return nil, fmt.Errorf("fetching validator status records: %w", err)
	}

	cutoff := time.Unix(to, 0)
	records := make([]*staking.ValidatorStatusRecord, 0, len(response.Records))

	for i := range response.Records {
		if r := &response.Records[i]; r.ID >= fromID && r.RecordTime.Before(cutoff) {
			records = append(records, r)
		}
	}

	sort.SliceStable(records, func(i, j int) bool { return records[i].ID < records[j].ID })

	return records, nil
}

// stakingRecordsURL builds a query with the same parameters as clerk/time.
func stakingRecordsURL(urlString, path string, fromID uint64, to int64) (*url.URL, error) {
	formattedTime := time.Unix(to, 0).UTC().Format(time.RFC3339Nano)

	return makeURL(urlString, path, fmt.Sprintf(fetchStateSyncEventsFormat, fromID, formattedTime, stateFetchLimit))
}
