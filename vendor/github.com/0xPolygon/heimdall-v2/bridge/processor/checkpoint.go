package processor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authlegacytx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethTypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/contracts/rootchain"
	"github.com/0xPolygon/heimdall-v2/helper"
	hmTypes "github.com/0xPolygon/heimdall-v2/types"
	chainmanagertypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	checkpointtypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	topuptypes "github.com/0xPolygon/heimdall-v2/x/topup/types"
)

// CheckpointProcessor - processor for checkpoint queue.
type CheckpointProcessor struct {
	BaseProcessor

	// header listener subscription
	cancelNoACKPolling context.CancelFunc

	// RootChain abi
	rootChainAbi *abi.ABI
}

// CheckpointContext represents checkpoint context
type CheckpointContext struct {
	ChainmanagerParams *chainmanagertypes.Params
	CheckpointParams   *checkpointtypes.Params
}

// NewCheckpointProcessor - add rootChain abi to the checkpoint processor
func NewCheckpointProcessor(rootChainAbi *abi.ABI) *CheckpointProcessor {
	return &CheckpointProcessor{
		rootChainAbi: rootChainAbi,
	}
}

// Start - consumes messages from the checkpoint queue and call processMsg
func (cp *CheckpointProcessor) Start() error {
	cp.Logger.Info("Starting")
	// no-ack
	ackCtx, cancelNoACKPolling := context.WithCancel(context.Background())
	cp.cancelNoACKPolling = cancelNoACKPolling
	cp.Logger.Info("Start polling for no-ack", "pollInterval", helper.GetConfig().NoACKPollInterval)

	go cp.startPollingForNoAck(ackCtx, helper.GetConfig().NoACKPollInterval)

	return nil
}

// RegisterTasks registers the checkpoint-related tasks with machinery
func (cp *CheckpointProcessor) RegisterTasks() {
	cp.Logger.Info("Registering checkpoint tasks")

	if err := cp.queueConnector.Server.RegisterTask("sendCheckpointToHeimdall", cp.sendCheckpointToHeimdall); err != nil {
		cp.Logger.Error("RegisterTasks | sendCheckpointToHeimdall", "error", err)
	}

	if err := cp.queueConnector.Server.RegisterTask("sendCheckpointToRootchain", cp.sendCheckpointToRootChain); err != nil {
		cp.Logger.Error("RegisterTasks | sendCheckpointToRootChain", "error", err)
	}

	if err := cp.queueConnector.Server.RegisterTask("sendCheckpointAckToHeimdall", cp.sendCheckpointAckToHeimdall); err != nil {
		cp.Logger.Error("RegisterTasks | sendCheckpointAckToHeimdall", "error", err)
	}
}

func (cp *CheckpointProcessor) startPollingForNoAck(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			go cp.handleCheckpointNoAck() //nolint:contextcheck
		case <-ctx.Done():
			cp.Logger.Info("No-ack Polling stopped")
			ticker.Stop()

			return
		}
	}
}

// sendCheckpointToHeimdall - handles header block from bor
// 1. check if I am the proposer for next checkpoint
// 2. check if the checkpoint has to be proposed for the given header block
// 3. if so, propose checkpoint to heimdall.
func (cp *CheckpointProcessor) sendCheckpointToHeimdall(headerBlockStr string) (err error) {
	header := ethTypes.Header{}
	if err := header.UnmarshalJSON([]byte(headerBlockStr)); err != nil {
		cp.Logger.Error("Error while unmarshalling the header block", "error", err)
		return err
	}

	cp.Logger.Info("Processing new header", "headerNumber", header.Number)

	isProposer, err := util.IsProposer(cp.cliCtx.Codec)
	if err != nil {
		cp.Logger.Error("Error checking isProposer in HeaderBlock handler", "error", err)
		return err
	}

	if isProposer {
		// fetch checkpoint context
		checkpointContext, err := cp.getCheckpointContext()
		if err != nil {
			return err
		}

		// process the latest confirmed child block only
		chainmanagerParams := checkpointContext.ChainmanagerParams

		cp.Logger.Debug("no of checkpoint confirmations required", "BorChainTxConfirmations", chainmanagerParams.BorChainTxConfirmations)

		latestConfirmedChildBlock := header.Number.Uint64() - chainmanagerParams.BorChainTxConfirmations
		if latestConfirmedChildBlock <= 0 {
			cp.Logger.Error("no of blocks on childChain is less than confirmations required", "childChainBlocks", header.Number.Uint64(), "confirmationsRequired", chainmanagerParams.BorChainTxConfirmations)
			return errors.New("no of blocks on childChain is less than confirmations required")
		}

		expectedCheckpointState, err := cp.nextExpectedCheckpoint(checkpointContext, latestConfirmedChildBlock)
		if err != nil {
			cp.Logger.Error("Error while calculate next expected checkpoint", "error", err)
			return err
		}

		start := expectedCheckpointState.newStart
		end := expectedCheckpointState.newEnd

		//
		// Check checkpoint buffer
		//
		timeStamp := uint64(time.Now().Unix())
		checkpointBufferTime := uint64(checkpointContext.CheckpointParams.CheckpointBufferTime.Seconds())

		bufferedCheckpoint, err := util.GetBufferedCheckpoint(cp.cliCtx.Codec)
		if err != nil {
			cp.Logger.Debug("No buffered checkpoint", "bufferedCheckpoint", bufferedCheckpoint)
		}

		if bufferedCheckpoint != nil && !(bufferedCheckpoint.Timestamp == 0 || ((timeStamp > bufferedCheckpoint.Timestamp) && timeStamp-bufferedCheckpoint.Timestamp >= checkpointBufferTime)) {
			cp.Logger.Info("Checkpoint already exits in buffer", "Checkpoint", bufferedCheckpoint.String())
			return nil
		}

		if err := cp.createAndSendCheckpointToHeimdall(checkpointContext, start, end); err != nil {
			cp.Logger.Error("Error sending checkpoint to heimdall", "error", err)
			return err
		}
	} else {
		cp.Logger.Info("I am not the proposer, skipping new header", "headerNumber", header.Number)
		return
	}

	return nil
}

// sendCheckpointToRootChain - handles checkpoint confirmation event from heimdall.
// 1. check if I am the current proposer.
// 2. check if this checkpoint has to be submitted to rootChain
// 3. if so, create and broadcast the checkpoint transaction to rootChain
func (cp *CheckpointProcessor) sendCheckpointToRootChain(eventBytes string, blockHeight int64) error {
	cp.Logger.Info("Received sendCheckpointToRootChain request", "eventBytes", eventBytes, "blockHeight", blockHeight)

	var event sdk.StringEvent
	if err := cp.cliCtx.Codec.UnmarshalJSON([]byte(eventBytes), &event); err != nil {
		cp.Logger.Error("Error unmarshalling event from heimdall", "error", err)
		return err
	}

	cp.Logger.Info("processing checkpoint confirmation event", "eventType", event.Type)

	isCurrentProposer, err := util.IsCurrentProposer(cp.cliCtx.Codec)
	if err != nil {
		cp.Logger.Error("Error checking isCurrentProposer in CheckpointConfirmation handler", "error", err)
		return err
	}

	var (
		startBlock uint64
		endBlock   uint64
		txHash     string
	)

	for _, attr := range event.Attributes {
		if attr.Key == checkpointtypes.AttributeKeyStartBlock {
			startBlock, _ = strconv.ParseUint(attr.Value, 10, 64)
		}

		if attr.Key == checkpointtypes.AttributeKeyEndBlock {
			endBlock, _ = strconv.ParseUint(attr.Value, 10, 64)
		}

		if attr.Key == hmTypes.AttributeKeyTxHash {
			txHash = attr.Value
		}
	}

	checkpointContext, err := cp.getCheckpointContext()
	if err != nil {
		return err
	}

	shouldSend, err := cp.shouldSendCheckpoint(checkpointContext, startBlock, endBlock)
	if err != nil {
		return err
	}

	if shouldSend && isCurrentProposer {
		txHash := common.FromHex(txHash)
		if err := cp.createAndSendCheckpointToRootChain(checkpointContext, startBlock, endBlock, blockHeight, txHash); err != nil {
			cp.Logger.Error("Error sending checkpoint to rootChain", "error", err)
			return err
		}
	}

	cp.Logger.Info("I am not the current proposer or checkpoint already sent. Ignoring", "eventType", event.Type)

	return nil
}

// sendCheckpointAckToHeimdall - handles checkpointAck event from rootChain
// 1. create and broadcast checkpointAck msg to heimdall.
func (cp *CheckpointProcessor) sendCheckpointAckToHeimdall(eventName string, checkpointAckStr string) error {
	// fetch checkpoint context
	checkpointContext, err := cp.getCheckpointContext()
	if err != nil {
		return err
	}

	log := ethTypes.Log{}
	if err = json.Unmarshal([]byte(checkpointAckStr), &log); err != nil {
		cp.Logger.Error("Error while unmarshalling event from rootChain", "error", err)
		return err
	}

	event := new(rootchain.RootchainNewHeaderBlock)
	if err = helper.UnpackLog(cp.rootChainAbi, event, eventName, &log); err != nil {
		cp.Logger.Error("Error while parsing event", "name", eventName, "error", err)
	} else {
		checkpointNumber := big.NewInt(0).Div(event.HeaderBlockId, big.NewInt(0).SetUint64(checkpointContext.CheckpointParams.ChildChainBlockInterval))

		cp.Logger.Info(
			"✅ Received task to send checkpoint-ack to heimdall",
			"event", eventName,
			"start", event.Start,
			"end", event.End,
			"reward", event.Reward,
			"root", "0x"+hex.EncodeToString(event.Root[:]),
			"proposer", event.Proposer.Hex(),
			"checkpointNumber", checkpointNumber,
			"txHash", log.TxHash.String(),
			"logIndex", uint64(log.Index),
		)

		// fetch latest checkpoint
		latestCheckpoint, err := util.GetLatestCheckpoint(cp.cliCtx.Codec)
		// event checkpoint is older than or equal to the latest checkpoint
		if err == nil && latestCheckpoint != nil && latestCheckpoint.EndBlock >= event.End.Uint64() {
			cp.Logger.Debug("Checkpoint ack is already submitted", "start", event.Start, "end", event.End)
			return nil
		}

		// create msg checkpoint ack message
		msg := checkpointtypes.NewMsgCpAck(
			helper.GetFromAddress(cp.cliCtx),
			checkpointNumber.Uint64(),
			event.Proposer.String(),
			event.Start.Uint64(),
			event.End.Uint64(),
			event.Root[:],
		)

		// return broadcast to heimdall
		txRes, err := cp.txBroadcaster.BroadcastToHeimdall(&msg, event)
		if err != nil {
			cp.Logger.Error("Error while broadcasting checkpoint-ack to heimdall", "error", err)
			return err
		}

		if txRes.Code != abci.CodeTypeOK {
			cp.Logger.Error("checkpoint-ack tx failed on heimdall", "txHash", txRes.TxHash, "code", txRes.Code)
			return fmt.Errorf("checkpoint-ack tx failed, tx response code: %d", txRes.Code)
		}

	}

	return nil
}

// handleCheckpointNoAck - Checkpoint No-Ack handler
// 1. Fetch the latest checkpoint time from rootChain
// 2. Check if elapsed time is more than NoAck Wait time.
// 3. Send NoAck to heimdall if required.
func (cp *CheckpointProcessor) handleCheckpointNoAck() {
	// fetch fresh checkpoint context
	checkpointContext, err := cp.getCheckpointContext()
	if err != nil {
		return
	}

	lastCreatedAt, err := cp.getLatestCheckpointTime(checkpointContext)
	if err != nil {
		cp.Logger.Error("Error fetching latest checkpoint time from rootChain", "error", err)
		return
	}

	isNoAckRequired, count := cp.checkIfNoAckIsRequired(checkpointContext, lastCreatedAt)
	if isNoAckRequired {
		var isProposer bool

		if isProposer, err = util.IsInProposerList(count, cp.cliCtx.Codec); err != nil {
			cp.Logger.Error("Error checking IsInProposerList while proposing Checkpoint No-Ack ", "error", err)
			return
		}

		// if I am the proposer and NoAck is required, then propose No-Ack
		if isProposer {
			// send Checkpoint No-Ack to heimdall
			if err := cp.proposeCheckpointNoAck(); err != nil {
				cp.Logger.Error("Error proposing Checkpoint No-Ack ", "error", err)
				return
			}
		}
	}
}

// nextExpectedCheckpoint - fetched contract checkpoint state and returns the next probable checkpoint that needs to be sent
func (cp *CheckpointProcessor) nextExpectedCheckpoint(checkpointContext *CheckpointContext, latestChildBlock uint64) (*ContractCheckpoint, error) {
	chainmanagerParams := checkpointContext.ChainmanagerParams
	checkpointParams := checkpointContext.CheckpointParams

	rootChainInstance, err := cp.contractCaller.GetRootChainInstance(chainmanagerParams.ChainParams.RootChainAddress)
	if err != nil {
		return nil, err
	}

	// fetch the current header block from rootChain contract
	_currentHeaderBlock, err := cp.contractCaller.CurrentHeaderBlock(rootChainInstance, checkpointParams.ChildChainBlockInterval)
	if err != nil {
		cp.Logger.Error("Error while fetching current header block number from rootChain", "error", err)
		return nil, err
	}

	// current header block
	currentHeaderBlockNumber := big.NewInt(0).SetUint64(_currentHeaderBlock)

	// get header info
	_, currentStart, currentEnd, _, _, err := cp.contractCaller.GetHeaderInfo(currentHeaderBlockNumber.Uint64(), rootChainInstance, checkpointParams.ChildChainBlockInterval)
	if err != nil {
		cp.Logger.Error("Error while fetching current header block object from rootChain", "error", err)
		return nil, err
	}
	// find the next start/end
	var start, end uint64
	start = currentEnd

	// add 1 if start > 0
	if start > 0 {
		start = start + 1
	}

	// get diff
	diff := latestChildBlock - start + 1
	// process if diff > 0 (positive)
	if diff > 0 {
		expectedDiff := diff - diff%checkpointParams.AvgCheckpointLength
		if expectedDiff > 0 {
			expectedDiff = expectedDiff - 1
		}
		// cap with max checkpoint length
		if expectedDiff > checkpointParams.MaxCheckpointLength {
			expectedDiff = checkpointParams.MaxCheckpointLength - 1
		}
		// get the result
		end = expectedDiff + start
		cp.Logger.Debug("Calculating checkpoint eligibility",
			"latest", latestChildBlock,
			"start", start,
			"end", end,
		)
	}

	return NewContractCheckpoint(start, end, &HeaderBlock{
		start:  currentStart,
		end:    currentEnd,
		number: currentHeaderBlockNumber,
	}), nil
}

// createAndSendCheckpointToHeimdall - creates checkpoint msg and broadcasts to heimdall
func (cp *CheckpointProcessor) createAndSendCheckpointToHeimdall(checkpointContext *CheckpointContext, start uint64, end uint64) error {
	cp.Logger.Debug("Initiating checkpoint to Heimdall", "start", start, "end", end)

	if end == 0 || start >= end {
		cp.Logger.Info("Waiting for blocks or invalid start end formation", "start", start, "end", end)
		return nil
	}

	// get checkpoint params
	checkpointParams := checkpointContext.CheckpointParams

	// Get root hash
	root, err := cp.contractCaller.GetRootHash(start, end, checkpointParams.MaxCheckpointLength)
	if err != nil {
		return err
	}

	cp.Logger.Info("Root hash calculated", "rootHash", common.Bytes2Hex(root))

	var accountRootHash []byte
	// get DividendAccountRoot from heimdall
	if accountRootHash, err = cp.fetchDividendAccountRoot(); err != nil {
		cp.Logger.Info("Error while fetching initial account root hash from HeimdallServer", "err", err)
		return err
	}

	cp.Logger.Info("✅ Creating and broadcasting new checkpoint",
		"start", start,
		"end", end,
		"root", common.Bytes2Hex(root),
		"accountRoot", common.Bytes2Hex(accountRootHash),
	)

	chainParams := checkpointContext.ChainmanagerParams.ChainParams

	address, err := helper.GetAddressString()
	if err != nil {
		cp.Logger.Error("Error while converting address to string during checkpoint creation", "error", err)
		return err
	}

	// create and send the checkpoint message
	msg := checkpointtypes.NewMsgCheckpointBlock(
		address,
		start,
		end,
		root,
		accountRootHash,
		chainParams.BorChainId,
	)

	// return broadcast to heimdall
	txRes, err := cp.txBroadcaster.BroadcastToHeimdall(msg, nil)
	if err != nil {
		cp.Logger.Error("Error while broadcasting checkpoint to heimdall", "error", err)
		return err
	}

	if txRes.Code != abci.CodeTypeOK {
		cp.Logger.Error("Checkpoint tx failed on heimdall", "txHash", txRes.TxHash, "code", txRes.Code)
		return fmt.Errorf("checkpoint tx failed, tx response code: %d", txRes.Code)
	}

	return nil
}

// createAndSendCheckpointToRootChain prepares the data required for rootChain checkpoint submission
// and sends a transaction to rootChain
func (cp *CheckpointProcessor) createAndSendCheckpointToRootChain(checkpointContext *CheckpointContext, start uint64, end uint64, height int64, txHash []byte) error {
	cp.Logger.Info("Preparing checkpoint to be pushed on chain", "height", height, "txHash", common.Bytes2Hex(txHash), "start", start, "end", end)
	// proof
	tx, err := helper.QueryTxWithProof(cp.cliCtx, txHash)
	if err != nil {
		cp.Logger.Error("Error querying checkpoint tx proof", "txHash", txHash)
		return err
	}

	// fetch side txs sigs
	decoder := authlegacytx.DefaultTxDecoder(cp.cliCtx.Codec)

	stdTx, err := decoder(tx.Tx)
	if err != nil {
		cp.Logger.Error("Error while decoding checkpoint tx", "txHash", tx.Tx.Hash(), "error", err)
		return err
	}

	msg := stdTx.GetMsgs()[0]

	sideMsg, ok := msg.(*checkpointtypes.MsgCheckpoint)
	if !ok {
		cp.Logger.Error("Invalid side-tx msg", "txHash", tx.Tx.Hash())
		return err
	}

	// side-tx data
	sideTxData := sideMsg.GetSideSignBytes()

	signatures, err := cp.getCheckpointSignatures(common.Bytes2Hex(txHash))
	if err != nil {
		cp.Logger.Error("Error fetching checkpoint signatures", "error", err)
		return err
	}

	sigs, err := cp.parseCheckpointSignatures(signatures)
	if err != nil {
		cp.Logger.Error("Error parsing checkpoint signatures", "error", err)
		return err
	}

	// chain manager params
	chainParams := checkpointContext.ChainmanagerParams.ChainParams
	// root chain address
	rootChainAddress := chainParams.RootChainAddress
	// root chain instance
	rootChainInstance, err := cp.contractCaller.GetRootChainInstance(rootChainAddress)
	if err != nil {
		cp.Logger.Info("Error while creating rootChain instance", "error", err)
		return err
	}

	if err := cp.contractCaller.SendCheckpoint(sideTxData, sigs, common.HexToAddress(rootChainAddress), rootChainInstance); err != nil {
		cp.Logger.Info("Error submitting checkpoint to rootChain", "error", err)
		return err
	}

	return nil
}

// parseCheckpointSignatures parse checkpoint signatures for the L1 checkpoint contract
func (cp *CheckpointProcessor) parseCheckpointSignatures(signatures []checkpointtypes.CheckpointSignature) ([][3]*big.Int, error) {
	type sideTxSig struct {
		address []byte
		sig     []byte
	}

	sideTxSigs := make([]sideTxSig, 0)

	for _, entry := range signatures {
		sideTxSigs = append(sideTxSigs, sideTxSig{
			address: entry.ValidatorAddress,
			sig:     entry.Signature,
		})
	}

	if len(sideTxSigs) == 0 {
		return nil, errors.New("no side tx sigs found")
	}

	sort.Slice(sideTxSigs, func(i, j int) bool {
		return bytes.Compare(sideTxSigs[i].address, sideTxSigs[j].address) < 0
	})

	dummyLegacyTxn := ethTypes.NewTx(&ethTypes.LegacyTx{
		Nonce:    0,
		To:       &common.Address{},
		Value:    nil,
		Gas:      0,
		GasPrice: nil,
		Data:     nil,
	})

	sigs := make([][3]*big.Int, 0, len(sideTxSigs))

	for _, sideTxSig := range sideTxSigs {
		R, S, V, err := ethTypes.HomesteadSigner{}.SignatureValues(dummyLegacyTxn, sideTxSig.sig)
		if err != nil {
			return nil, err
		}

		sigs = append(sigs, [3]*big.Int{R, S, V})
	}

	return sigs, nil
}

// fetchDividendAccountRoot - fetches dividend account root hash
func (cp *CheckpointProcessor) fetchDividendAccountRoot() ([]byte, error) {
	cp.Logger.Info("Sending Rest call to Get Dividend AccountRootHash")

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(util.DividendAccountRootURL))
	if err != nil {
		cp.Logger.Error("Error Fetching AccountRootHash from HeimdallServer ", "error", err)
		return []byte{}, err
	}

	cp.Logger.Info("Dividend account root fetched")

	var accountRootHashObject topuptypes.QueryDividendAccountRootHashResponse
	if err = cp.cliCtx.Codec.UnmarshalJSON(response, &accountRootHashObject); err != nil {
		cp.Logger.Error("Error unmarshalling AccountRootHash received from Heimdall Server", "error", err)
		return accountRootHashObject.AccountRootHash, err
	}

	return accountRootHashObject.AccountRootHash, nil
}

// getLatestCheckpointTime gets the latest checkpoint time from rootChain
func (cp *CheckpointProcessor) getLatestCheckpointTime(checkpointContext *CheckpointContext) (int64, error) {
	// get chain params
	chainParams := checkpointContext.ChainmanagerParams.ChainParams
	checkpointParams := checkpointContext.CheckpointParams

	rootChainInstance, err := cp.contractCaller.GetRootChainInstance(chainParams.RootChainAddress)
	if err != nil {
		return 0, err
	}

	// fetch the last header number
	lastHeaderNumber, err := cp.contractCaller.CurrentHeaderBlock(rootChainInstance, checkpointParams.ChildChainBlockInterval)
	if err != nil {
		cp.Logger.Error("Error while fetching current header block number", "error", err)
		return 0, err
	}

	// header block
	_, _, _, createdAt, _, err := cp.contractCaller.GetHeaderInfo(lastHeaderNumber, rootChainInstance, checkpointParams.ChildChainBlockInterval)
	if err != nil {
		cp.Logger.Error("Error while fetching header block object", "error", err)
		return 0, err
	}

	return int64(createdAt), nil
}

func (cp *CheckpointProcessor) getLastNoAckTime() uint64 {
	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(util.LastNoAckURL))
	if err != nil {
		cp.Logger.Error("Error while sending request for last no-ack", "Error", err)
		return 0
	}

	var noAckObject checkpointtypes.QueryLastNoAckResponse
	if err := cp.cliCtx.Codec.UnmarshalJSON(response, &noAckObject); err != nil {
		cp.Logger.Error("Error unmarshalling no-ack data ", "error", err)
		return 0
	}

	return noAckObject.LastNoAckId
}

func (cp *CheckpointProcessor) getCheckpointSignatures(txHash string) ([]checkpointtypes.CheckpointSignature, error) {
	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(util.CheckpointSignaturesURL, txHash)))
	if err != nil {
		return nil, fmt.Errorf("error while sending request for checkpoint signatures: %w", err)
	}

	var res checkpointtypes.QueryCheckpointSignaturesResponse
	if err := cp.cliCtx.Codec.UnmarshalJSON(response, &res); err != nil {
		return nil, fmt.Errorf("error unmarshalling checkpoint signatures: %w", err)
	}

	return res.Signatures, nil
}

// checkIfNoAckIsRequired - check if NoAck has to be sent or not
func (cp *CheckpointProcessor) checkIfNoAckIsRequired(checkpointContext *CheckpointContext, lastCreatedAt int64) (bool, uint64) {
	var index float64
	// if last created at 0, it means no checkpoint yet
	if lastCreatedAt == 0 {
		index = 1
	}

	// checkpoint params
	checkpointParams := checkpointContext.CheckpointParams

	checkpointCreationTime := time.Unix(lastCreatedAt, 0)
	currentTime := time.Now().UTC()
	timeDiff := currentTime.Sub(checkpointCreationTime)
	// check if the last checkpoint was < NoACK wait time
	if timeDiff.Seconds() >= checkpointParams.CheckpointBufferTime.Seconds() && index == 0 {
		index = math.Floor(timeDiff.Seconds() / checkpointParams.CheckpointBufferTime.Seconds())
	}

	if index == 0 {
		return false, uint64(index)
	}

	// check the difference between no-ack time and current time
	lastNoAck := cp.getLastNoAckTime()

	lastNoAckTime := time.Unix(int64(lastNoAck), 0)
	// if last no ack = 0, the first no-ack to be sent
	if currentTime.Sub(lastNoAckTime).Seconds() < checkpointParams.CheckpointBufferTime.Seconds() && lastNoAck != 0 {
		cp.Logger.Debug("Cannot send multiple no-ack in short time", "timeDiff", currentTime.Sub(lastNoAckTime).Seconds(), "ExpectedDiff", checkpointParams.CheckpointBufferTime.Seconds())
		return false, uint64(index)
	}

	return true, uint64(index)
}

// proposeCheckpointNoAck - sends Checkpoint NoAck to heimdall
func (cp *CheckpointProcessor) proposeCheckpointNoAck() (err error) {
	address, err := helper.GetAddressString()
	if err != nil {
		return fmt.Errorf("error converting address to string: %w", err)
	}

	// send NO ACK
	msg := checkpointtypes.NewMsgCheckpointNoAck(
		address,
	)

	// return broadcast to heimdall
	txRes, err := cp.txBroadcaster.BroadcastToHeimdall(&msg, nil)
	if err != nil {
		cp.Logger.Error("Error while broadcasting checkpoint-no-ack to heimdall", "msg", msg, "error", err)
		return err
	}

	if txRes.Code != abci.CodeTypeOK {
		cp.Logger.Error("Checkpoint No-Ack tx failed on heimdall", "txHash", txRes.TxHash, "code", txRes.Code)
		return fmt.Errorf("checkpoint-no-ack tx failed, tx response code: %d", txRes.Code)
	}

	cp.Logger.Info("No-ack transaction sent successfully")

	return nil
}

// shouldSendCheckpoint checks if checkpoint with given start,end should be sent to rootChain or not.
func (cp *CheckpointProcessor) shouldSendCheckpoint(checkpointContext *CheckpointContext, start uint64, end uint64) (bool, error) {
	chainmanagerParams := checkpointContext.ChainmanagerParams

	rootChainInstance, err := cp.contractCaller.GetRootChainInstance(chainmanagerParams.ChainParams.RootChainAddress)
	if err != nil {
		cp.Logger.Error("Error while creating rootChain instance", "error", err)
		return false, err
	}

	// current child block from contract
	currentChildBlock, err := cp.contractCaller.GetLastChildBlock(rootChainInstance)
	if err != nil {
		cp.Logger.Error("Error fetching current child block", "currentChildBlock", currentChildBlock, "error", err)
		return false, err
	}

	cp.Logger.Debug("Fetched current child block", "currentChildBlock", currentChildBlock)

	shouldSend := false
	// validate if the checkpoint needs to be pushed to rootChain and submit
	cp.Logger.Info("Validating if checkpoint needs to be pushed", "committedLastBlock", currentChildBlock, "startBlock", start)
	// check if we need to send the checkpoint or not
	if ((currentChildBlock + 1) == start) || (currentChildBlock == 0 && start == 0) {
		cp.Logger.Info("Checkpoint Valid", "startBlock", start)

		shouldSend = true
	} else if currentChildBlock > start {
		cp.Logger.Info("Start block does not match, checkpoint already sent", "committedLastBlock", currentChildBlock, "startBlock", start)
	} else if currentChildBlock > end {
		cp.Logger.Info("Checkpoint already sent", "committedLastBlock", currentChildBlock, "startBlock", start)
	} else {
		cp.Logger.Info("No need to send checkpoint")
	}

	return shouldSend, nil
}

// Stop stops all necessary go routines
func (cp *CheckpointProcessor) Stop() {
	// cancel No-Ack polling
	cp.cancelNoACKPolling()
}

func (cp *CheckpointProcessor) getCheckpointContext() (*CheckpointContext, error) {
	chainmanagerParams, err := util.GetChainmanagerParams(cp.cliCtx.Codec)
	if err != nil {
		cp.Logger.Error("Error while fetching chain manager params", "error", err)
		return nil, err
	}

	checkpointParams, err := util.GetCheckpointParams(cp.cliCtx.Codec)
	if err != nil {
		cp.Logger.Error("Error while fetching checkpoint params", "error", err)
		return nil, err
	}

	return &CheckpointContext{
		ChainmanagerParams: chainmanagerParams,
		CheckpointParams:   checkpointParams,
	}, nil
}
