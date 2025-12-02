package listener

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/RichardKnop/machinery/v1/tasks"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/helper"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
)

const heimdallLastBlockKey = "heimdall-last-block" // storage key

// HeimdallListener - Listens to and process events from heimdall
type HeimdallListener struct {
	BaseListener
}

// Start starts new block subscription
func (hl *HeimdallListener) Start() error {
	hl.Logger.Info("Starting heimdall listener")

	// create cancellable context
	headerCtx, cancelHeaderProcess := context.WithCancel(context.Background())
	hl.cancelHeaderProcess = cancelHeaderProcess

	// Heimdall pollInterval = (minimal pollInterval of root chain and bor chain)
	pollInterval := helper.GetConfig().SyncerPollInterval
	if helper.GetConfig().CheckpointPollInterval < helper.GetConfig().SyncerPollInterval {
		pollInterval = helper.GetConfig().CheckpointPollInterval
	}

	hl.Logger.Info("Start polling for events", "pollInterval", pollInterval)
	hl.StartPolling(headerCtx, pollInterval, nil)

	return nil
}

// Stop stops the heimdall listener
func (hl *HeimdallListener) Stop() {
	hl.Logger.Info("Stopping heimdall listener")

	// cancel subscription if any, and clean up reference
	if hl.cancelSubscription != nil {
		hl.cancelSubscription()
		hl.cancelSubscription = nil
	}

	// cancel header processing if any, and clean up reference
	if hl.cancelHeaderProcess != nil {
		hl.cancelHeaderProcess()
		hl.cancelHeaderProcess = nil
	}

	hl.Logger.Info("Heimdall listener stopped")
}

// ProcessHeader -
func (hl *HeimdallListener) ProcessHeader(_ *blockHeader) {
}

// StartPolling - starts polling for heimdall events
func (hl *HeimdallListener) StartPolling(ctx context.Context, pollInterval time.Duration, _ *big.Int) {
	// How often to fire the passed in function in second
	interval := pollInterval

	// Set up the ticket and the channel to signal
	// the ending of the interval
	ticker := time.NewTicker(interval)

	// start listening
	for {
		select {
		case <-ticker.C:
			fromBlock, toBlock, err := hl.fetchFromAndToBlock(ctx)
			if err != nil {
				hl.Logger.Error("Error fetching from and toBlock, skipping events query", "fromBlock", fromBlock, "toBlock", toBlock, "error", err)
				continue
			} else if fromBlock < toBlock {
				hl.Logger.Info("Fetching new events between", "fromBlock", fromBlock, "toBlock", toBlock)

				// Querying and processing Begin events
				for i := fromBlock; i <= toBlock; i++ {
					// Early context check to ensure graceful shutdown
					select {
					case <-ctx.Done():
						hl.Logger.Info("Polling stopped during event fetch loop")
						ticker.Stop()
						return
					default:
					}
					events, err := helper.GetBeginBlockEvents(ctx, hl.httpClient, int64(i))
					if err != nil {
						hl.Logger.Error("Error fetching begin block events", "error", err)
						continue
					}
					for _, event := range events {
						hl.ProcessBlockEvent(sdk.StringifyEvent(event), int64(i))
					}
				}
				// store last block processed
				if err := hl.storageClient.Put([]byte(heimdallLastBlockKey), []byte(strconv.FormatUint(toBlock, 10)), nil); err != nil {
					hl.Logger.Error("hl.storageClient.Put", "Error", err)
				}
			}

		case <-ctx.Done():
			hl.Logger.Info("Polling stopped")
			ticker.Stop()
			return
		}
	}
}

func (hl *HeimdallListener) fetchFromAndToBlock(ctx context.Context) (uint64, uint64, error) {
	nodeStatus, err := helper.GetNodeStatus(hl.cliCtx, ctx)
	if err != nil {
		hl.Logger.Error("Error while fetching heimdall node status", "error", err)
		return 0, 0, err
	}

	chainId := hl.cliCtx.ChainID
	if chainId == "" {
		// If chainId is not set in cliCtx, fetch it from node status.
		// It should not happen because chainId is set in cliCtx during bridge initialization.
		hl.Logger.Debug("ChainID is empty in cliCtx")
		if nodeStatus.NodeInfo.Network == "" {
			return 0, 0, errors.New("network is empty in node status; cannot determine initial fromBlock")
		}
		chainId = nodeStatus.NodeInfo.Network
	}

	// fromBlock - get the initial block height from config
	fromBlock := helper.GetInitialBlockHeight(chainId)
	// fromBlock - get last block from storage
	hasLastBlock, _ := hl.storageClient.Has([]byte(heimdallLastBlockKey), nil)
	if hasLastBlock {
		lastBlockBytes, err := hl.storageClient.Get([]byte(heimdallLastBlockKey), nil)
		if err != nil {
			hl.Logger.Info("Error while fetching last block bytes from storage", "error", err)
			return 0, 0, err
		}

		result, err := strconv.ParseUint(string(lastBlockBytes), 10, 64)
		if err != nil {
			hl.Logger.Info("Error parsing last block bytes from storage", "error", err)
			return 0, 0, err
		}

		hl.Logger.Debug("Got last block from bridge storage", "lastBlock", result)
		if result >= fromBlock {
			hl.Logger.Debug("Overriding fromBlock using last processed block from storage", "oldFromBlock", fromBlock, "lastProcessedBlock", result, "newFromBlock", result+1)
			fromBlock = result + 1
		}
	}

	// toBlock - get the latest block height from heimdall node
	toBlock := uint64(nodeStatus.SyncInfo.LatestBlockHeight)
	if toBlock <= fromBlock {
		toBlock = fromBlock + 1
	}

	return fromBlock, toBlock, err
}

// ProcessBlockEvent - process the block events (BeginBlock, EndBlock events) from heimdall.
func (hl *HeimdallListener) ProcessBlockEvent(event sdk.StringEvent, blockHeight int64) {
	hl.Logger.Info("Received block event from Heimdall", "eventType", event.Type)

	eventBytes, err := json.Marshal(event)
	if err != nil {
		hl.Logger.Error("Error while parsing block event", "eventType", event.Type, "error", err)
		return
	}

	switch event.Type {
	case checkpointTypes.EventTypeCheckpoint:
		hl.sendBlockTask("sendCheckpointToRootchain", eventBytes, blockHeight)
	default:
		hl.Logger.Debug("BlockEvent Type mismatch", "eventType", event.Type)
	}
}

func (hl *HeimdallListener) sendBlockTask(taskName string, eventBytes []byte, blockHeight int64) {
	// create the machinery task
	signature := &tasks.Signature{
		Name: taskName,
		Args: []tasks.Arg{
			{
				Type:  "string",
				Value: string(eventBytes),
			},
			{
				Type:  "int64",
				Value: blockHeight,
			},
		},
	}
	signature.RetryCount = 3

	hl.Logger.Info("Sending block level task", "taskName", taskName, "currentTime", time.Now(), "blockHeight", blockHeight)

	// send the task
	_, err := hl.queueConnector.Server.SendTask(signature)
	if err != nil {
		hl.Logger.Error("Error sending block level task", "taskName", taskName, "blockHeight", blockHeight, "error", err)
	}
}
