package keeper

import (
	"context"
	"math/big"
	"time"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	heimdallTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

const (
	// MaxRecordListLimit is the maximum record list limit for queries.
	MaxRecordListLimit = 50
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	k *Keeper
}

// NewQueryServer creates a new querier for clerk clients.
func NewQueryServer(k *Keeper) types.QueryServer {
	return queryServer{
		k: k,
	}
}

func (q queryServer) GetRecordById(ctx context.Context, request *types.RecordRequest) (*types.RecordResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetRecordByIdMethod, startTime, &err)

	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	record, err := q.k.GetEventRecord(ctx, request.RecordId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.RecordResponse{Record: *record}, nil
}

func (q queryServer) GetRecordList(ctx context.Context, request *types.RecordListRequest) (*types.RecordListResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetRecordListMethod, startTime, &err)

	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if request.Page == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "page cannot be 0")
	}
	if request.Limit == 0 || request.Limit > MaxRecordListLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot be 0 or greater than %d", MaxRecordListLimit)
	}

	records, err := q.k.GetEventRecordList(ctx, request.Page, request.Limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.RecordListResponse{EventRecords: records}, nil
}

func (q queryServer) GetRecordListWithTime(ctx context.Context, request *types.RecordListWithTimeRequest) (*types.RecordListWithTimeResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetRecordListWithTimeMethod, startTime, &err)

	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if isPaginationEmpty(request.Pagination) {
		return nil, status.Errorf(codes.InvalidArgument, "pagination request is empty (at least one argument must be set)")
	}
	if request.Pagination.Limit == 0 || request.Pagination.Limit > MaxRecordListLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot be 0 or greater than %d", MaxRecordListLimit)
	}
	if request.FromId < 1 {
		return nil, status.Errorf(codes.InvalidArgument, "fromId cannot be less than 1")
	}

	// Collect the records based on pagination parameters.
	result := make([]types.EventRecord, 0, request.Pagination.Limit)

	// Use a range iterator starting from FromId.
	rng := (&collections.Range[uint64]{}).StartInclusive(request.FromId)

	iterator, err := q.k.RecordsWithID.Iterate(ctx, rng)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	defer iterator.Close()

	skipped := uint64(0)   // Records skipped based on pagination offset.
	collected := uint64(0) // Records collected based on pagination limit.

	for ; iterator.Valid(); iterator.Next() {
		value, err := iterator.Value()
		if err != nil {
			q.k.Logger(ctx).Debug("error in fetching event record from iterator", "error", err)
			break
		}

		if !value.RecordTime.Before(request.ToTime) {
			// Here, the time is >= ToTime, break early.
			break
		}

		// Skip records based on the pagination offset.
		if skipped < request.Pagination.Offset {
			skipped++
			continue
		}

		// Collect records up to the limit.
		if collected < request.Pagination.Limit {
			result = append(result, value)
			collected++
		} else {
			// We have collected enough records, stop iterating.
			break
		}
	}

	if len(result) == 0 {
		return &types.RecordListWithTimeResponse{
			EventRecords: []types.EventRecord{},
		}, nil
	}

	return &types.RecordListWithTimeResponse{
		EventRecords: result,
	}, nil
}

func (q queryServer) GetRecordSequence(ctx context.Context, request *types.RecordSequenceRequest) (*types.RecordSequenceResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetRecordSequenceMethod, startTime, &err)

	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if !hex.IsTxHashNonEmpty(request.TxHash) {
		return nil, status.Error(codes.InvalidArgument, "invalid tx hash")
	}

	chainParams, err := q.k.ChainKeeper.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Get the main tx receipt.
	txHash := common.FromHex(request.TxHash)
	receipt, err := q.k.contractCaller.GetConfirmedTxReceipt(common.BytesToHash(txHash), chainParams.GetMainChainTxConfirmations())
	if err != nil || receipt == nil {
		return nil, status.Errorf(codes.Internal, "transaction is not confirmed yet. please wait for sometime and try again")
	}

	// Get the sequence id.
	sequence := new(big.Int).Mul(receipt.BlockNumber, big.NewInt(heimdallTypes.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(request.LogIndex))
	// Check if the incoming tx already exists.
	if !q.k.HasRecordSequence(ctx, sequence.String()) {
		return nil, status.Error(codes.NotFound, "record sequence not found")
	}

	return &types.RecordSequenceResponse{Sequence: sequence.Uint64()}, nil
}

// IsClerkTxOld implements the gRPC service handler to query the status of a clerk tx
func (q queryServer) IsClerkTxOld(ctx context.Context, request *types.RecordSequenceRequest) (*types.IsClerkTxOldResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.IsClerkTxOldMethod, startTime, &err)

	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if !hex.IsTxHashNonEmpty(request.TxHash) {
		return nil, status.Error(codes.InvalidArgument, "invalid tx hash")
	}

	chainParams, err := q.k.ChainKeeper.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Get the main tx receipt.
	txHash := common.FromHex(request.TxHash)
	receipt, err := q.k.contractCaller.GetConfirmedTxReceipt(common.BytesToHash(txHash), chainParams.GetMainChainTxConfirmations())
	if err != nil || receipt == nil {
		return nil, status.Errorf(codes.Internal, "transaction is not confirmed yet. please wait for sometime and try again")
	}

	// Get the sequence id.
	sequence := new(big.Int).Mul(receipt.BlockNumber, big.NewInt(heimdallTypes.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(request.LogIndex))

	// Check if the incoming tx already exists.
	if !q.k.HasRecordSequence(ctx, sequence.String()) {
		return nil, status.Error(codes.NotFound, "record sequence not found")
	}

	return &types.IsClerkTxOldResponse{IsOld: true}, nil
}

// GetLatestRecordId implements the gRPC service handler to query the latest record id from L1.
func (q queryServer) GetLatestRecordId(ctx context.Context, _ *types.LatestRecordIdRequest) (*types.LatestRecordIdResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetLatestRecordIdMethod, startTime, &err)

	// Get chain params to get the StateSender contract address.
	chainParams, err := q.k.ChainKeeper.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Get the StateSender contract instance.
	stateSenderInstance, err := q.k.contractCaller.GetStateSenderInstance(chainParams.ChainParams.StateSenderAddress)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get state sender instance")
	}

	// Get the current state counter from L1.
	stateCounter := q.k.contractCaller.CurrentStateCounter(stateSenderInstance)
	if stateCounter == nil {
		return nil, status.Error(codes.Internal, "failed to get latest state counter from L1")
	}

	latestRecordId := stateCounter.Uint64()
	eventRecordExists := q.k.HasEventRecord(ctx, latestRecordId)
	return &types.LatestRecordIdResponse{LatestRecordId: latestRecordId, IsProcessedByHeimdall: eventRecordExists}, nil
}

// GetRecordCount implements the gRPC service handler to query the total count of event records.
func (q queryServer) GetRecordCount(ctx context.Context, _ *types.RecordCountRequest) (*types.RecordCountResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkQueryMetric(api.GetRecordCountMethod, startTime, &err)

	return &types.RecordCountResponse{Count: q.k.GetEventRecordCount(ctx)}, nil
}

func isPaginationEmpty(p query.PageRequest) bool {
	return p.Key == nil &&
		p.Offset == 0 &&
		p.Limit == 0 &&
		!p.CountTotal &&
		!p.Reverse
}

func recordClerkQueryMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.ClerkSubsystem, method, api.QueryType, success, start)
}
