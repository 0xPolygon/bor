package keeper

import (
	"bytes"
	"errors"
	"math/big"
	"time"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authTypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	heimdallTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/topup/types"
)

var topupMsgTypeURL = sdk.MsgTypeURL(&types.MsgTopupTx{})

type sideMsgServer struct {
	k *Keeper
}

// NewSideMsgServerImpl returns an implementation of the x/topup SideMsgServer interface for the provided Keeper.
func NewSideMsgServerImpl(keeper *Keeper) sidetxs.SideMsgServer {
	return &sideMsgServer{k: keeper}
}

// SideTxHandler redirects to the right sideMsgServer side_handler based on methodName
func (s sideMsgServer) SideTxHandler(methodName string) sidetxs.SideTxHandler {
	switch methodName {
	case topupMsgTypeURL:
		return s.SideHandleTopupTx
	default:
		return nil
	}
}

// PostTxHandler redirects to the right sideMsgServer post_handler based on methodName
func (s sideMsgServer) PostTxHandler(methodName string) sidetxs.PostTxHandler {
	switch methodName {
	case topupMsgTypeURL:
		return s.PostHandleTopupTx
	default:
		return nil
	}
}

// SideHandleTopupTx handles the side tx for a validator's topup tx
func (s sideMsgServer) SideHandleTopupTx(ctx sdk.Context, msgI sdk.Msg) sidetxs.Vote {
	var err error
	startTime := time.Now()
	defer recordTopupMetric(api.SideHandleTopupTxMethod, api.SideType, startTime, &err)

	logger := s.k.Logger(ctx)

	msg, ok := msgI.(*types.MsgTopupTx)
	if !ok {
		logger.Error("type mismatch for MsgTopupTx")
		return sidetxs.Vote_VOTE_NO
	}

	logger.Debug("validating external call for topup msg",
		"txHash", string(msg.TxHash),
		"logIndex", msg.LogIndex,
		"blockNumber", msg.BlockNumber,
	)

	// check the feasibility of topup tx based on msg fee
	if msg.Fee.LT(ante.DefaultFeeWantedPerTx[0].Amount) {
		logger.Error("default fee exceeds amount to topup", "user", msg.User,
			"amount", msg.Fee, "defaultFeeWantedPerTx", ante.DefaultFeeWantedPerTx[0])
		return sidetxs.Vote_VOTE_NO
	}

	params, err := s.k.ChainKeeper.GetParams(ctx)
	if err != nil {
		return sidetxs.Vote_VOTE_NO
	}
	chainParams := params.ChainParams

	// get main tx receipt
	receipt, err := s.k.contractCaller.GetConfirmedTxReceipt(common.BytesToHash(msg.TxHash), params.MainChainTxConfirmations)
	if err != nil || receipt == nil {
		return sidetxs.Vote_VOTE_NO
	}

	// get event log for topup
	eventLog, err := s.k.contractCaller.DecodeValidatorTopupFeesEvent(chainParams.StakingInfoAddress, receipt, msg.LogIndex)
	if err != nil || eventLog == nil {
		logger.Error("error fetching log from txHash for DecodeValidatorTopupFeesEvent")
		return sidetxs.Vote_VOTE_NO
	}

	if receipt.BlockNumber.Uint64() != msg.BlockNumber {
		logger.Error("blockNumber in message doesn't match blockNumber in receipt", "msgBlockNumber", msg.BlockNumber, "receiptBlockNumber", receipt.BlockNumber.Uint64)
		return sidetxs.Vote_VOTE_NO
	}

	ac := address.NewHexCodec()
	msgAddrBytes, err := ac.StringToBytes(msg.User)
	if err != nil {
		logger.Error("error converting msg.User to bytes", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	eventLogBytes, err := ac.StringToBytes(eventLog.User.String())
	if err != nil {
		logger.Error("error converting eventLog.User to bytes", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	if !bytes.Equal(eventLogBytes, msgAddrBytes) {
		logger.Error(
			"user address from contract event log does not match with user from topup message",
			"eventUser", eventLog.User.String(),
			"msgUser", msg.User,
		)

		return sidetxs.Vote_VOTE_NO
	}

	if eventLog.Fee.Cmp(msg.Fee.BigInt()) != 0 {
		logger.Error("fee in message doesn't match fee in event logs", "msgFee", msg.Fee, "eventFee", eventLog.Fee)
		return sidetxs.Vote_VOTE_NO
	}

	logger.Debug("Successfully validated external call for topup msg")

	return sidetxs.Vote_VOTE_YES
}

// PostHandleTopupTx handles the post-handler tx for a validator's topup tx
func (s sideMsgServer) PostHandleTopupTx(ctx sdk.Context, msgI sdk.Msg, sideTxResult sidetxs.Vote) error {
	var err error
	startTime := time.Now()
	defer recordTopupMetric(api.PostHandleTopupTxMethod, api.PostType, startTime, &err)

	logger := s.k.Logger(ctx)

	msg, ok := msgI.(*types.MsgTopupTx)
	if !ok {
		err := errors.New("type mismatch for MsgTopupTx")
		logger.Error(err.Error())
		return err
	}

	// skip handler if topup is not approved
	if sideTxResult != sidetxs.Vote_VOTE_YES {
		logger.Debug("skipping new topup tx since side-tx didn't get yes votes")
		return errors.New("side-tx didn't get yes votes")
	}

	blockNumber := new(big.Int).SetUint64(msg.BlockNumber)
	sequence := new(big.Int).Mul(blockNumber, big.NewInt(types.DefaultLogIndexUnit))
	sequence.Add(sequence, new(big.Int).SetUint64(msg.LogIndex))

	// check if the event has already been processed
	exists, err := s.k.HasTopupSequence(ctx, sequence.String())
	if err != nil {
		logger.Error("error while fetching older topup sequence",
			"sequence", sequence.String(),
			"logIndex", msg.LogIndex,
			"blockNumber", msg.BlockNumber,
			"error", err)
		return err
	}
	if exists {
		logger.Error("older tx found",
			"sequence", sequence.String(),
			"logIndex", msg.LogIndex,
			"blockNumber", msg.BlockNumber,
			"txHash", msg.TxHash)
		return errors.New("older tx found")
	}

	logger.Debug("persisting topup state", "sideTxResult", sideTxResult)

	// create topup event
	user := msg.User
	topupAmount := sdk.Coins{sdk.Coin{Denom: authTypes.FeeToken, Amount: msg.Fee}}

	err = s.k.BankKeeper.MintCoins(ctx, types.ModuleName, topupAmount)
	if err != nil {
		logger.Error("error while minting coins to x/topup module", "topupAmount", topupAmount, "error", err)
		return err
	}

	err = s.k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.MustAccAddressFromHex(user), topupAmount)
	if err != nil {
		logger.Error("error while sending coins from x/topup module to user", "user", user, "topupAmount", topupAmount, "error", err)
		return err
	}

	err = s.k.BankKeeper.SendCoins(ctx, sdk.MustAccAddressFromHex(user), sdk.MustAccAddressFromHex(msg.Proposer), ante.DefaultFeeWantedPerTx)
	if err != nil {
		logger.Error("error while sending coins from user to proposer", "user", user, "proposer", msg.Proposer, "topupAmount", topupAmount, "error", err)
		return err
	}

	logger.Debug("persisted topup state for", "user", user, "topupAmount", topupAmount.String())

	// save topup
	err = s.k.SetTopupSequence(ctx, sequence.String())
	if err != nil {
		logger.Error("error while saving topup sequence", "sequence", sequence.String(), "error", err)
		return err
	}

	txBytes := ctx.TxBytes()

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeTopup,
			sdk.NewAttribute(sdk.AttributeKeyAction, msg.Type()),
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(heimdallTypes.AttributeKeyTxHash, common.Bytes2Hex(txBytes)),
			sdk.NewAttribute(heimdallTypes.AttributeKeySideTxResult, sideTxResult.String()),
			sdk.NewAttribute(types.AttributeKeySender, msg.Proposer),
			sdk.NewAttribute(types.AttributeKeyRecipient, msg.User),
			sdk.NewAttribute(types.AttributeKeyTopupAmount, msg.Fee.String()),
		),
	})

	return nil
}

// recordTopupMetric records metrics for side and post handlers.
func recordTopupMetric(method string, apiType string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.TopupSubsystem, method, apiType, success, start)
}
