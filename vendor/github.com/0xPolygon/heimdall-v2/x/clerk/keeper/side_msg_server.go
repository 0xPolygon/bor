package keeper

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	heimdallTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

type sideMsgServer struct {
	Keeper
}

var msgEventRecord = sdk.MsgTypeURL(&types.MsgEventRecord{})

// NewSideMsgServerImpl returns an implementation of the clerk SideMsgServer interface
// for the provided Keeper.
func NewSideMsgServerImpl(keeper Keeper) sidetxs.SideMsgServer {
	return &sideMsgServer{Keeper: keeper}
}

// SideTxHandler returns a side handler for clerk type messages.
func (srv *sideMsgServer) SideTxHandler(methodName string) sidetxs.SideTxHandler {
	switch methodName {
	case msgEventRecord:
		return srv.SideHandleMsgEventRecord
	default:
		return nil
	}
}

// PostTxHandler returns a post-handler for clerk type messages.
func (srv *sideMsgServer) PostTxHandler(methodName string) sidetxs.PostTxHandler {
	switch methodName {
	case msgEventRecord:
		return srv.PostHandleMsgEventRecord
	default:
		return nil
	}
}

func (srv *sideMsgServer) SideHandleMsgEventRecord(ctx sdk.Context, _msg sdk.Msg) (result sidetxs.Vote) {
	var err error
	startTime := time.Now()
	defer recordClerkMetric(api.SideHandleMsgEventRecordMethod, api.SideType, startTime, &err)

	msg, ok := _msg.(*types.MsgEventRecord)
	if !ok {
		srv.Logger(ctx).Error("type mismatch for MsgEventRecord")
		return sidetxs.Vote_VOTE_NO
	}

	srv.Logger(ctx).Debug("✅ Validating External call for clerk msg",
		"txHash", msg.TxHash,
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	// chainManager params
	params, err := srv.ChainKeeper.GetParams(ctx)
	if err != nil {
		srv.Logger(ctx).Error("failed to get chain manager params", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	chainParams := params.ChainParams

	// get confirmed tx receipt
	receipt, err := srv.Keeper.contractCaller.GetConfirmedTxReceipt(common.HexToHash(msg.TxHash), params.GetMainChainTxConfirmations())
	if receipt == nil || err != nil {
		return sidetxs.Vote_VOTE_NO
	}

	// get event log for clerk
	eventLog, err := srv.Keeper.contractCaller.DecodeStateSyncedEvent(chainParams.StateSenderAddress, receipt, msg.LogIndex)
	if err != nil || eventLog == nil {
		srv.Logger(ctx).Error("Error fetching log from tx hash")
		return sidetxs.Vote_VOTE_NO
	}

	if receipt.BlockNumber.Uint64() != msg.BlockNumber {
		srv.Logger(ctx).Error("blockNumber in message doesn't match blockNumber in receipt", "MsgBlockNumber", msg.BlockNumber, "ReceiptBlockNumber", receipt.BlockNumber.Uint64())
		return sidetxs.Vote_VOTE_NO
	}

	// check if message and event log matches
	if eventLog.Id.Uint64() != msg.Id {
		srv.Logger(ctx).Error("ID in message doesn't match with id in log", "msgId", msg.Id, "stateIdFromTx", eventLog.Id)
		return sidetxs.Vote_VOTE_NO
	}

	ac := address.NewHexCodec()
	msgContractAddrBytes, err := ac.StringToBytes(msg.ContractAddress)
	if err != nil {
		srv.Logger(ctx).Error(
			"Could not generate bytes from msg contract address",
			"MsgContractAddress", msg.ContractAddress,
		)
		return sidetxs.Vote_VOTE_NO
	}
	eventLogContractAddrBytes, err := ac.StringToBytes(eventLog.ContractAddress.String())
	if err != nil {
		srv.Logger(ctx).Error(
			"Could not generate bytes from event logs contract address",
			"EventContractAddress", eventLog.ContractAddress.String(),
		)
		return sidetxs.Vote_VOTE_NO
	}

	if !bytes.Equal(eventLogContractAddrBytes, msgContractAddrBytes) {
		srv.Logger(ctx).Error(
			"ContractAddress from event does not match with Msg ContractAddress",
			"EventContractAddress", eventLog.ContractAddress.String(),
			"MsgContractAddress", msg.ContractAddress,
		)

		return sidetxs.Vote_VOTE_NO
	}

	if !bytes.Equal(eventLog.Data, msg.Data) {
		if !(len(eventLog.Data) > helper.MaxStateSyncSize && bytes.Equal(msg.Data, []byte(""))) {
			srv.Logger(ctx).Error(
				"Data from event does not match with Msg Data",
				"EventData", hex.EncodeToString(eventLog.Data),
				"MsgData", string(msg.Data),
			)

			return sidetxs.Vote_VOTE_NO
		}
	}

	return sidetxs.Vote_VOTE_YES
}

func (srv *sideMsgServer) PostHandleMsgEventRecord(ctx sdk.Context, _msg sdk.Msg, sideTxResult sidetxs.Vote) error {
	var err error
	startTime := time.Now()
	defer recordClerkMetric(api.PostHandleMsgEventRecordMethod, api.PostType, startTime, &err)

	logger := srv.Logger(ctx)

	msg, ok := _msg.(*types.MsgEventRecord)
	if !ok {
		err := errors.New("type mismatch for MsgEventRecord")
		logger.Error(err.Error())
	}

	// Skip handler if clerk is not approved
	if sideTxResult != sidetxs.Vote_VOTE_YES {
		logger.Debug("skipping new clerk since side-tx didn't get yes votes")
		return nil
	}

	// check for replay
	if srv.HasEventRecord(ctx, msg.Id) {
		logger.Debug("skipping new clerk record as it's already processed")
		return errors.New("clerk record already processed")
	}

	logger.Debug("persisting clerk state", "sideTxResult", sideTxResult)

	// sequence id
	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(heimdallTypes.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// create the event record
	record := types.NewEventRecord(
		msg.TxHash,
		msg.LogIndex,
		msg.Id,
		msg.ContractAddress,
		msg.Data,
		msg.ChainId,
		ctx.BlockTime(),
	)

	// save event into state
	if err := srv.SetEventRecord(ctx, record); err != nil {
		logger.Error("unable to update event record", "id", msg.Id, "error", err)
		return err
	}

	// save the record sequence
	srv.SetRecordSequence(ctx, sequence.String())

	// tx bytes
	txBytes := ctx.TxBytes()
	hash := txBytes

	// add events
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeRecord,
			sdk.NewAttribute(sdk.AttributeKeyAction, msg.Type()),                       // action
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),     // module name
			sdk.NewAttribute(heimdallTypes.AttributeKeyTxHash, common.Bytes2Hex(hash)), // tx hash
			sdk.NewAttribute(types.AttributeKeyRecordTxLogIndex, strconv.FormatUint(msg.LogIndex, 10)),
			sdk.NewAttribute(heimdallTypes.AttributeKeySideTxResult, sideTxResult.String()), // result
			sdk.NewAttribute(types.AttributeKeyRecordID, strconv.FormatUint(msg.Id, 10)),
			sdk.NewAttribute(types.AttributeKeyRecordContract, msg.ContractAddress),
		),
	})

	return nil
}

// recordClerkMetric records metrics for side and post handlers.
func recordClerkMetric(method string, apiType string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.ClerkSubsystem, method, apiType, success, start)
}
