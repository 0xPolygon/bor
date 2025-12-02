package processor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// SpanProcessor - process span related events
type SpanProcessor struct {
	BaseProcessor

	// header listener subscription
	cancelSpanService context.CancelFunc
}

// Start starts new block subscription
func (sp *SpanProcessor) Start() error {
	sp.Logger.Info("starting bor process")

	// create cancellable context
	spanCtx, cancelSpanService := context.WithCancel(context.Background())

	sp.cancelSpanService = cancelSpanService

	// start polling for span
	sp.Logger.Info("start polling for span", "pollInterval", helper.GetConfig().SpanPollInterval)

	go sp.startPolling(spanCtx, helper.GetConfig().SpanPollInterval)

	return nil
}

// RegisterTasks - nil
func (sp *SpanProcessor) RegisterTasks() {
}

// startPolling polls heimdall and checks if a new span needs to be proposed
func (sp *SpanProcessor) startPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	// stop ticker when everything done
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sp.checkAndPropose(ctx)
			// sp.checkAndVoteProducers() //nolint:contextcheck
		case <-ctx.Done():
			sp.Logger.Info("Polling stopped")
			ticker.Stop()

			return
		}
	}
}

// checkAndPropose checks if the current user is the span proposer and proposes the span
func (sp *SpanProcessor) checkAndPropose(ctx context.Context) {
	isProposer, err := util.IsProposer(sp.cliCtx.Codec)
	if err != nil {
		sp.Logger.Error("Error while checking if proposer", "error", err)
		return
	}

	if !isProposer {
		sp.Logger.Debug("Not the proposer, skipping span proposal")
		return
	}

	lastSpan, err := sp.getLastSpan()
	if err != nil {
		sp.Logger.Error("Unable to fetch last span", "error", err)
		return
	}

	if lastSpan == nil {
		sp.Logger.Debug("Last span not found")
		return
	}

	latestBlock, err := sp.contractCaller.GetBorChainBlock(ctx, nil)
	if err != nil {
		sp.Logger.Error("Error fetching current child block", "error", err)
		return
	}
	latestBorBlockNumber := latestBlock.Number.Uint64()

	if latestBorBlockNumber < lastSpan.StartBlock {
		sp.Logger.Debug("Current bor block is less than last span start block, skipping proposing span",
			"lastBlock", latestBorBlockNumber,
			"lastSpanStartBlock", lastSpan.StartBlock,
		)
		return
	}

	sp.Logger.Debug("Found last span",
		"lastSpan", lastSpan.Id,
		"startBlock", lastSpan.StartBlock,
		"endBlock", lastSpan.EndBlock,
	)

	nextSpanMsg, err := sp.fetchNextSpanDetails(lastSpan.Id+1, lastSpan.EndBlock+1)
	if err != nil {
		sp.Logger.Error("Unable to fetch next span details", "error", err, "lastSpanId", lastSpan.Id)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				sp.Logger.Error("Recovered panic in propose goroutine", "panic", r)
			}
		}()

		if err := sp.propose(ctx, lastSpan, nextSpanMsg); err != nil {
			sp.Logger.Error("Error in propose", "error", err)
		}
	}()
}

//nolint:unused
func (sp *SpanProcessor) checkAndVoteProducers() {
	validatorPubKey := helper.GetPubKey()

	lastSpan, err := sp.getLastSpan()
	if err != nil {
		sp.Logger.Error("Unable to fetch last span", "error", err)
		return
	}

	found := false
	validatorId := uint64(0)

	for _, validator := range lastSpan.ValidatorSet.Validators {
		if bytes.Equal(validator.PubKey, validatorPubKey) {
			validatorId = validator.ValId
			found = true
			break
		}
	}

	if !found {
		sp.Logger.Error("Validator not found in last span", "validatorPubKey", validatorPubKey)
		return
	}

	producerVotes, err := sp.getProducerVotesByValidatorId(validatorId)
	if err != nil {
		sp.Logger.Error("Unable to fetch producer votes", "error", err)
		return
	}

	sp.Logger.Debug("Current producer votes", "votes", producerVotes)

	localProducers := helper.GetProducerVotes()

	sp.Logger.Debug("Local producers", "producers", localProducers)

	needToUpdateVotes := false
	if len(localProducers) != len(producerVotes.Votes) {
		needToUpdateVotes = true
	} else {
		for i, producer := range localProducers {
			if producer != producerVotes.Votes[i] {
				needToUpdateVotes = true
				break
			}
		}
	}

	if needToUpdateVotes {
		err := sp.sendProducerVotes(validatorId, localProducers)
		if err != nil {
			sp.Logger.Error("Error while sending producer votes", "error", err)
		}
	}
}

//nolint:unused
func (sp *SpanProcessor) sendProducerVotes(validatorId uint64, producerVotes []uint64) error {
	sp.Logger.Debug("Updating producer votes", "producers", producerVotes)

	addrString, err := helper.GetAddressString()
	if err != nil {
		sp.Logger.Error("error converting address to string", "err", err)
		return err
	}

	msg := types.MsgVoteProducers{
		Voter:   addrString,
		VoterId: validatorId,
		Votes:   types.ProducerVotes{Votes: producerVotes},
	}

	txRes, err := sp.txBroadcaster.BroadcastToHeimdall(&msg, nil)
	if err != nil {
		sp.Logger.Error("Error while broadcasting span to heimdall", "error", err)
		return err
	}

	if txRes.Code != abci.CodeTypeOK {
		sp.Logger.Error("producer votes tx failed on heimdall", "txHash", txRes.TxHash, "code", txRes.Code)
		return fmt.Errorf("producer votes tx failed on heimdall, code: %d", txRes.Code)
	}

	return nil
}

// propose producers for the next span if needed
func (sp *SpanProcessor) propose(ctx context.Context, lastSpan *types.Span, nextSpanMsg *types.Span) error {
	// call with the last span on record plus new span duration and see if it has been proposed
	currentBlock, err := sp.getCurrentChildBlock(ctx)
	if err != nil {
		return fmt.Errorf("error while fetching current child block: %w", err)
	}

	if lastSpan.StartBlock <= currentBlock && currentBlock <= lastSpan.EndBlock {
		// log new span
		sp.Logger.Info("✅ Proposing new span", "spanId", nextSpanMsg.Id, "startBlock", nextSpanMsg.StartBlock, "endBlock", nextSpanMsg.EndBlock)

		seed, seedAuthor, err := sp.fetchNextSpanSeed(nextSpanMsg.Id)
		if err != nil {
			return fmt.Errorf("error while fetching next span seed: %w", err)
		}

		addrString, err := helper.GetAddressString()
		if err != nil {
			return fmt.Errorf("error converting address to string: %w", err)
		}

		// broadcast to heimdall
		msg := types.MsgProposeSpan{
			SpanId:     nextSpanMsg.Id,
			Proposer:   addrString,
			StartBlock: nextSpanMsg.StartBlock,
			EndBlock:   nextSpanMsg.EndBlock,
			ChainId:    nextSpanMsg.BorChainId,
			Seed:       seed.Bytes(),
			SeedAuthor: seedAuthor,
		}

		// return broadcast to heimdall
		txRes, err := sp.txBroadcaster.BroadcastToHeimdall(&msg, nil) //nolint:contextcheck
		if err != nil {
			return fmt.Errorf("error while broadcasting span to heimdall. spanId: %d, startBlock: %d, endBlock: %d, error: %w",
				nextSpanMsg.Id, nextSpanMsg.StartBlock, nextSpanMsg.EndBlock, err)
		}

		if txRes.Code != abci.CodeTypeOK {
			return fmt.Errorf("propose span tx failed on heimdall, txHash: %s, code: %d, spanId: %d, startBlock: %d, endBlock: %d",
				txRes.TxHash, txRes.Code, nextSpanMsg.Id, nextSpanMsg.StartBlock, nextSpanMsg.EndBlock)
		}

		sp.Logger.Info("Span tx successfully broadcasted to heimdall", "txHash", txRes.TxHash, "spanId", nextSpanMsg.Id, "startBlock", nextSpanMsg.StartBlock, "endBlock", nextSpanMsg.EndBlock)
	}

	return nil
}

// checks span status
func (sp *SpanProcessor) getLastSpan() (*types.Span, error) {
	// fetch the latest start block from heimdall using the rest query
	result, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(util.LatestSpanURL))
	if err != nil {
		sp.Logger.Error("Error while fetching latest span")
		return nil, err
	}

	var lastSpan types.QueryLatestSpanResponse
	if err = sp.cliCtx.Codec.UnmarshalJSON(result, &lastSpan); err != nil {
		sp.Logger.Error("Error unmarshalling span", "error", err)
	}
	return &lastSpan.Span, nil
}

// getProducerVotesByValidatorId gets the producer votes for a given voter id
func (sp *SpanProcessor) getProducerVotesByValidatorId(validatorId uint64) (*types.ProducerVotes, error) {
	req, err := http.NewRequest("GET", helper.GetHeimdallServerEndpoint(fmt.Sprintf(util.ProducerVotesURL, validatorId)), nil)
	if err != nil {
		sp.Logger.Error("Error creating a new request", "error", err)
		return nil, err
	}

	result, err := helper.FetchFromAPI(req.URL.String())
	if err != nil {
		sp.Logger.Error("Error fetching producer votes", "error", err)
		return nil, err
	}

	var res types.QueryProducerVotesByValidatorIdResponse
	if err = sp.cliCtx.Codec.UnmarshalJSON(result, &res); err != nil {
		sp.Logger.Error("Error unmarshalling producer votes", "error", err)
		return nil, err
	}

	return &types.ProducerVotes{Votes: res.Votes}, nil
}

// get span by id
func (sp *SpanProcessor) getSpanById(id uint64) (*types.Span, error) {
	// fetch latest span from heimdall using the rest query
	result, err := helper.FetchFromAPI(fmt.Sprintf(helper.GetHeimdallServerEndpoint(util.SpanByIdURL), strconv.FormatUint(id, 10)))
	if err != nil {
		sp.Logger.Error("Error while fetching latest span")
		return nil, err
	}

	var span types.QuerySpanByIdResponse
	if err = sp.cliCtx.Codec.UnmarshalJSON(result, &span); err != nil {
		sp.Logger.Error("Error unmarshalling span", "error", err)
		return nil, err
	}

	sp.Logger.Debug("Span details", "span", span.Span.String())
	return span.Span, nil
}

// getCurrentChildBlock gets the current child block
func (sp *SpanProcessor) getCurrentChildBlock(ctx context.Context) (uint64, error) {
	childBlock, err := sp.contractCaller.GetBorChainBlock(ctx, nil)
	if err != nil {
		return 0, err
	}

	return childBlock.Number.Uint64(), nil
}

// fetch next span details from heimdall.
func (sp *SpanProcessor) fetchNextSpanDetails(id uint64, start uint64) (*types.Span, error) {
	req, err := http.NewRequest("GET", helper.GetHeimdallServerEndpoint(util.NextSpanInfoURL), nil)
	if err != nil {
		sp.Logger.Error("Error creating a new request", "error", err)
		return nil, err
	}

	configParams, err := util.GetChainmanagerParams(sp.cliCtx.Codec)
	if err != nil {
		sp.Logger.Error("Error while fetching chainmanager params", "error", err)
		return nil, err
	}

	q := req.URL.Query()
	q.Add("span_id", strconv.FormatUint(id, 10))
	q.Add("bor_chain_id", configParams.ChainParams.BorChainId)
	q.Add("start_block", strconv.FormatUint(start, 10))
	req.URL.RawQuery = q.Encode()

	// fetch next span details
	result, err := helper.FetchFromAPI(req.URL.String())
	if err != nil {
		sp.Logger.Error("Error fetching proposers", "error", err)
		return nil, err
	}

	var res types.QueryNextSpanResponse
	if err = sp.cliCtx.Codec.UnmarshalJSON(result, &res); err != nil {
		sp.Logger.Error("Error unmarshalling propose tx msg ", "error", err)
		return nil, err
	}

	sp.Logger.Debug("◽ Generated proposer span msg", "msg", res.Span.String())

	return &res.Span, nil
}

// fetchNextSpanSeed - fetches seed for next span
func (sp *SpanProcessor) fetchNextSpanSeed(id uint64) (common.Hash, string, error) {
	sp.Logger.Info("Sending Rest call to Get Seed for next span")

	response, err := helper.FetchFromAPI(fmt.Sprintf(helper.GetHeimdallServerEndpoint(util.NextSpanSeedURL), strconv.FormatUint(id, 10)))
	if err != nil {
		sp.Logger.Error("Error Fetching next span seed from HeimdallServer ", "error", err)
		return common.Hash{}, "", err
	}

	sp.Logger.Info("Next span seed fetched")

	var nextSpanSeedRes types.QueryNextSpanSeedResponse
	if err := sp.cliCtx.Codec.UnmarshalJSON(response, &nextSpanSeedRes); err != nil {
		sp.Logger.Error("Error unmarshalling nextSpanSeed received from Heimdall Server", "error", err)
		return common.Hash{}, "", err
	}

	return common.HexToHash(nextSpanSeedRes.Seed), nextSpanSeedRes.SeedAuthor, nil
}

// Stop stops all necessary go routines
func (sp *SpanProcessor) Stop() {
	// cancel span polling
	sp.cancelSpanService()
}
