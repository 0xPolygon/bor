package heimdallgrpc

import (
	"context"
	"sort"
	"time"

	"github.com/cosmos/cosmos-sdk/types/query"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/log"

	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

const (
	stateSyncTotalTimeout = 1 * time.Minute
)

func (h *HeimdallGRPCClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	log.Info("Fetching state sync events", "fromID", fromID, "to", to)

	var err error

	globalCtx, cancel := context.WithTimeout(ctx, stateSyncTotalTimeout)
	defer cancel()

	// Start the timer and set the request type on the context.
	start := time.Now()
	ctx = heimdall.WithRequestType(globalCtx, heimdall.StateSyncRequest)

	// Defer the metrics call.
	defer func() {
		heimdall.SendMetrics(ctx, start, err == nil)
	}()

	eventRecords := make([]*clerk.EventRecordWithTime, 0)

	for {
		pagination := query.PageRequest{
			Limit: stateFetchLimit,
		}

		req := &types.RecordListWithTimeRequest{
			FromId:     fromID,
			ToTime:     time.Unix(to, 0),
			Pagination: pagination,
		}

		var res *types.RecordListWithTimeResponse
		pageCtx, pageCancel := context.WithTimeout(ctx, defaultTimeout)
		res, err = h.clerkQueryClient.GetRecordListWithTime(pageCtx, req)
		pageCancel()
		if err != nil {
			return nil, err
		}

		events := res.GetEventRecords()

		for _, event := range events {
			eventRecord := &clerk.EventRecordWithTime{
				EventRecord: clerk.EventRecord{
					ID:       event.Id,
					Contract: common.HexToAddress(event.Contract),
					Data:     event.Data,
					TxHash:   common.HexToHash(event.TxHash),
					LogIndex: event.LogIndex,
					ChainID:  event.BorChainId,
				},
				Time: event.RecordTime,
			}
			eventRecords = append(eventRecords, eventRecord)
		}

		if len(events) < stateFetchLimit {
			break
		}

		fromID += uint64(stateFetchLimit)
	}

	log.Info("Fetched state sync events", "fromID", fromID, "to", to)

	return eventRecords, nil
}

// StateSyncEventsAtHeight fetches state sync events visible at a specific Heimdall height
// using the native gRPC GetRecordListVisibleAtHeight endpoint.
func (h *HeimdallGRPCClient) StateSyncEventsAtHeight(ctx context.Context, fromID uint64, toTime int64, heimdallHeight int64) ([]*clerk.EventRecordWithTime, error) {
	log.Info("Fetching state sync events at height (gRPC)", "fromID", fromID, "toTime", toTime, "heimdallHeight", heimdallHeight)

	var err error

	globalCtx, cancel := context.WithTimeout(ctx, stateSyncTotalTimeout)
	defer cancel()

	start := time.Now()
	ctx = heimdall.WithRequestType(globalCtx, heimdall.StateSyncAtHeightRequest)

	defer func() {
		heimdall.SendMetrics(ctx, start, err == nil)
	}()

	eventRecords := make([]*clerk.EventRecordWithTime, 0)

	for {
		req := &types.RecordListVisibleAtHeightRequest{
			FromId:         fromID,
			HeimdallHeight: heimdallHeight,
			ToTime:         time.Unix(toTime, 0),
			Pagination:     query.PageRequest{Limit: stateFetchLimit},
		}

		var res *types.RecordListVisibleAtHeightResponse
		pageCtx, pageCancel := context.WithTimeout(ctx, defaultTimeout)
		res, err = h.clerkQueryClient.GetRecordListVisibleAtHeight(pageCtx, req)
		pageCancel()
		if err != nil {
			return nil, err
		}

		events := res.GetEventRecords()

		for _, event := range events {
			eventRecord := &clerk.EventRecordWithTime{
				EventRecord: clerk.EventRecord{
					ID:       event.Id,
					Contract: common.HexToAddress(event.Contract),
					Data:     event.Data,
					TxHash:   common.HexToHash(event.TxHash),
					LogIndex: event.LogIndex,
					ChainID:  event.BorChainId,
				},
				Time: event.RecordTime,
			}
			eventRecords = append(eventRecords, eventRecord)
		}

		if len(events) < stateFetchLimit {
			break
		}

		fromID += uint64(stateFetchLimit)
	}

	sort.SliceStable(eventRecords, func(i, j int) bool {
		return eventRecords[i].ID < eventRecords[j].ID
	})

	return eventRecords, nil
}

// GetBlockHeightByTime returns the Heimdall block height at or before the given cutoff
// unix timestamp using the native gRPC GetBlockHeightByTime endpoint.
func (h *HeimdallGRPCClient) GetBlockHeightByTime(ctx context.Context, cutoffTime int64) (int64, error) {
	log.Info("Fetching block height by time (gRPC)", "cutoffTime", cutoffTime)

	var err error

	start := time.Now()
	ctx = heimdall.WithRequestType(ctx, heimdall.BlockHeightByTimeRequest)

	defer func() {
		heimdall.SendMetrics(ctx, start, err == nil)
	}()

	reqCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	req := &types.BlockHeightByTimeRequest{
		CutoffTime: cutoffTime,
	}

	res, err := h.clerkQueryClient.GetBlockHeightByTime(reqCtx, req)
	if err != nil {
		return 0, err
	}

	return res.Height, nil
}
