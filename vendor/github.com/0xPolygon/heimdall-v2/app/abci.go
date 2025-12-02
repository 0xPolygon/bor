package app

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtTypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/common/strutil"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/metrics"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	milestoneAbci "github.com/0xPolygon/heimdall-v2/x/milestone/abci"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

const (
	ChangeProducerThreshold = 5
	SpanRotationBuffer      = 10
)

// NewPrepareProposalHandler prepares the proposal after validating the vote extensions
func (app *HeimdallApp) NewPrepareProposalHandler() sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		startTime := time.Now()
		defer metrics.RecordABCIHandlerDuration(metrics.PrepareProposalDuration, startTime)

		logger := app.Logger()

		validatorSet, err := app.getValidatorSetForHeight(ctx, req.Height)
		if err != nil {
			logger.Error("Error occurred while getting validator set for height in PrepareProposal", "error", err, "height", req.Height)
			return nil, err
		}

		if err := ValidateVoteExtensions(ctx, req.Height, req.LocalLastCommit.Votes, req.LocalLastCommit.Round, validatorSet, app.MilestoneKeeper); err != nil {
			logger.Error("Error occurred while validating VEs in PrepareProposal", err)
			return nil, err
		}

		if err := ValidateNonRpVoteExtensions(ctx, req.Height, req.LocalLastCommit.Votes, validatorSet, app.ChainManagerKeeper, app.CheckpointKeeper, app.caller, logger); err != nil {
			logger.Error("Error occurred while validating non-rp VEs in PrepareProposal", err)
		}

		// prepare the proposal with the vote extensions and the validators set's votes
		var txs [][]byte
		bz, err := req.LocalLastCommit.Marshal()
		if err != nil {
			logger.Error("Error occurred while marshaling the LocalLastCommit in prepare proposal", "error", err)
			return nil, err
		}
		txs = append(txs, bz)

		// init totalTxBytes with the actual size of the marshaled vote info in bytes
		totalTxBytes := len(bz)
		for _, proposedTx := range req.Txs {

			// check if the total tx bytes exceed the max tx bytes of the request
			if totalTxBytes+len(proposedTx) > int(req.MaxTxBytes) {
				continue
			}

			tx, err := app.TxDecode(proposedTx)
			if err != nil {
				return nil, fmt.Errorf("error occurred while decoding tx bytes in PrepareProposalHandler. Error: %w", err)
			}

			// ensure we allow transactions with only one side msg inside
			if sidetxs.CountSideHandlers(app.sideTxCfg, tx) > 1 {
				continue
			}

			// Check for MsgVoteProducers and apply VEBLOP validation during PrepareProposal
			shouldSkip := false
			msgs := tx.GetMsgs()
			for _, msg := range msgs {
				if _, ok := msg.(*borTypes.MsgVoteProducers); ok {
					if err := app.BorKeeper.CanVoteProducers(ctx); err != nil {
						logger.Info("skipping MsgVoteProducers in PrepareProposal", "error", err)
						shouldSkip = true
						break
					}
				}
			}

			if shouldSkip {
				continue
			}

			// run the tx by executing the msg_server handler on the tx msgs and the ante handler
			app.Logger().Info("prepare proposal verify tx", "tx", tx.GetMsgs())
			_, err = app.PrepareProposalVerifyTx(tx)
			if err != nil {
				logger.Error("RunTx returned an error in PrepareProposal", "error", err)
				continue
			}

			totalTxBytes += len(proposedTx)
			txs = append(txs, proposedTx)
		}

		// check if there are less than 1 txs in the request
		if len(txs) < 1 {
			logger.Error(fmt.Sprintf("unexpected behaviour, less than 1 txs proposed by %s", req.ProposerAddress))
			return nil, fmt.Errorf("unexpected behaviour, less than 1 txs proposed by %s", req.ProposerAddress)
		}

		return &abci.ResponsePrepareProposal{Txs: txs}, nil
	}
}

// NewProcessProposalHandler processes the proposal, validates the vote extensions, and reject the proposal in case
// there's no majority. It is implemented by all the validators.
func (app *HeimdallApp) NewProcessProposalHandler() sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		startTime := time.Now()
		defer metrics.RecordABCIHandlerDuration(metrics.ProcessProposalDuration, startTime)

		logger := app.Logger()

		validatorSet, err := app.getValidatorSetForHeight(ctx, req.Height)
		if err != nil {
			logger.Error("Error occurred while getting validator set for height in ProcessProposal", "error", err, "height", req.Height)
			return nil, err
		}

		// check if there are any txs in the request
		if len(req.Txs) < 1 {
			logger.Error("unexpected behaviour, no txs found in the proposal")
			return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
		}

		// extract the ExtendedCommitInfo from the txs (it is encoded at the beginning, index 0)
		extCommitInfo := new(abci.ExtendedCommitInfo)
		extendedCommitTx := req.Txs[0]
		if err := extCommitInfo.Unmarshal(extendedCommitTx); err != nil {
			logger.Error("Error occurred while decoding ExtendedCommitInfo", "height", req.Height, "error", err)
			return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
		}

		if extCommitInfo.Round != req.ProposedLastCommit.Round {
			logger.Error("Received commit round does not match expected round", "expected", req.ProposedLastCommit.Round, "got", extCommitInfo.Round)
			return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
		}

		// validate the vote extensions
		if err := ValidateVoteExtensions(ctx, req.Height, extCommitInfo.Votes, req.ProposedLastCommit.Round, validatorSet, app.MilestoneKeeper); err != nil {
			logger.Error("Invalid vote extension, rejecting proposal", "error", err)
			return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
		}

		// validate non-RP vote extensions
		if err := ValidateNonRpVoteExtensions(ctx, req.Height, extCommitInfo.Votes, validatorSet, app.ChainManagerKeeper, app.CheckpointKeeper, app.caller, logger); err != nil {
			logger.Error("Invalid non-rp vote extension proposal", "error", err)
		}

		// run the rest of the txs
		for _, tx := range req.Txs[1:] {
			txn, err := app.TxDecode(tx)
			if err != nil {
				logger.Error("error occurred while decoding tx bytes in ProcessProposalHandler", "error", err)
				return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
			}

			// ensure we allow transactions with only one side msg inside
			if sidetxs.CountSideHandlers(app.sideTxCfg, txn) > 1 {
				return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
			}

			// Check for MsgVoteProducers and apply VEBLOP validation to reject malicious proposals
			msgs := txn.GetMsgs()
			for _, msg := range msgs {
				if _, ok := msg.(*borTypes.MsgVoteProducers); ok {
					if err := app.BorKeeper.CanVoteProducers(ctx); err != nil {
						logger.Error("rejecting proposal with invalid MsgVoteProducers", "error", err)
						return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
					}
				}
			}

			// run the tx by executing the msg_server handler on the tx msgs and the ante handler
			if _, err := app.ProcessProposalVerifyTx(tx); err != nil {
				// this should never happen, as the txs have already been checked in PrepareProposal
				logger.Error("RunTx returned an error in ProcessProposal", "error", err)
				return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
			}
		}

		// if all validations passed
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}
}

// ExtendVoteHandler extends pre-commit vote
func (app *HeimdallApp) ExtendVoteHandler() sdk.ExtendVoteHandler {
	return func(ctx sdk.Context, req *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
		startTime := time.Now()
		defer metrics.RecordABCIHandlerDuration(metrics.ExtendVoteDuration, startTime)

		logger := app.Logger()
		logger.Debug("Extending Vote!", "height", ctx.BlockHeight())
		defer func() {
			// better debugging with this panic recover routine printing runtime.Stack
			if r := recover(); r != nil {
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, false)
				logger.Error(
					"panic in ExtendVoteHandler",
					"panic", r,
					"stack", string(buf[:n]),
				)
				panic(r)
			}
		}()

		// check if VEs are enabled
		if err := checkIfVoteExtensionsDisabled(ctx, req.Height); err != nil {
			return nil, err
		}

		// prepare the side tx responses
		sideTxRes := make([]sidetxs.SideTxResponse, 0)

		// extract the ExtendedVoteInfo from the txs (it is encoded at the beginning, index 0)
		extCommitInfo := new(abci.ExtendedCommitInfo)

		// check whether ExtendedVoteInfo is encoded at the beginning
		bz := req.Txs[0]
		if err := extCommitInfo.Unmarshal(bz); err != nil {
			logger.Error("Error occurred while decoding ExtendedCommitInfo", "error", err)
			// abnormal behavior since the block got >2/3 pre-votes, so the special tx should have been added
			return nil, errors.New("error occurred while decoding ExtendedCommitInfo, they should have be encoded in the beginning of txs slice")
		}

		dummyVoteExt, err := GetDummyNonRpVoteExtension(req.Height, ctx.ChainID())
		if err != nil {
			logger.Error("Error occurred while getting dummy vote extension", "error", err)
			return nil, err
		}

		nonRpVoteExt := dummyVoteExt

		txs := req.Txs[1:]

		// decode txs and execute side txs
		for _, rawTx := range txs {
			// create a cache wrapped context for stateless execution
			ctx, _ = app.cacheTxContext(ctx)
			tx, err := app.TxDecode(rawTx)
			if err != nil {
				// This tx comes from a block that has already been pre-voted by >2/3 of the voting power, so this should never happen
				return nil, fmt.Errorf("error occurred while decoding tx bytes in ExtendVoteHandler. Error: %w", err)
			}

			// messages represent the side txs (operations performed by modules using the VEs mechanism)
			// e.g. bor, checkpoint, clerk, milestone, stake and topup
			messages := tx.GetMsgs()
			for _, msg := range messages {
				// get the right module's side handler for the message
				sideHandler := app.sideTxCfg.GetSideHandler(msg)
				if sideHandler == nil {
					logger.Debug("No side handler found for the message", "msg", msg)
					continue
				}

				// execute the side handler to collect the votes from the validators
				res := sideHandler(ctx, msg)

				if res == sidetxs.Vote_VOTE_YES && checkpointTypes.IsCheckpointMsg(msg) {
					checkpointMsg, ok := msg.(*types.MsgCheckpoint)
					if !ok {
						logger.Error("ExtendVoteHandler: type mismatch for MsgCheckpoint")
						continue
					}

					nonRpVoteExt = packExtensionWithVote(checkpointMsg.GetSideSignBytes())
				}

				// add the side handler results (YES/NO/UNSPECIFIED votes) to the side tx response
				txHash := cmtTypes.Tx(rawTx).Hash()
				logger.Debug("Adding vote extension", "txHash", txHash, "blockHeight", req.Height, "blockHash", req.Hash, "vote", res)
				ve := sidetxs.SideTxResponse{
					TxHash: txHash,
					Result: res,
				}
				sideTxRes = append(sideTxRes, ve)

				if len(sideTxRes) == maxSideTxResponsesCount {
					break
				}
			}

			if len(sideTxRes) == maxSideTxResponsesCount {
				break
			}
		}

		vt := sidetxs.VoteExtension{
			Height:               req.Height,
			BlockHash:            req.Hash,
			SideTxResponses:      sideTxRes,
			MilestoneProposition: nil,
		}

		getBlockAuthor := func(ctx sdk.Context, blockNumber uint64) ([]common.Address, error) {
			return app.BorKeeper.GetProducersByBlockNumber(ctx, blockNumber)
		}

		milestoneProp, err := milestoneAbci.GenMilestoneProposition(ctx, &app.BorKeeper, &app.MilestoneKeeper, app.caller, getBlockAuthor)
		if err != nil {
			if errors.Is(err, milestoneAbci.ErrNoHeadersFound) {
				logger.Debug("No headers found for generating milestone proposition, continuing without it")
			} else {
				logger.Error("Error occurred while generating milestone proposition", "error", err)
			}
			// We still want to participate in the consensus even if we fail to generate the milestone proposition
		} else if milestoneProp != nil {
			if err := milestoneAbci.ValidateMilestoneProposition(ctx, &app.MilestoneKeeper, milestoneProp); err != nil {
				logger.Error("Invalid milestone proposition", "error", err, "height", req.Height, "milestoneProp", milestoneProp)
				// We don't want to halt consensus because of invalid milestone proposition
			} else {
				vt.MilestoneProposition = milestoneProp
				logger.Debug("Proposed milestone", "hash", strutil.HashesToString(milestoneProp.BlockHashes), "startBlock", milestoneProp.StartBlockNumber, "endBlock", milestoneProp.StartBlockNumber+uint64(len(milestoneProp.BlockHashes)))
			}
		}

		bz, err = vt.Marshal()
		if err != nil {
			logger.Error("Error occurred while marshalling the VoteExtension in ExtendVoteHandler", "error", err)
			return nil, err
		}

		if err := ValidateNonRpVoteExtension(ctx, req.Height, nonRpVoteExt, app.ChainManagerKeeper, app.CheckpointKeeper,
			app.caller); err != nil {
			logger.Error("Error occurred while validating non-rp vote extension", "error", err)
		}

		return &abci.ResponseExtendVote{VoteExtension: bz, NonRpExtension: nonRpVoteExt}, nil
	}
}

// VerifyVoteExtensionHandler performs some sanity checks on the VE received from other validators
func (app *HeimdallApp) VerifyVoteExtensionHandler() sdk.VerifyVoteExtensionHandler {
	return func(ctx sdk.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
		startTime := time.Now()
		defer metrics.RecordABCIHandlerDuration(metrics.VerifyVoteExtensionDuration, startTime)

		logger := app.Logger()
		logger.Debug("Verifying vote extension", "height", ctx.BlockHeight())

		// check if VEs are enabled
		if err := checkIfVoteExtensionsDisabled(ctx, req.Height); err != nil {
			return nil, err
		}

		ac := address.NewHexCodec()
		valAddr, err := ac.BytesToString(req.ValidatorAddress)
		if err != nil {
			return nil, err
		}

		var voteExtension sidetxs.VoteExtension
		if err := proto.Unmarshal(req.VoteExtension, &voteExtension); err != nil {
			logger.Error("ALERT, VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS! Error while unmarshalling VoteExtension", "validator", valAddr, "error", err)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// ensure block height and hash match
		if req.Height != voteExtension.Height {
			logger.Error("ALERT, VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS!", "block height", req.Height, "consolidatedSideTxResponse height", voteExtension.Height, "validator", valAddr)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		if !bytes.Equal(req.Hash, voteExtension.BlockHash) {
			logger.Error("ALERT, VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS!", "block hash", common.Bytes2Hex(req.Hash), "consolidatedSideTxResponse blockHash", common.Bytes2Hex(voteExtension.BlockHash), "validator", valAddr)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// check for duplicate votes
		txHash, err := validateSideTxResponses(voteExtension.SideTxResponses)
		if err != nil {
			logger.Error("ALERT, VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS!", "validator", valAddr, "tx hash", common.Bytes2Hex(txHash), "error", err)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		if err := ValidateNonRpVoteExtension(ctx, req.Height, req.NonRpVoteExtension, app.ChainManagerKeeper, app.CheckpointKeeper, app.caller); err != nil {
			logger.Error("ALERT, NON-RP VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS!", "validator", valAddr, "error", err)
		}

		if err := milestoneAbci.ValidateMilestoneProposition(ctx, &app.MilestoneKeeper, voteExtension.MilestoneProposition); err != nil {
			logger.Error("ALERT, MILESTONE PROPOSITION VOTE EXTENSION REJECTED. THIS SHOULD NOT HAPPEN; THE VALIDATOR COULD BE MALICIOUS!", "validator", valAddr, "error", err)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
	}
}

func (app *HeimdallApp) updateBlockProducerStatus(ctx sdk.Context, supportingProducerIDs map[uint64]struct{}) error {
	if err := app.BorKeeper.UpdateLatestActiveProducer(ctx, supportingProducerIDs); err != nil {
		app.Logger().Error("Error occurred while updating latest active producer", "error", err)
		return err
	}

	if err := app.BorKeeper.ClearLatestFailedProducer(ctx); err != nil {
		app.Logger().Error("Error occurred while clearing latest failed producer", "error", err)
		return err
	}

	return nil
}

func (app *HeimdallApp) checkAndAddFutureSpan(ctx sdk.Context, majorityMilestone *milestoneTypes.MilestoneProposition, lastSpan borTypes.Span, supportingValidatorIDs map[uint64]struct{}) error {
	logger := app.Logger()

	if majorityMilestone.StartBlockNumber+uint64(len(majorityMilestone.BlockHashes)-1) >= lastSpan.StartBlock && helper.IsVeblop(lastSpan.EndBlock+1) {
		logger.Info("New milestone is greater than the last span, creating a new veblop span", "lastSpan", lastSpan, "newMilestone", majorityMilestone)

		params, err := app.BorKeeper.GetParams(ctx)
		if err != nil {
			logger.Error("Error occurred while getting bor params", "error", err)
			return err
		}

		endBlock := lastSpan.EndBlock + params.SpanDuration - 1

		currentProducer, err := app.BorKeeper.FindCurrentProducerID(ctx, lastSpan.EndBlock)
		if err != nil {
			logger.Error("Error occurred while finding current producer", "error", err)
			return err
		}

		err = app.BorKeeper.AddNewVeblopSpan(ctx, currentProducer, lastSpan.EndBlock+1, endBlock, lastSpan.BorChainId, supportingValidatorIDs, uint64(ctx.BlockHeight()))
		if err != nil {
			logger.Error("Error occurred while adding new veblop span", "error", err)
			return err
		}

		if err := app.updateBlockProducerStatus(ctx, supportingValidatorIDs); err != nil {
			logger.Error("Error occurred while updating block producer status", "error", err)
			return err
		}
	}

	return nil
}

// checkAndRotateCurrentSpan checks if a new veblop span should be created when no milestone has been proposed for a while.
// This is to ensure liveness and rotate producers.
func (app *HeimdallApp) checkAndRotateCurrentSpan(ctx sdk.Context) error {
	logger := app.Logger()

	hasMilestone, err := app.MilestoneKeeper.HasMilestone(ctx)
	if err != nil {
		logger.Error("Error occurred while checking for the last milestone", "error", err)
		return err
	}

	var lastMilestone *milestoneTypes.Milestone

	if hasMilestone {
		lastMilestone, err = app.MilestoneKeeper.GetLastMilestone(ctx)
		if err != nil {
			logger.Error("Error occurred while fetching the last milestone", "error", err)
			return err
		}
	}

	lastMilestoneBlock, err := app.MilestoneKeeper.GetLastMilestoneBlock(ctx)
	if err != nil {
		logger.Error("Error occurred while fetching the last milestone block", "error", err)
		return err
	}

	if lastMilestoneBlock == 0 {
		lastMilestoneBlock = uint64(ctx.BlockHeight())
	}

	diff := ctx.BlockHeight() - int64(lastMilestoneBlock)

	if lastMilestone != nil && lastMilestoneBlock != 0 && diff > ChangeProducerThreshold && helper.IsVeblop(lastMilestone.EndBlock+1) {
		logger.Info("Block finalization time is greater than change producer threshold, creating a new veblop span", "lastMilestone", lastMilestone, "lastMilestoneBlock", lastMilestoneBlock, "diff", diff, "currentBlock", ctx.BlockHeight())

		addSpanCtx, spanCache := app.cacheTxContext(ctx)

		latestActiveProducer, err := app.BorKeeper.GetLatestActiveProducer(ctx)
		if err != nil {
			logger.Error("Error occurred while getting latest active producer", "error", err)
			return err
		}

		lastSpan, err := app.BorKeeper.GetLastSpan(ctx)
		if err != nil {
			logger.Error("Error occurred while getting last span", "error", err)
			return err
		}

		params, err := app.BorKeeper.GetParams(ctx)
		if err != nil {
			logger.Error("Error occurred while getting bor params", "error", err)
			return err
		}

		endBlock := lastSpan.EndBlock

		for endBlock-lastMilestone.EndBlock > 2*params.SpanDuration {
			endBlock -= params.SpanDuration
		}

		if endBlock <= lastMilestone.EndBlock {
			endBlock = lastSpan.EndBlock
			for endBlock <= lastMilestone.EndBlock {
				endBlock += params.SpanDuration
			}
		}

		currentProducer, err := app.BorKeeper.FindCurrentProducerID(ctx, lastMilestone.EndBlock+1)
		if err != nil {
			logger.Error("Error occurred while finding current producer", "error", err)
			return err
		}

		latestFailedProducer, err := app.BorKeeper.GetLatestFailedProducer(ctx)
		if err != nil {
			logger.Error("Error occurred while getting latest failed producer", "error", err)
			return err
		}

		for producerID := range latestFailedProducer {
			delete(latestActiveProducer, producerID)
		}

		delete(latestActiveProducer, currentProducer)

		err = app.BorKeeper.AddNewVeblopSpan(addSpanCtx, currentProducer, lastMilestone.EndBlock+1, endBlock, lastMilestone.BorChainId, latestActiveProducer, uint64(ctx.BlockHeight()))
		if err != nil {
			logger.Warn("Error occurred while adding new veblop span", "error", err)
		} else {
			// update the last milestone block to a future block height to avoid immediately rotating the span in the next block
			err = app.MilestoneKeeper.SetLastMilestoneBlock(addSpanCtx, uint64(ctx.BlockHeight())+SpanRotationBuffer)
			if err != nil {
				logger.Error("Error occurred while setting last milestone block", "error", err)
				return err
			}

			err = app.BorKeeper.AddLatestFailedProducer(addSpanCtx, currentProducer)
			if err != nil {
				logger.Error("Error occurred while adding latest failed producer", "error", err)
				return err
			}
		}

		if err == nil {
			spanCache.Write()
		}
	}
	return nil
}

// PreBlocker application updates every pre block
func (app *HeimdallApp) PreBlocker(ctx sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
	startTime := time.Now()
	defer metrics.RecordABCIHandlerDuration(metrics.PreBlockerDuration, startTime)

	logger := app.Logger()

	// handle the case when the VEs are disabled starting from the next block
	if err := checkIfVoteExtensionsDisabled(ctx, req.Height+1); err != nil {
		return nil, err
	}

	milestoneDeletionHeight := helper.GetMilestoneDeletionHeight()
	if req.Height == milestoneDeletionHeight && milestoneDeletionHeight != -1 {
		// Delete faulty milestone if exists.
		milestoneNumber := helper.GetFaultyMilestoneNumber()
		milestone, err := app.MilestoneKeeper.GetMilestoneByNumber(ctx, milestoneNumber)
		if err != nil {
			logger.Error("Error occurred while getting milestone by number", "error", err, "milestoneNumber", milestoneNumber)
		}
		if milestone != nil {
			if app.MilestoneKeeper.IsFaultyMilestone(*milestone) {
				if err := app.MilestoneKeeper.DeleteMilestone(ctx, milestoneNumber); err != nil {
					logger.Error("Error occurred while deleting milestone", "error", err, "milestoneNumber", milestoneNumber)
				}
			}
		}
		logger.Info("Deleted milestone matching target condition", "milestone", milestoneNumber)
	}

	// Extract ExtendedVoteInfo encoded at the beginning of txs bytes
	extCommitInfo := new(abci.ExtendedCommitInfo)

	// req.Txs must have non-zero length
	if len(req.Txs) == 0 {
		logger.Error("Unexpected behavior, no txs found in the pre-blocker", "height", req.Height)
		return nil, fmt.Errorf("no txs found in the pre-blocker at height %d", req.Height)
	}

	bz := req.Txs[0]
	if err := extCommitInfo.Unmarshal(bz); err != nil {
		logger.Error("Error occurred while unmarshalling ExtendedCommitInfo", "error", err)
		return nil, err
	}

	extVoteInfo := extCommitInfo.Votes

	// handle the case when the VEs are enabled at the initial height
	if req.Height <= retrieveVoteExtensionsEnableHeight(ctx) {
		if len(extVoteInfo) != 0 {
			logger.Error("Unexpected behavior, non-empty VEs found in the initial height's pre-blocker", "height", req.Height)
			return nil, errors.New("non-empty VEs found in the initial height's pre-blocker")
		}
		return app.ModuleManager.PreBlock(ctx)
	}

	// Fetch txs from block n-1 so that we can match them with the approved txs in block n to execute sideTxs
	lastBlockTxs, err := app.StakeKeeper.GetLastBlockTxs(ctx)
	if err != nil {
		logger.Error("Error occurred while fetching last block txs", "error", err)
		return nil, err
	}
	// update last block txs
	err = app.StakeKeeper.SetLastBlockTxs(ctx, req.Txs[1:])
	if err != nil {
		logger.Error("Error occurred while setting last block txs", "error", err)
		return nil, err
	}

	validatorSet, err := app.getValidatorSetForHeight(ctx, req.Height)
	if err != nil {
		logger.Error("Error occurred while getting validator set for height in PreBlocker", "error", err, "height", req.Height)
		return nil, err
	}

	hasMilestone, err := app.MilestoneKeeper.HasMilestone(ctx)
	if err != nil {
		logger.Error("Error occurred while checking for the last milestone", "error", err)
		return nil, err
	}

	var lastEndBlock *uint64 = nil
	var lastEndHash []byte
	if hasMilestone {
		lastMilestone, err := app.MilestoneKeeper.GetLastMilestone(ctx)
		if err != nil {
			logger.Error("Error occurred while fetching the last milestone", "error", err)
			return nil, err
		}
		lastEndBlock = &lastMilestone.EndBlock
		lastEndHash = lastMilestone.Hash
	}

	majorityMilestone, aggregatedProposers, proposer, supportingValidatorIDs, err := milestoneAbci.GetMajorityMilestoneProposition(
		ctx,
		validatorSet,
		extVoteInfo,
		logger,
		lastEndBlock,
		lastEndHash,
	)
	if err != nil {
		logger.Error("Error occurred while getting majority milestone proposition", "error", err)
		return nil, err
	}

	isValidMilestone := false
	if majorityMilestone != nil {
		var lastSpanHeimdallBlock uint64
		if helper.IsVeblop(majorityMilestone.StartBlockNumber) {
			lastSpanHeimdallBlock, err = app.BorKeeper.GetLastSpanBlock(ctx)
			if err != nil {
				logger.Warn("Error occurred while getting last span block", "error", err)
			}
		}

		if err := milestoneAbci.ValidateMilestoneProposition(ctx, &app.MilestoneKeeper, majorityMilestone); err != nil {
			logger.Error("Invalid milestone proposition", "error", err, "height", req.Height, "majorityMilestone", majorityMilestone)
			// We don't want to halt consensus because of invalid majority milestone proposition
		} else if helper.IsVeblop(majorityMilestone.StartBlockNumber) && ctx.BlockHeight() == int64(lastSpanHeimdallBlock)+1 {
			logger.Info("Last span was created in the previous block, skipping milestone addition", "lastSpanHeimdallBlock", lastSpanHeimdallBlock, "currentBlock", ctx.BlockHeight())
		} else {
			isValidMilestone = true
		}
	}

	if isValidMilestone {
		params, err := app.ChainManagerKeeper.GetParams(ctx)
		if err != nil {
			logger.Error("Error occurred while getting chain manager params", "error", err)
			return nil, err
		}

		addMilestoneCtx, msCache := app.cacheTxContext(ctx)

		logger.Debug("Adding milestone", "hashes",
			strutil.HashesToString(majorityMilestone.BlockHashes),
			"startBlock", majorityMilestone.StartBlockNumber,
			"endBlock", majorityMilestone.StartBlockNumber+uint64(len(majorityMilestone.BlockHashes)-1),
			"proposer", proposer,
			"totalDifficulty", majorityMilestone.BlockTds[len(majorityMilestone.BlockHashes)-1],
		)

		if err := app.MilestoneKeeper.AddMilestone(addMilestoneCtx, milestoneTypes.Milestone{
			Proposer:        proposer,
			Hash:            majorityMilestone.BlockHashes[len(majorityMilestone.BlockHashes)-1],
			StartBlock:      majorityMilestone.StartBlockNumber,
			EndBlock:        majorityMilestone.StartBlockNumber + uint64(len(majorityMilestone.BlockHashes)-1),
			BorChainId:      params.ChainParams.BorChainId,
			MilestoneId:     common.Bytes2Hex(aggregatedProposers),
			Timestamp:       uint64(ctx.BlockTime().Unix()),
			TotalDifficulty: majorityMilestone.BlockTds[len(majorityMilestone.BlockHashes)-1],
		}); err != nil {
			logger.Error("Error occurred while adding milestone", "error", err)
			return nil, err
		}

		lastSpan, err := app.BorKeeper.GetLastSpan(ctx)
		if err != nil {
			logger.Error("Error occurred while fetching the last span", "error", err)
			return nil, err
		}

		if helper.IsVeblop(lastSpan.StartBlock + 1) {
			err = app.MilestoneKeeper.SetLastMilestoneBlock(addMilestoneCtx, uint64(ctx.BlockHeight()))
			if err != nil {
				logger.Error("error while setting last milestone block in store", "err", err)
				return nil, err
			}

			err = app.BorKeeper.UpdateValidatorPerformanceScore(addMilestoneCtx, supportingValidatorIDs, uint64(len(majorityMilestone.BlockHashes)))
			if err != nil {
				logger.Error("Error occurred while updating validator performance score", "error", err)
				return nil, err
			}

			if err := app.updateBlockProducerStatus(addMilestoneCtx, supportingValidatorIDs); err != nil {
				logger.Error("Error occurred while updating block producer status", "error", err)
				return nil, err
			}
		}

		if err := app.checkAndAddFutureSpan(addMilestoneCtx, majorityMilestone, lastSpan, supportingValidatorIDs); err != nil {
			return nil, err
		}
		msCache.Write()
	} else {
		if err := app.checkAndRotateCurrentSpan(ctx); err != nil {
			return nil, err
		}
	}

	// tally votes
	approvedTxs, _, _, err := tallyVotes(extVoteInfo, logger, validatorSet.GetTotalVotingPower(), req.Height)
	if err != nil {
		logger.Error("Error occurred while tallying votes", "error", err)
		return nil, err
	}

	approvedTxsMap := make(map[string]bool)
	for _, tx := range approvedTxs {
		approvedTxsMap[common.Bytes2Hex(tx)] = true
	}

	txs := lastBlockTxs.Txs

	majorityExt, err := getMajorityNonRpVoteExtension(ctx, extVoteInfo, validatorSet, logger)
	if err != nil {
		logger.Error("Error occurred while getting majority non-rp vote extension", "error", err)
		return nil, err
	}

	checkpointTxHash := findCheckpointTx(txs, majorityExt[1:], app, logger) // skip first byte because it's the vote
	if approvedTxsMap[checkpointTxHash] {
		signatures := getCheckpointSignatures(majorityExt, extVoteInfo)
		if err := app.CheckpointKeeper.SetCheckpointSignaturesTxHash(ctx, checkpointTxHash); err != nil {
			logger.Error("Error occurred while setting checkpoint signatures tx hash", "error", err)
			return nil, err
		}
		if err := app.CheckpointKeeper.SetCheckpointSignatures(ctx, signatures); err != nil {
			logger.Error("Error occurred while setting checkpoint signatures", "error", err)
			return nil, err
		}
	}

	// execute side txs
	for _, rawTx := range txs {
		decodedTx, err := app.TxDecode(rawTx)
		if err != nil {
			logger.Error("Error occurred while decoding tx bytes", "error", err)
			return nil, err
		}

		var txBytes cmtTypes.Tx = rawTx

		txHash := common.Bytes2Hex(txBytes.Hash())

		if approvedTxsMap[txHash] {

			// execute post handler for the approved side tx
			msgs := decodedTx.GetMsgs()
			executedPostHandlers := 0
			for _, msg := range msgs {
				if checkpointTypes.IsCheckpointMsg(msg) && checkpointTxHash != txHash {
					logger.Debug("Skipping checkpoint message since it is not the one that generated the signatures", "msg", msg)
					continue
				}

				postHandler := app.sideTxCfg.GetPostHandler(msg)
				if postHandler != nil {
					// Create a new context based off of the existing context with a cache wrapped
					// multi-store in case message processing fails.
					postHandlerCtx, msCache := app.cacheTxContext(ctx)
					postHandlerCtx = postHandlerCtx.WithTxBytes(txBytes.Hash())
					err = postHandler(postHandlerCtx, msg, sidetxs.Vote_VOTE_YES)
					if err == nil {
						msCache.Write()
					} else {
						logger.Error("Error occurred while executing post handler", "error", err, "msg", msg)
					}

					executedPostHandlers++
				}

				// make sure only one post handler is executed
				if executedPostHandlers > 0 {
					logger.Info("One post handler already executed, skipping others", "msg", msg)
					break
				}
			}

		}
	}

	// set the block proposer
	addr, err := sdk.HexifyAddressBytes(req.ProposerAddress)
	if err != nil {
		return nil, err
	}

	account, err := sdk.AccAddressFromHex(addr)
	if err != nil {
		return nil, err
	}
	err = app.AccountKeeper.SetBlockProposer(ctx, account)
	if err != nil {
		app.Logger().Error("error while setting the block proposer", "error", err)
		return nil, err
	}

	return app.ModuleManager.PreBlock(ctx)
}

func (app *HeimdallApp) getValidatorSetForHeight(ctx sdk.Context, height int64) (*stakeTypes.ValidatorSet, error) {
	var (
		validatorSet *stakeTypes.ValidatorSet
		err          error
	)

	// for unit tests and devnets, check whether we are at least 2 heights post the initial height
	// before using the penultimate block validator set
	if height >= helper.GetTallyFixHeight() && height >= helper.GetInitialHeight()+2 {
		// use validator set from 2 blocks ago
		validatorSet, err = getPenultimateBlockValidatorSet(ctx, app.StakeKeeper)
		if err != nil {
			return nil, fmt.Errorf("failed to get penultimate block validator set: %w", err)
		}
	} else {
		// use previous block validator set (legacy behavior)
		validatorSet, err = getPreviousBlockValidatorSet(ctx, app.StakeKeeper)
		if err != nil {
			return nil, fmt.Errorf("failed to get previous block validator set: %w", err)
		}
	}
	return validatorSet, nil
}
