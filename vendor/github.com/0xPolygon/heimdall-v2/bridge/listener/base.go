package listener

import (
	"context"
	"math/big"
	"time"

	"cosmossdk.io/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/spf13/viper"
	"github.com/syndtr/goleveldb/leveldb"

	"github.com/0xPolygon/heimdall-v2/bridge/queue"
	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
)

// Listener defines a block header listener for RootChain, BorChain, Heimdall
type Listener interface {
	Start() error

	StartHeaderProcess(context.Context)

	StartPolling(context.Context, time.Duration, *big.Int)

	StartSubscription(context.Context, ethereum.Subscription)

	ProcessHeader(*blockHeader)

	Stop()

	String() string
}

type BaseListener struct {
	Logger log.Logger
	name   string
	quit   chan struct{}

	// The "subclass" of BaseService
	impl Listener

	// contract caller
	contractCaller helper.ContractCaller

	chainClient *ethclient.Client

	// header channel
	HeaderChannel chan *blockHeader

	// cancel function for poll/subscription
	cancelSubscription context.CancelFunc

	// header listener subscription
	cancelHeaderProcess context.CancelFunc

	// cli context
	cliCtx client.Context

	// queue connector
	queueConnector *queue.Connector

	// http client to subscribe to
	httpClient *rpchttp.HTTP

	// storage client
	storageClient *leveldb.DB
}

type blockHeader struct {
	header      *types.Header // block header
	isFinalized bool          // if the block is a finalized block or not
}

// NewBaseListener creates a new BaseListener.
func NewBaseListener(cdc codec.Codec, queueConnector *queue.Connector, httpClient *rpchttp.HTTP, chainClient *ethclient.Client, name string, impl Listener) *BaseListener {
	logger := util.Logger().With("service", "listener", "module", name)

	contractCaller, err := helper.NewContractCaller()
	if err != nil {
		logger.Error("Error while getting the contract caller", "error", err)
		panic(err)
	}

	cliCtx := client.Context{}.WithCodec(cdc)
	cliCtx.BroadcastMode = flags.BroadcastAsync
	cmt := helper.GetConfig().CometBFTRPCUrl
	rpc, err := client.NewClientFromNode(cmt)
	if err != nil {
		logger.Error("Error while creating rpc client", "error", err)
		panic(err)
	}
	cliCtx = cliCtx.WithClient(rpc)

	// creating the syncer object
	return &BaseListener{
		Logger:        logger,
		name:          name,
		quit:          make(chan struct{}),
		impl:          impl,
		storageClient: util.GetBridgeDBInstance(viper.GetString(util.BridgeDBFlag)),

		cliCtx:         cliCtx,
		queueConnector: queueConnector,
		httpClient:     httpClient,
		contractCaller: contractCaller,
		chainClient:    chainClient,

		HeaderChannel: make(chan *blockHeader),
	}
}

// String implements Service by returning a string representation of the service.
func (bl *BaseListener) String() string {
	return bl.name
}

// StartHeaderProcess starts the header process when they get a new header
func (bl *BaseListener) StartHeaderProcess(ctx context.Context) {
	bl.Logger.Info("Starting header process")

	for {
		select {
		case newHeader := <-bl.HeaderChannel:
			bl.impl.ProcessHeader(newHeader)
		case <-ctx.Done():
			bl.Logger.Info("Header process stopped")
			return
		}
	}
}

// StartPolling starts polling
func (bl *BaseListener) StartPolling(ctx context.Context, pollInterval time.Duration, number *big.Int) {
	// How often to fire the passed in function in second
	interval := pollInterval

	// Set up the ticket and the channel to signal
	// the ending of the interval
	ticker := time.NewTicker(interval)

	// start listening
	for {
		select {
		case <-ticker.C:
			var bHeader *blockHeader

			header, err := bl.chainClient.HeaderByNumber(ctx, number)
			if err == nil && header != nil {
				if number != nil {
					// finalized was requested
					bHeader = &blockHeader{header: header, isFinalized: true}
				} else {
					// the latest was requested
					bHeader = &blockHeader{header: header, isFinalized: false}
				}
			}

			// if an error occurred and finalized was requested, fall back to the latest block
			if err != nil && number != nil {
				header, err = bl.chainClient.HeaderByNumber(ctx, nil)
				if err == nil && header != nil {
					bHeader = &blockHeader{header: header, isFinalized: false}
				}
			}

			if err != nil {
				bl.Logger.Error("Error in fetching block header while polling", "err", err)
			}

			// push data to the channel
			if bHeader != nil {
				bl.HeaderChannel <- bHeader
			}
		case <-ctx.Done():
			bl.Logger.Info("Polling stopped")
			ticker.Stop()

			return
		}
	}
}

func (bl *BaseListener) StartSubscription(ctx context.Context, subscription ethereum.Subscription) {
	for {
		select {
		case err := <-subscription.Err():
			// stop service
			bl.Logger.Error("Error while subscribing new blocks", "error", err)
			// bl.Stop()

			// cancel subscription
			if bl.cancelSubscription != nil {
				bl.Logger.Debug("Cancelling the subscription of listener")
				bl.cancelSubscription()
			}

			return
		case <-ctx.Done():
			bl.Logger.Info("Subscription stopped")
			return
		}
	}
}

// Stop stops all necessary go routines
func (bl *BaseListener) Stop() {
	// cancel subscription if any
	if bl.cancelSubscription != nil {
		bl.cancelSubscription()
	}

	// cancel the header process
	if bl.cancelHeaderProcess != nil {
		bl.cancelHeaderProcess()
	}
}
