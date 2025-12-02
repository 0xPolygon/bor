package keeper

import (
	"context"
	"math/big"
	"strconv"
	"time"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/0xPolygon/heimdall-v2/metrics/api"
	hmTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the clerk MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

func (srv msgServer) HandleMsgEventRecord(ctx context.Context, msg *types.MsgEventRecord) (*types.MsgEventRecordResponse, error) {
	var err error
	startTime := time.Now()
	defer recordClerkTransactionMetric(api.HandleMsgEventRecordMethod, startTime, &err)

	logger := srv.Logger(ctx)

	logger.Debug("✅ Validating clerk msg",
		"id", msg.Id,
		"contract", msg.ContractAddress,
		"data", string(msg.Data),
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	// check if the event record exists
	if exists := srv.HasEventRecord(ctx, msg.Id); exists {
		return nil, types.ErrEventRecordAlreadySynced
	}

	// chainManager params
	params, err := srv.ChainKeeper.GetParams(ctx)
	if err != nil {
		logger.Error("failed to get chain manager params", "error", err)
		return nil, err
	}

	chainParams := params.ChainParams

	// check chain id
	if chainParams.BorChainId != msg.ChainId {
		logger.Error("Invalid Bor chain id", "msgChainID", msg.ChainId, "borChainId", chainParams.BorChainId)
		return nil, errors.Wrapf(sdkerrors.ErrInvalidRequest, "invalid bor chain id")
	}

	// sequence id
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(hmTypes.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	if srv.HasRecordSequence(ctx, sequence.String()) {
		logger.Error("Event already processed", "Sequence", sequence.String())
		return nil, errors.Wrapf(sdkerrors.ErrConflict, "old events are not allowed")
	}

	// add events
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeRecord,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(types.AttributeKeyRecordID, strconv.FormatUint(msg.Id, 10)),
			sdk.NewAttribute(types.AttributeKeyRecordContract, msg.ContractAddress),
			sdk.NewAttribute(types.AttributeKeyRecordTxHash, msg.TxHash),
			sdk.NewAttribute(types.AttributeKeyRecordTxLogIndex, strconv.FormatUint(msg.LogIndex, 10)),
		),
	})

	return &types.MsgEventRecordResponse{}, nil
}

func recordClerkTransactionMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.ClerkSubsystem, method, api.TxType, success, start)
}
