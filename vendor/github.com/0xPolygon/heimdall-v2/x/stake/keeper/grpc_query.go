package keeper

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	hexCodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	k *Keeper
}

// NewQueryServer creates a new querier for stake clients.
// It uses the underlying keeper and its contractCaller to interact with the Ethereum chain.
func NewQueryServer(k *Keeper) types.QueryServer {
	return queryServer{
		k: k,
	}
}

// GetCurrentValidatorSet queries all validators that are currently active in the validator set
func (q queryServer) GetCurrentValidatorSet(ctx context.Context, _ *types.QueryCurrentValidatorSetRequest) (*types.QueryCurrentValidatorSetResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetCurrentValidatorSetMethod, startTime, &err)

	validatorSet, err := q.k.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCurrentValidatorSetResponse{ValidatorSet: validatorSet}, nil
}

// GetSignerByAddress queries validator info for given validator address.
func (q queryServer) GetSignerByAddress(ctx context.Context, req *types.QuerySignerRequest) (*types.QuerySignerResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetSignerByAddressMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if !common.IsHexAddress(req.ValAddress) {
		return nil, status.Error(codes.InvalidArgument, "invalid validator address")
	}

	// validate address
	ac := hexCodec.NewHexCodec()
	_, err = ac.StringToBytes(req.ValAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid validator address %s", req.ValAddress)
	}

	validator, err := q.k.GetValidatorInfo(ctx, req.ValAddress)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "error in getting validator corresponding to the given address %s. error: %v", req.ValAddress, err)
	}

	return &types.QuerySignerResponse{Validator: validator}, nil
}

// GetValidatorById queries validator info for a given validator id.
func (q queryServer) GetValidatorById(ctx context.Context, req *types.QueryValidatorRequest) (*types.QueryValidatorResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetValidatorByIdMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("invalid validator id %d", req.Id))
	}

	validator, err := q.k.GetValidatorFromValID(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("error in getting validator corresponding to the given id %d. error: %v", req.Id, err))
	}

	return &types.QueryValidatorResponse{Validator: validator}, nil
}

// GetValidatorStatusByAddress queries validator status for given validator address.
func (q queryServer) GetValidatorStatusByAddress(ctx context.Context, req *types.QueryValidatorStatusRequest) (*types.QueryValidatorStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	// validate address
	ac := hexCodec.NewHexCodec()
	_, err := ac.StringToBytes(req.ValAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid validator address %s", req.ValAddress)
	}

	isCurrentValidator, err := q.k.IsCurrentValidatorByAddress(ctx, req.ValAddress)
	if err != nil {
		return &types.QueryValidatorStatusResponse{IsOld: false}, err
	}

	return &types.QueryValidatorStatusResponse{IsOld: isCurrentValidator}, nil
}

// GetTotalPower queries the total power of a validator set
func (q queryServer) GetTotalPower(ctx context.Context, _ *types.QueryTotalPowerRequest) (*types.QueryTotalPowerResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetTotalPowerMethod, startTime, &err)

	totalPower, err := q.k.GetTotalPower(ctx)
	if err != nil {
		return nil, err
	}

	return &types.QueryTotalPowerResponse{TotalPower: totalPower}, nil
}

// IsStakeTxOld queries for the staking sequence
func (q queryServer) IsStakeTxOld(ctx context.Context, req *types.QueryStakeIsOldTxRequest) (*types.QueryStakeIsOldTxResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.IsStakeTxOldMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if !hex.IsTxHashNonEmpty(req.TxHash) {
		return nil, status.Error(codes.InvalidArgument, "invalid tx hash")
	}

	if req.LogIndex >= math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "invalid log index")
	}

	chainParams, err := q.k.cmKeeper.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "chain params not found")
	}

	// get main tx receipt
	receipt, err := q.k.contractCaller.GetConfirmedTxReceipt(common.HexToHash(req.TxHash), chainParams.MainChainTxConfirmations)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if receipt == nil {
		return nil, status.Errorf(codes.NotFound, "receipt not found")
	}

	sequence := new(big.Int).Mul(receipt.BlockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(req.LogIndex))

	// check if incoming tx already exists
	if !q.k.HasStakingSequence(ctx, sequence.String()) {
		return &types.QueryStakeIsOldTxResponse{IsOld: false}, nil
	}

	return &types.QueryStakeIsOldTxResponse{IsOld: true}, nil
}

// GetProposersByTimes queries for the proposers by Tendermint iterations
func (q queryServer) GetProposersByTimes(ctx context.Context, req *types.QueryProposersRequest) (*types.QueryProposersResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetProposersByTimesMethod, startTime, &err)

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	if req.Times >= math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "times exceeds MaxInt64")
	}

	if req.Times == 0 {
		return nil, status.Error(codes.InvalidArgument, "times is 0")
	}

	validatorSet, err := q.k.GetValidatorSet(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	times := int(req.Times)
	if times > len(validatorSet.Validators) {
		times = len(validatorSet.Validators)
	}

	// init proposers
	proposers := make([]types.Validator, times)

	// get proposers
	for index := 0; index < times; index++ {
		proposers[index] = *(validatorSet.GetProposer())
		validatorSet.IncrementProposerPriority(1)
	}

	return &types.QueryProposersResponse{Proposers: proposers}, nil
}

// GetCurrentProposer queries the validator info for the current proposer
func (q queryServer) GetCurrentProposer(ctx context.Context, _ *types.QueryCurrentProposerRequest) (*types.QueryCurrentProposerResponse, error) {
	var err error
	startTime := time.Now()
	defer recordStakeQueryMetric(api.GetCurrentProposerMethod, startTime, &err)

	proposer := q.k.GetCurrentProposer(ctx)

	return &types.QueryCurrentProposerResponse{Validator: *proposer}, nil
}

func recordStakeQueryMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.StakeSubsystem, method, api.QueryType, success, start)
}
