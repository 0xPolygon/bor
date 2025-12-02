package keeper

import (
	"context"
	"time"

	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	hmTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
)

const (
	MaxCheckpointListLimit = 10_000 // In erigon, CheckpointsFetchLimit is 10_000.
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	k *Keeper
}

func isPaginationEmpty(p query.PageRequest) bool {
	return p.Key == nil &&
		p.Offset == 0 &&
		p.Limit == 0 &&
		!p.CountTotal &&
		!p.Reverse
}

// NewQueryServer creates a new querier for the checkpoint client.
// It uses the underlying keeper and its contractCaller to interact with the Ethereum chain.
func NewQueryServer(k *Keeper) types.QueryServer {
	return queryServer{
		k: k,
	}
}

// GetCheckpointParams returns the checkpoint params
func (q queryServer) GetCheckpointParams(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointParamsMethod, startTime, &err)

	// get the validator set
	params, err := q.k.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryParamsResponse{
		Params: params,
	}, nil
}

// GetAckCount returns the checkpoint ack count
func (q queryServer) GetAckCount(ctx context.Context, _ *types.QueryAckCountRequest) (*types.QueryAckCountResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetAckCountMethod, startTime, &err)

	count, err := q.k.GetAckCount(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAckCountResponse{AckCount: count}, err
}

// GetCheckpoint returns the checkpoint based on its number
func (q queryServer) GetCheckpoint(ctx context.Context, req *types.QueryCheckpointRequest) (*types.QueryCheckpointResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointMethod, startTime, &err)

	checkpoint, err := q.k.GetCheckpointByNumber(ctx, req.Number)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCheckpointResponse{Checkpoint: checkpoint}, nil
}

// GetCheckpointLatest returns the latest checkpoint
func (q queryServer) GetCheckpointLatest(ctx context.Context, _ *types.QueryCheckpointLatestRequest) (*types.QueryCheckpointLatestResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointLatestMethod, startTime, &err)

	checkpoint, err := q.k.GetLastCheckpoint(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCheckpointLatestResponse{Checkpoint: checkpoint}, nil
}

// GetCheckpointBuffer returns the checkpoint from the buffer
func (q queryServer) GetCheckpointBuffer(ctx context.Context, _ *types.QueryCheckpointBufferRequest) (*types.QueryCheckpointBufferResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointBufferMethod, startTime, &err)

	checkpoint, err := q.k.GetCheckpointFromBuffer(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCheckpointBufferResponse{Checkpoint: checkpoint}, nil
}

// GetLastNoAck returns the last no ack
func (q queryServer) GetLastNoAck(ctx context.Context, _ *types.QueryLastNoAckRequest) (*types.QueryLastNoAckResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetLastNoAckMethod, startTime, &err)

	noAck, err := q.k.GetLastNoAck(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryLastNoAckResponse{LastNoAckId: noAck}, err
}

// GetNextCheckpoint returns the next expected checkpoint
func (q queryServer) GetNextCheckpoint(ctx context.Context, req *types.QueryNextCheckpointRequest) (*types.QueryNextCheckpointResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetNextCheckpointMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	chainParams, err := q.k.ck.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// get the validator set
	validatorSet, err := q.k.stakeKeeper.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	proposer := validatorSet.GetProposer()
	ackCount, err := q.k.GetAckCount(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	params, err := q.k.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	var start uint64

	if ackCount != 0 {
		checkpointNumber := ackCount

		lastCheckpoint, err := q.k.GetCheckpointByNumber(ctx, checkpointNumber)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		start = lastCheckpoint.EndBlock + 1
	}

	endBlockNumber := start + params.AvgCheckpointLength

	contractCaller := q.k.IContractCaller

	rootHash, err := contractCaller.GetRootHash(start, endBlockNumber, params.MaxCheckpointLength)
	if err != nil {
		q.k.Logger(ctx).Error("could not fetch rootHash", "start", start, "end", endBlockNumber, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	dividendAccounts, err := q.k.topupKeeper.GetAllDividendAccounts(ctx)
	if err != nil {
		q.k.Logger(ctx).Error("could not get the dividends accounts", "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	accRootHash, err := hmTypes.GetAccountRootHash(dividendAccounts)
	if err != nil {
		q.k.Logger(ctx).Error("could not get generate account root hash", "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	checkpointMsg := types.MsgCheckpoint{
		Proposer:        proposer.Signer,
		StartBlock:      start,
		EndBlock:        endBlockNumber,
		RootHash:        rootHash,
		AccountRootHash: accRootHash,
		BorChainId:      chainParams.ChainParams.BorChainId,
	}

	return &types.QueryNextCheckpointResponse{Checkpoint: checkpointMsg}, nil
}

// GetCheckpointList returns the list of checkpoints.
func (q queryServer) GetCheckpointList(ctx context.Context, req *types.QueryCheckpointListRequest) (*types.QueryCheckpointListResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointListMethod, startTime, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	if isPaginationEmpty(req.Pagination) {
		return nil, status.Errorf(codes.InvalidArgument, "pagination request is empty (at least one of offset, key or limit must be set)")
	}
	if req.Pagination.Limit == 0 || req.Pagination.Limit > MaxCheckpointListLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot be 0 or greater than %d", MaxCheckpointListLimit)
	}

	checkpoints, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.checkpoints,
		&req.Pagination, func(number uint64, checkpoint types.Checkpoint) (types.Checkpoint, error) {
			return checkpoint, nil
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error in pagination; please verify the pagination params: %v", err)
	}

	return &types.QueryCheckpointListResponse{CheckpointList: checkpoints, Pagination: *pageRes}, nil
}

// GetCheckpointOverview returns the checkpoint overview
// which includes AckCount, LastNoAckId, BufferCheckpoint, ValidatorCount, and ValidatorSet
func (q queryServer) GetCheckpointOverview(ctx context.Context, _ *types.QueryCheckpointOverviewRequest) (*types.QueryCheckpointOverviewResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointOverviewMethod, startTime, &err)

	// get the validator set
	validatorSet, err := q.k.stakeKeeper.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get validator set: %v", err)
	}

	ackCount, err := q.k.GetAckCount(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get checkpoint ack count: %v", err)
	}

	lastNoAck, err := q.k.GetLastNoAck(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get last checkpoint no-ack: %v", err)
	}

	bufferCheckpoint, err := q.k.GetCheckpointFromBuffer(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get checkpoint from buffer: %v", err)
	}

	return &types.QueryCheckpointOverviewResponse{
		AckCount:         ackCount,
		LastNoAckId:      lastNoAck,
		BufferCheckpoint: bufferCheckpoint,
		ValidatorCount:   uint64(len(validatorSet.Validators)),
		ValidatorSet:     validatorSet,
	}, nil
}

// GetCheckpointSignatures queries for the last checkpoint signatures
func (q queryServer) GetCheckpointSignatures(ctx context.Context, req *types.QueryCheckpointSignaturesRequest) (*types.QueryCheckpointSignaturesResponse, error) {
	var err error
	startTime := time.Now()
	defer recordCheckpointQueryMetric(api.GetCheckpointSignaturesMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if !hex.IsTxHashNonEmpty(req.TxHash) {
		return nil, status.Error(codes.InvalidArgument, "invalid tx hash")
	}

	txHash, err := q.k.GetCheckpointSignaturesTxHash(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if req.TxHash != txHash {
		return nil, status.Error(codes.NotFound, "checkpoint signatures not set for the given tx hash")
	}

	checkpointSignatures, err := q.k.GetCheckpointSignatures(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if len(checkpointSignatures.Signatures) == 0 {
		return nil, status.Error(codes.NotFound, "checkpoint signatures not set")
	}
	return &types.QueryCheckpointSignaturesResponse{Signatures: checkpointSignatures.Signatures}, nil
}

func recordCheckpointQueryMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.CheckpointSubsystem, method, api.QueryType, success, start)
}
