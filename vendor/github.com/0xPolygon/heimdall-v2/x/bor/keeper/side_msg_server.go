package keeper

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"time"

	cmttypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	heimdallTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/bor/types"
)

var SpanProposeMsgTypeURL = sdk.MsgTypeURL(&types.MsgProposeSpan{})
var FillMissingSpansMsgTypeURL = sdk.MsgTypeURL(&types.MsgBackfillSpans{})

type sideMsgServer struct {
	k *Keeper
}

var _ sidetxs.SideMsgServer = sideMsgServer{}

// NewSideMsgServerImpl returns an implementation of the x/bor SideMsgServer interface for the provided Keeper.
func NewSideMsgServerImpl(keeper *Keeper) sidetxs.SideMsgServer {
	return &sideMsgServer{
		k: keeper,
	}
}

// SideTxHandler returns a side handler for span type messages.
func (s sideMsgServer) SideTxHandler(methodName string) sidetxs.SideTxHandler {
	switch methodName {
	case SpanProposeMsgTypeURL:
		return s.SideHandleMsgSpan
	case FillMissingSpansMsgTypeURL:
		return s.SideHandleMsgBackfillSpans
	default:
		return nil
	}
}

// SideHandleMsgSpan validates external calls required for processing the proposed span
func (s sideMsgServer) SideHandleMsgSpan(ctx sdk.Context, msgI sdk.Msg) sidetxs.Vote {
	var err error
	start := time.Now()
	defer recordBorMetric(api.SideHandleMsgSpanMethod, api.SideType, start, &err)

	logger := s.k.Logger(ctx)

	msg, ok := msgI.(*types.MsgProposeSpan)
	if !ok {
		logger.Error("MsgProposeSpan type mismatch", "msg type received", msgI)
		return sidetxs.Vote_VOTE_NO
	}

	if helper.IsVeblop(msg.StartBlock) {
		logger.Debug("skipping span msg since block height is greater than veblop height", "block height", ctx.BlockHeight(), "veblop height", helper.GetVeblopHeight())
		return sidetxs.Vote_VOTE_NO
	}

	logger.Debug("✅ validating external call for span msg",
		"proposer", msg.Proposer,
		"spanId", msg.SpanId,
		"startBlock", msg.StartBlock,
		"endBlock", msg.EndBlock,
		"seed", msg.Seed,
	)

	params, err := s.k.GetParams(ctx)
	if err != nil {
		logger.Error("error fetching params", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	if params.SpanDuration != (msg.EndBlock - msg.StartBlock + 1) {
		logger.Error("span duration of proposed span is wrong",
			"proposedSpanDuration", msg.EndBlock-msg.StartBlock+1,
			"paramsSpanDuration", params.SpanDuration,
		)

		return sidetxs.Vote_VOTE_NO
	}

	// calculate next span seed locally
	nextSpanSeed, nextSpanSeedAuthor, err := s.k.FetchNextSpanSeed(ctx, msg.SpanId)
	if err != nil {
		logger.Error("error fetching next span seed from mainChain", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	// check if span seed matches or not
	if !bytes.Equal(msg.Seed, nextSpanSeed.Bytes()) {
		logger.Error(
			"span seed does not match",
			"msgSeed", msg.Seed,
			"mainChainSeed", nextSpanSeed.String(),
		)

		return sidetxs.Vote_VOTE_NO
	}

	// check if span seed author matches or not.
	if util.FormatAddress(msg.SeedAuthor) != util.FormatAddress(nextSpanSeedAuthor.Hex()) {
		logger.Error(
			"Span Seed Author does not match",
			"proposer", msg.Proposer,
			"chainID", msg.ChainId,
			"msgSeed", msg.Seed,
			"msgSeedAuthor", msg.SeedAuthor,
			"mainChainSeedAuthor", nextSpanSeedAuthor.Hex(),
			"mainChainSeed", nextSpanSeed,
		)

		return sidetxs.Vote_VOTE_NO
	}

	var latestMilestoneEndBlock uint64
	latestMilestone, err := s.k.mk.GetLastMilestone(ctx)
	if err == nil {
		latestMilestoneEndBlock = latestMilestone.EndBlock
	} else {
		logger.Error("error fetching latest milestone", "error", err)
	}

	// fetch current child block
	childBlock, err := s.k.contractCaller.GetBorChainBlock(ctx, nil)
	if err != nil {
		logger.Error("error fetching current child block", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	lastSpan, err := s.k.GetLastSpan(ctx)
	if err != nil {
		logger.Error("error fetching last span", "error", err)
		return sidetxs.Vote_VOTE_NO
	}

	currentBlock := childBlock.Number.Uint64()

	maxBlockNumber := max(latestMilestoneEndBlock, currentBlock)

	if types.IsBlockCloseToSpanEnd(maxBlockNumber, lastSpan.EndBlock) {
		logger.Debug("current block is close to span end", "currentBlock", currentBlock, "lastSpanEndBlock", lastSpan.EndBlock)
		return sidetxs.Vote_VOTE_NO
	}

	// If we are past end of the last span, we need to backfill before proposing a new span
	if msg.StartBlock <= maxBlockNumber {
		logger.Error("span is already in the past",
			"currentBlock", currentBlock,
			"msgStartBlock", msg.StartBlock,
			"msgEndBlock", msg.EndBlock,
			"latestMilestoneEndBlock", latestMilestoneEndBlock,
			"lastSpanEndBlock", lastSpan.EndBlock,
		)
		return sidetxs.Vote_VOTE_NO
	}

	// check if the proposed span is in-turn or not
	if !(lastSpan.StartBlock <= currentBlock && currentBlock <= lastSpan.EndBlock) {
		logger.Error(
			"span proposed is not in-turn",
			"currentChildBlock", currentBlock,
			"msgStartBlock", msg.StartBlock,
			"msgEndBlock", msg.EndBlock,
		)

		return sidetxs.Vote_VOTE_NO
	}

	logger.Debug("✅ successfully validated external call for span msg")

	return sidetxs.Vote_VOTE_YES
}

func (s sideMsgServer) SideHandleMsgBackfillSpans(ctx sdk.Context, msgI sdk.Msg) sidetxs.Vote {
	return sidetxs.Vote_VOTE_NO
}

// PostTxHandler returns a side handler for span type messages.
func (s sideMsgServer) PostTxHandler(methodName string) sidetxs.PostTxHandler {
	switch methodName {
	case SpanProposeMsgTypeURL:
		return s.PostHandleMsgSpan
	case FillMissingSpansMsgTypeURL:
		return s.PostHandleMsgBackfillSpans
	default:
		return nil
	}
}

// PostHandleMsgSpan handles state persisting span msg
func (s sideMsgServer) PostHandleMsgSpan(ctx sdk.Context, msgI sdk.Msg, sideTxResult sidetxs.Vote) error {
	var err error
	start := time.Now()
	defer recordBorMetric(api.PostHandleMsgSpanMethod, api.PostType, start, &err)

	logger := s.k.Logger(ctx)

	msg, ok := msgI.(*types.MsgProposeSpan)
	if !ok {
		err = errors.New("MsgProposeSpan type mismatch")
		logger.Error(err.Error(), "msg type received", msg)
		return err
	}

	if helper.IsVeblop(msg.StartBlock) {
		logger.Debug("skipping span msg since block height is greater than veblop height", "block height", ctx.BlockHeight(), "veblop height", helper.GetVeblopHeight())
		return nil
	}

	// Skip handler if the span is not approved
	if sideTxResult != sidetxs.Vote_VOTE_YES {
		logger.Debug("skipping new span since side-tx didn't get yes votes")
		return errors.New("side-tx didn't get yes votes")
	}

	// check for replay
	ok, err = s.k.HasSpan(ctx, msg.SpanId)
	if err != nil {
		logger.Error("error occurred while checking for span", "span id", msg.SpanId, "error", err)
		return err
	}
	if ok {
		logger.Debug("skipping new span as it's already processed", "span id", msg.SpanId)
		return errors.New("span already processed")
	}

	logger.Debug("persisting span state", "span id", msg.SpanId, "sideTxResult", sideTxResult)

	seedAuthor := common.HexToAddress(msg.SeedAuthor)
	if err = s.k.StoreSeedProducer(ctx, msg.SpanId, &seedAuthor); err != nil {
		logger.Error("Unable to store seed producer", "error", err)
		return err
	}

	// freeze for new span
	err = s.k.FreezeSet(ctx, msg.SpanId, msg.StartBlock, msg.EndBlock, msg.ChainId, common.Hash(msg.Seed))
	if err != nil {
		logger.Error("unable to freeze validator set for span", "span id", msg.SpanId, "error", err)
		return err
	}

	txBytes := ctx.TxBytes()
	hash := cmttypes.Tx(txBytes).Hash()

	// add events
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeProposeSpan,
			sdk.NewAttribute(sdk.AttributeKeyAction, msg.Type()),                               // action
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),             // module name
			sdk.NewAttribute(heimdallTypes.AttributeKeyTxHash, common.BytesToHash(hash).Hex()), // tx hash
			sdk.NewAttribute(heimdallTypes.AttributeKeySideTxResult, sideTxResult.String()),    // result
			sdk.NewAttribute(types.AttributeKeySpanID, strconv.FormatUint(msg.SpanId, 10)),
			sdk.NewAttribute(types.AttributeKeySpanStartBlock, strconv.FormatUint(msg.StartBlock, 10)),
			sdk.NewAttribute(types.AttributeKeySpanEndBlock, strconv.FormatUint(msg.EndBlock, 10)),
		),
	})

	return nil
}

func (s sideMsgServer) PostHandleMsgBackfillSpans(ctx sdk.Context, msgI sdk.Msg, sideTxResult sidetxs.Vote) error {
	var err error
	start := time.Now()
	defer recordBorMetric(api.PostHandleMsgBackfillSpansMethod, api.PostType, start, &err)

	logger := s.k.Logger(ctx)

	msg, ok := msgI.(*types.MsgBackfillSpans)
	if !ok {
		err = errors.New("MsgBackfillSpans type mismatch")
		logger.Error(err.Error(), "msg type received", msg)
		return err
	}

	if helper.IsVeblop(msg.LatestSpanId) {
		logger.Debug("skipping backfill spans msg since span id is greater than veblop height", "span id", msg.LatestSpanId, "veblop height", helper.GetVeblopHeight())
		return nil
	}

	if sideTxResult != sidetxs.Vote_VOTE_YES {
		logger.Debug("skipping new span since side-tx didn't get yes votes")
		return errors.New("side-tx didn't get yes votes")
	}

	latestMilestone, err := s.k.mk.GetLastMilestone(ctx)
	if err != nil {
		logger.Error("failed to get latest milestone", "error", err)
		return fmt.Errorf("failed to get latest milestone: %w", err)
	}

	if latestMilestone == nil {
		logger.Error("latest milestone is nil")
		return types.ErrLatestMilestoneNotFound
	}

	latestSpan, err := s.k.GetSpan(ctx, msg.LatestSpanId)
	if err != nil {
		logger.Error("failed to get latest span", "error", err)
		return err
	}

	borSpans := types.GenerateBorCommittedSpans(latestMilestone.EndBlock, &latestSpan)
	spansOverlap := 0
	for i := range borSpans {
		if _, err := s.k.GetSpan(ctx, borSpans[i].Id); err == nil {
			spansOverlap++
		}
		if spansOverlap > 1 {
			logger.Error("more than one span overlap detected", "span id", borSpans[i].Id)
			return fmt.Errorf("more than one span overlap detected for span id: %d", borSpans[i].Id)
		}
		if err = s.k.AddNewSpan(ctx, &borSpans[i]); err != nil {
			logger.Error("Unable to store spans", "error", err)
			return err
		}
	}

	txBytes := ctx.TxBytes()
	hash := cmttypes.Tx(txBytes).Hash()

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeProposeSpan,
			sdk.NewAttribute(sdk.AttributeKeyAction, msg.Type()),
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(heimdallTypes.AttributeKeyTxHash, common.BytesToHash(hash).Hex()),
			sdk.NewAttribute(heimdallTypes.AttributeKeySideTxResult, sideTxResult.String()),
			sdk.NewAttribute(types.AttributesKeyLatestSpanId, strconv.FormatUint(msg.LatestSpanId, 10)),
			sdk.NewAttribute(types.AttributesKeyLatestBorSpanId, strconv.FormatUint(borSpans[0].Id, 10)),
		),
	})

	return nil
}

// recordBorMetric records metrics for side and post handlers.
func recordBorMetric(method string, apiType string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.BorSubsystem, method, apiType, success, start)
}
