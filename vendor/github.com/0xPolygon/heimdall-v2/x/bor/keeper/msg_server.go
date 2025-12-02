package keeper

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/errors"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/ethereum/go-ethereum/common"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/metrics/api"
	"github.com/0xPolygon/heimdall-v2/x/bor/types"
)

type msgServer struct {
	Keeper
}

var _ types.MsgServer = msgServer{}

// NewMsgServerImpl returns an implementation of the bor MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

func (m msgServer) ProposeSpan(ctx context.Context, msg *types.MsgProposeSpan) (*types.MsgProposeSpanResponse, error) {
	var err error
	start := time.Now()
	defer recordBorTransactionMetric(api.ProposeSpanMethod, start, &err)

	logger := m.Logger(ctx)

	logger.Debug("✅ validating proposed span msg",
		"proposer", msg.Proposer,
		"spanId", msg.SpanId,
		"startBlock", msg.StartBlock,
		"endBlock", msg.EndBlock,
		"seed", msg.Seed,
	)

	_, err = sdk.ValAddressFromHex(msg.Proposer)
	if err != nil {
		logger.Error("invalid proposer address", "error", err)
		return nil, errors.Wrapf(err, "invalid proposer address")
	}

	// verify chain id
	chainParams, err := m.ck.GetParams(ctx)
	if err != nil {
		logger.Error("failed to get chain params", "error", err)
		return nil, errors.Wrapf(err, "failed to get chain params")
	}

	if chainParams.ChainParams.BorChainId != msg.ChainId {
		logger.Error("invalid bor chain id", "expected", chainParams.ChainParams.BorChainId, "got", msg.ChainId)
		return nil, types.ErrInvalidChainID
	}

	// verify seed length
	if len(msg.Seed) != common.HashLength {
		logger.Error("invalid seed length", "expected", common.HashLength, "got", len(msg.Seed))
		return nil, types.ErrInvalidSeedLength
	}

	lastSpan, err := m.GetLastSpan(ctx)
	if err != nil {
		logger.Error("unable to fetch last span", "Error", err)
		return nil, errors.Wrapf(err, "unable to fetch last span")
	}

	// Validate span continuity
	if lastSpan.Id+1 != msg.SpanId || msg.StartBlock != lastSpan.EndBlock+1 || msg.EndBlock <= msg.StartBlock {
		logger.Error("blocks not in continuity",
			"lastSpanId", lastSpan.Id,
			"spanId", msg.SpanId,
			"lastSpanStartBlock", lastSpan.StartBlock,
			"lastSpanEndBlock", lastSpan.EndBlock,
			"spanStartBlock", msg.StartBlock,
			"spanEndBlock", msg.EndBlock,
		)

		return nil, types.ErrInvalidSpan
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// add events
	sdkCtx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeProposeSpan,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(types.AttributeKeySpanID, strconv.FormatUint(msg.SpanId, 10)),
			sdk.NewAttribute(types.AttributeKeySpanStartBlock, strconv.FormatUint(msg.StartBlock, 10)),
			sdk.NewAttribute(types.AttributeKeySpanEndBlock, strconv.FormatUint(msg.EndBlock, 10)),
		),
	})

	logger.Debug("Emitted propose-span event")
	return &types.MsgProposeSpanResponse{}, nil
}

// UpdateParams defines a method to update the params in x/bor module.
func (m msgServer) UpdateParams(ctx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	var err error
	start := time.Now()
	defer recordBorTransactionMetric(api.BorUpdateParamsMethod, start, &err)

	if m.authority != msg.Authority {
		return nil, errors.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", m.authority, msg.Authority)
	}

	if err := msg.Params.ValidateBasic(); err != nil {
		return nil, err
	}

	if err := m.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}

func (m msgServer) VoteProducers(ctx context.Context, msg *types.MsgVoteProducers) (*types.MsgVoteProducersResponse, error) {
	var err error
	start := time.Now()
	defer recordBorTransactionMetric(api.VoteProducersMethod, start, &err)

	// Validate VEBLOP phase
	if err := m.CanVoteProducers(ctx); err != nil {
		return nil, err
	}

	voter, err := sdk.AccAddressFromHex(msg.Voter)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid voter address")
	}

	validator, err := m.sk.GetValidatorFromValID(ctx, msg.VoterId)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid voter id")
	}

	pk := secp256k1.PubKey(validator.PubKey)

	if util.FormatAddress(voter.String()) != util.FormatAddress(pk.Address().String()) {
		return nil, fmt.Errorf("voter address %s does not match validator address %s under validator id %d", voter.String(), pk.Address().String(), msg.VoterId)
	}

	// Check if there are any duplicate votes in the msg.Votes
	seen := make(map[uint64]bool)
	for _, vote := range msg.Votes.Votes {
		if seen[vote] {
			return nil, fmt.Errorf("duplicate vote for validator id %d", vote)
		}
		seen[vote] = true
	}

	err = m.SetProducerVotes(ctx, msg.VoterId, msg.Votes)
	if err != nil {
		return nil, err
	}

	return &types.MsgVoteProducersResponse{}, nil
}

func (s msgServer) BackfillSpans(ctx context.Context, msg *types.MsgBackfillSpans) (*types.MsgBackfillSpansResponse, error) {
	var err error
	start := time.Now()
	defer recordBorTransactionMetric(api.BackfillSpansMethod, start, &err)

	logger := s.Logger(ctx)

	logger.Debug("✅ validating proposed backfill spans msg",
		"proposer", msg.Proposer,
		"latestSpanId", msg.LatestSpanId,
		"latestBorSpanId", msg.LatestBorSpanId,
		"chainId", msg.ChainId,
	)

	_, err = sdk.ValAddressFromHex(msg.Proposer)
	if err != nil {
		logger.Error("invalid proposer address", "error", err)
		return nil, errors.Wrapf(err, "invalid proposer address")
	}

	chainParams, err := s.ck.GetParams(ctx)
	if err != nil {
		logger.Error("failed to get chain params", "error", err)
		return nil, errors.Wrapf(err, "failed to get chain params")
	}

	if chainParams.ChainParams.BorChainId != msg.ChainId {
		logger.Error("invalid bor chain id", "expected", chainParams.ChainParams.BorChainId, "got", msg.ChainId)
		return nil, types.ErrInvalidChainID
	}

	latestSpan, err := s.GetLastSpan(ctx)
	if err != nil {
		logger.Error("failed to get latest span", "error", err)
		return nil, errors.Wrapf(err, "failed to get latest span")
	}

	if msg.LatestSpanId != latestSpan.Id && msg.LatestSpanId != latestSpan.Id-1 {
		logger.Error("invalid latest span id", "expected",
			fmt.Sprintf("%d or %d", latestSpan.Id, latestSpan.Id-1), "got", msg.LatestSpanId)
		return nil, types.ErrInvalidSpan
	}

	if msg.LatestBorSpanId <= msg.LatestSpanId {
		logger.Error("invalid bor span id, expected greater than latest span id",
			"latestSpanId", latestSpan.Id,
			"latestBorSpanId", msg.LatestBorSpanId,
		)
		return nil, types.ErrInvalidLastBorSpanID
	}

	latestMilestone, err := s.mk.GetLastMilestone(ctx)
	if err != nil {
		logger.Error("failed to get latest milestone", "error", err)
		return nil, errors.Wrapf(err, "failed to get latest milestone")
	}

	if latestMilestone == nil {
		logger.Error("latest milestone is nil")
		return nil, types.ErrLatestMilestoneNotFound
	}

	borLastUsedSpan, err := s.GetSpan(ctx, msg.LatestSpanId)
	if err != nil {
		logger.Error("failed to get last used bor span", "error", err)
		return nil, errors.Wrapf(err, "failed to get last used bor span")
	}

	borSpanId, err := types.CalcCurrentBorSpanId(latestMilestone.EndBlock, &borLastUsedSpan)
	if err != nil {
		logger.Error("failed to calculate bor span id", "error", err)
		return nil, errors.Wrapf(err, "failed to calculate bor span id")
	}

	if borSpanId != msg.LatestBorSpanId {
		logger.Error(
			"bor span id mismatch",
			"calculatedBorSpanId", borSpanId,
			"msgLatestBorSpanId", msg.LatestBorSpanId,
			"latestMilestoneEndBlock", latestMilestone.EndBlock,
			"latestSpanStartBlock", latestSpan.StartBlock,
			"latestSpanEndBlock", latestSpan.EndBlock,
			"latestSpanId", latestSpan.Id,
		)
		return nil, types.ErrInvalidSpan
	}

	return &types.MsgBackfillSpansResponse{}, nil
}

func recordBorTransactionMetric(method string, start time.Time, err *error) {
	success := *err == nil
	api.RecordAPICallWithStart(api.BorSubsystem, method, api.TxType, success, start)
}
