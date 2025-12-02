package listener

import (
	"context"
	"math/big"
	"strconv"
	"time"

	"github.com/RichardKnop/machinery/v1/tasks"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	chainmanagerTypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
)

// RootChainListenerContext root chain listener context
type RootChainListenerContext struct {
	ChainmanagerParams *chainmanagerTypes.Params
}

// RootChainListener - Listens to and process events from RootChain
type RootChainListener struct {
	BaseListener
	// ABIs
	abis []*abi.ABI

	stakingInfoAbi *abi.ABI
	stateSenderAbi *abi.ABI

	// For self-healing, it will be only initialized if sub_graph_url is provided
	subGraphClient *subGraphClient
}

const (
	lastRootBlockKey = "rootchain-last-block" // storage key
)

// NewRootChainListener - constructor func
func NewRootChainListener() *RootChainListener {
	contractCaller, err := helper.NewContractCaller()
	if err != nil {
		panic(err)
	}

	abis := []*abi.ABI{
		&contractCaller.RootChainABI,
		&contractCaller.StateSenderABI,
		&contractCaller.StakingInfoABI,
	}

	return &RootChainListener{
		abis:           abis,
		stakingInfoAbi: &contractCaller.StakingInfoABI,
		stateSenderAbi: &contractCaller.StateSenderABI,
	}
}

// Start starts new block subscription
func (rl *RootChainListener) Start() error {
	rl.Logger.Info("Starting")

	// create cancellable context
	ctx, cancelSubscription := context.WithCancel(context.Background())
	rl.cancelSubscription = cancelSubscription

	// create cancellable context
	headerCtx, cancelHeaderProcess := context.WithCancel(context.Background())
	rl.cancelHeaderProcess = cancelHeaderProcess

	// start the header process
	go rl.StartHeaderProcess(headerCtx)

	// start go routine to poll for the new header using the client object
	rl.Logger.Info("Start polling for rootChain header blocks", "pollInterval", helper.GetConfig().SyncerPollInterval)

	// start polling for the finalized block in the main L1 chain (available post-merge)
	go rl.StartPolling(ctx, helper.GetConfig().SyncerPollInterval, big.NewInt(int64(rpc.FinalizedBlockNumber)))

	// Start the self-healing process
	go rl.startSelfHealing(ctx)

	return nil
}

// ProcessHeader - process header block from rootChain
func (rl *RootChainListener) ProcessHeader(newHeader *blockHeader) {
	rl.Logger.Debug("New block detected", "blockNumber", newHeader.header.Number)

	// fetch context
	rootChainContext, err := rl.getRootChainContext()
	if err != nil {
		return
	}

	requiredConfirmations := rootChainContext.ChainmanagerParams.MainChainTxConfirmations
	headerNumber := newHeader.header.Number
	from := headerNumber

	// If the incoming header is a `finalized` header, it can directly be considered as
	// the upper cap (i.e., the `to` value)
	//
	// If the incoming header is a `latest` header, rely on `requiredConfirmations` to get
	// finalized block range.
	if !newHeader.isFinalized {
		// This check is only useful when the L1 blocks received are < requiredConfirmations
		// just for the below headerNumber -= requiredConfirmations math operation
		confirmationBlocks := big.NewInt(0).SetUint64(requiredConfirmations)
		if headerNumber.Cmp(confirmationBlocks) <= 0 {
			rl.Logger.Error("Block number less than Confirmations required", "blockNumber", headerNumber.Uint64, "confirmationsRequired", confirmationBlocks.Uint64)
			return
		}

		// subtract the `confirmationBlocks` to only consider blocks before that
		headerNumber = headerNumber.Sub(headerNumber, confirmationBlocks)

		// update the `from` value
		from = headerNumber
	}

	// get the last block from storage
	hasLastBlock, _ := rl.storageClient.Has([]byte(lastRootBlockKey), nil)
	if hasLastBlock {
		lastBlockBytes, err := rl.storageClient.Get([]byte(lastRootBlockKey), nil)
		if err != nil {
			rl.Logger.Info("Error while fetching last block bytes from storage", "error", err)
			return
		}

		rl.Logger.Debug("Got last block from bridge storage", "lastBlock", string(lastBlockBytes))

		if result, err := strconv.ParseUint(string(lastBlockBytes), 10, 64); err == nil {
			if result >= headerNumber.Uint64() {
				return
			}

			from = big.NewInt(0).SetUint64(result + 1)
		}
	}

	to := headerNumber

	// Prepare block range
	if to.Cmp(from) == -1 {
		from = to
	}

	// Set the last block to storage
	if err = rl.storageClient.Put([]byte(lastRootBlockKey), []byte(to.String()), nil); err != nil {
		rl.Logger.Error("rl.storageClient.Put", "Error", err)
	}

	// Handle events
	rl.queryAndBroadcastEvents(rootChainContext, from, to)
}

// queryAndBroadcastEvents fetches supported events from the rootChain and handles all of them
func (rl *RootChainListener) queryAndBroadcastEvents(rootChainContext *RootChainListenerContext, fromBlock *big.Int, toBlock *big.Int) {
	rl.Logger.Info("Query rootChain event logs", "fromBlock", fromBlock, "toBlock", toBlock)

	ctx, cancel := context.WithTimeout(context.Background(), rl.contractCaller.MainChainTimeout)
	defer cancel()

	// get chain params
	chainParams := rootChainContext.ChainmanagerParams.ChainParams

	// Fetch events from the rootChain
	logs, err := rl.contractCaller.MainChainClient.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: fromBlock,
		ToBlock:   toBlock,
		Addresses: []ethCommon.Address{
			ethCommon.HexToAddress(chainParams.RootChainAddress),
			ethCommon.HexToAddress(chainParams.StakingInfoAddress),
			ethCommon.HexToAddress(chainParams.StateSenderAddress),
		},
	})
	if err != nil {
		rl.Logger.Error("Error while filtering logs", "error", err)
		return
	} else if len(logs) > 0 {
		rl.Logger.Debug("New logs found", "numberOfLogs", len(logs))
	}

	// Process filtered log
	for _, vLog := range logs {
		topic := vLog.Topics[0].Bytes()
		for _, abiObject := range rl.abis {
			selectedEvent := helper.EventByID(abiObject, topic)
			if selectedEvent == nil {
				continue
			}

			rl.handleLog(vLog, selectedEvent)
		}
	}
}

func (rl *RootChainListener) SendTaskWithDelay(taskName string, eventName string, logBytes []byte, delay time.Duration, event interface{}) {
	defer util.LogElapsedTimeForStateSyncedEvent(event, "SendTaskWithDelay", time.Now())

	signature := &tasks.Signature{
		Name: taskName,
		Args: []tasks.Arg{
			{
				Type:  "string",
				Value: eventName,
			},
			{
				Type:  "string",
				Value: string(logBytes),
			},
		},
	}
	signature.RetryCount = 3

	// add delay for the task so that multiple validators won't send same transaction at same time
	eta := time.Now().Add(delay)
	signature.ETA = &eta
	rl.Logger.Info("Sending task", "taskName", taskName, "currentTime", time.Now(), "delayTime", eta)

	_, err := rl.queueConnector.Server.SendTask(signature)
	if err != nil {
		rl.Logger.Error("Error sending task", "taskName", taskName, "error", err)
	}
}

// getRootChainContext returns the root chain context
func (rl *RootChainListener) getRootChainContext() (*RootChainListenerContext, error) {
	chainmanagerParams, err := util.GetChainmanagerParams(rl.cliCtx.Codec)
	if err != nil {
		rl.Logger.Error("Error while fetching chain manager params", "error", err)
		return nil, err
	}

	return &RootChainListenerContext{
		ChainmanagerParams: chainmanagerParams,
	}, nil
}
