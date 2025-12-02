package keeper

import (
	"context"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

const (
	MaxSpanListLimit = 1_000
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

func NewQueryServer(k *Keeper) types.QueryServer {
	return queryServer{
		k: k,
	}
}

func (q queryServer) GetLatestSpan(ctx context.Context, _ *types.QueryLatestSpanRequest) (*types.QueryLatestSpanResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetLatestSpanMethod, start, &err)

	lastSpan, err := q.k.GetLastSpan(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryLatestSpanResponse{Span: lastSpan}, nil
}

func (q queryServer) GetNextSpan(ctx context.Context, req *types.QueryNextSpanRequest) (*types.QueryNextSpanResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetNextSpanMethod, start, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	lastSpan, err := q.k.GetLastSpan(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if req.SpanId != lastSpan.Id+1 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid span id")
	}

	if req.StartBlock != lastSpan.EndBlock+1 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid start block")
	}

	if req.BorChainId != lastSpan.BorChainId {
		return nil, status.Errorf(codes.InvalidArgument, "invalid chain id")
	}

	// fetch params
	params, err := q.k.FetchParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// fetch current validator set
	validatorSet, err := q.k.sk.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Convert []*Validator to []staketypes.Validator
	validators := make([]staketypes.Validator, len(validatorSet.Validators))
	for i, v := range validatorSet.Validators {
		validators[i] = *v
	}

	// fetch next selected block producers
	nextSpanSeed, _, err := q.k.FetchNextSpanSeed(ctx, req.SpanId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	selectedProducers, err := q.k.SelectNextProducers(ctx, nextSpanSeed, validators)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	selectedProducers = types.SortValidatorByAddress(selectedProducers)

	// create the next span
	nextSpan := &types.Span{
		Id:                req.SpanId,
		StartBlock:        req.StartBlock,
		EndBlock:          req.StartBlock + params.SpanDuration - 1,
		ValidatorSet:      validatorSet,
		SelectedProducers: selectedProducers,
		BorChainId:        req.BorChainId,
	}

	return &types.QueryNextSpanResponse{Span: *nextSpan}, nil
}

// GetNextSpanSeed returns the next span seed
func (q queryServer) GetNextSpanSeed(ctx context.Context, req *types.QueryNextSpanSeedRequest) (*types.QueryNextSpanSeedResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetNextSpanSeedMethod, start, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	spanId := req.GetId()

	// fetch the next span seed
	nextSpanSeed, nextSpanSeedAuthor, err := q.k.FetchNextSpanSeed(ctx, spanId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryNextSpanSeedResponse{
		Seed:       nextSpanSeed.String(),
		SeedAuthor: nextSpanSeedAuthor.Hex(),
	}, nil
}

// GetBorParams returns the bor module parameters
func (q queryServer) GetBorParams(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetBorParamsMethod, start, &err)

	params, err := q.k.FetchParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryParamsResponse{Params: params}, nil
}

// GetSpanById returns the span by id
func (q queryServer) GetSpanById(ctx context.Context, req *types.QuerySpanByIdRequest) (*types.QuerySpanByIdResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetSpanByIdMethod, start, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	spanId, err := strconv.Atoi(req.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	span, err := q.k.GetSpan(ctx, uint64(spanId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QuerySpanByIdResponse{Span: &span}, nil
}

// GetSpanList returns the list of spans
func (q queryServer) GetSpanList(ctx context.Context, req *types.QuerySpanListRequest) (*types.QuerySpanListResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetSpanListMethod, start, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	if isPaginationEmpty(req.Pagination) {
		return nil, status.Errorf(codes.InvalidArgument, "pagination request is empty (at least one argument must be set)")
	}
	if req.Pagination.Limit == 0 || req.Pagination.Limit > MaxSpanListLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot be 0 or greater than %d", MaxSpanListLimit)
	}

	spans, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.spans,
		&req.Pagination, func(id uint64, span types.Span) (types.Span, error) {
			return span, nil
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "error in pagination; please verify the pagination params: %v", err)
	}

	return &types.QuerySpanListResponse{SpanList: spans, Pagination: *pageRes}, nil
}

func (q queryServer) GetProducerVotesByValidatorId(ctx context.Context, req *types.QueryProducerVotesByValidatorIdRequest) (*types.QueryProducerVotesByValidatorIdResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetProducerVotesByValidatorIdMethod, start, &err)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	producerVotes, err := q.k.GetProducerVotes(ctx, req.ValidatorId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryProducerVotesByValidatorIdResponse{Votes: producerVotes.Votes}, nil
}

func (q queryServer) GetProducerVotes(ctx context.Context, req *types.QueryProducerVotesRequest) (*types.QueryProducerVotesResponse, error) {
	var err error
	start := time.Now()
	defer recordBorQueryMetric(api.GetProducerVotesMethod, start, &err)

	validatorSet, err := q.k.sk.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	producerVotes := make(map[uint64]types.ProducerVotes)
	for _, validator := range validatorSet.Validators {
		producerVotes[validator.ValId], err = q.k.GetProducerVotes(ctx, validator.ValId)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &types.QueryProducerVotesResponse{AllVotes: producerVotes}, nil
}

func recordBorQueryMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.BorSubsystem, method, api.QueryType, success, start)
}
