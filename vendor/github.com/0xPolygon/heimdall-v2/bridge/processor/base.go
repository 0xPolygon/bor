package processor

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"cosmossdk.io/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types"
	authlegacytx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/spf13/viper"
	"github.com/syndtr/goleveldb/leveldb"

	"github.com/0xPolygon/heimdall-v2/bridge/broadcaster"
	"github.com/0xPolygon/heimdall-v2/bridge/queue"
	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	topupTypes "github.com/0xPolygon/heimdall-v2/x/topup/types"
)

// Processor defines a block header listener for RootChain, BorChain, Heimdall
type Processor interface {
	Start() error

	RegisterTasks()

	String() string

	Stop()
}

type BaseProcessor struct {
	Logger log.Logger
	name   string
	quit   chan struct{}

	// queue connector
	queueConnector *queue.Connector

	// tx broadcaster
	txBroadcaster *broadcaster.TxBroadcaster

	// The "subclass" of BaseProcessor
	impl Processor

	// cli context
	cliCtx client.Context

	// contract caller
	contractCaller helper.ContractCaller

	// http client to subscribe to
	httpClient *rpchttp.HTTP

	// storage client
	storageClient *leveldb.DB
}

// Logger returns logger singleton instance
func Logger(name string) log.Logger {
	return util.Logger().With("service", "processor", "module", name)
}

// NewBaseProcessor creates a new BaseProcessor.
func NewBaseProcessor(cdc codec.Codec, queueConnector *queue.Connector, httpClient *rpchttp.HTTP, txBroadcaster *broadcaster.TxBroadcaster, name string, impl Processor) *BaseProcessor {
	logger := Logger(name)

	cliCtx := client.Context{}.WithCodec(cdc)
	cliCtx.BroadcastMode = flags.BroadcastSync
	cmt := helper.GetConfig().CometBFTRPCUrl
	rpc, err := client.NewClientFromNode(cmt)
	if err != nil {
		logger.Error("Error while creating rpc client", "error", err)
		panic(err)
	}
	cliCtx = cliCtx.WithClient(rpc)

	contractCaller, err := helper.NewContractCaller()
	if err != nil {
		logger.Error("Error while getting root chain instance", "error", err)
		panic(err)
	}

	if logger == nil {
		logger = log.NewNopLogger()
	}

	// creating the syncer object
	return &BaseProcessor{
		Logger: logger,
		name:   name,
		quit:   make(chan struct{}),
		impl:   impl,

		cliCtx:         cliCtx,
		queueConnector: queueConnector,
		contractCaller: contractCaller,
		txBroadcaster:  txBroadcaster,
		httpClient:     httpClient,
		storageClient:  util.GetBridgeDBInstance(viper.GetString(util.BridgeDBFlag)),
	}
}

// String implements Service by returning a string representation of the service.
func (bp *BaseProcessor) String() string {
	return bp.name
}

// Stop stops all necessary go routines
func (bp *BaseProcessor) Stop() {
	// override to stop any go-routines in individual processors
}

// isOldTx checks if the transaction already exists in the chain or not
// It is a generic function, which is consumed in all processors
func (bp *BaseProcessor) isOldTx(_ client.Context, txHash string, logIndex uint64, eventType util.BridgeEvent, event interface{}) (bool, error) {
	defer util.LogElapsedTimeForStateSyncedEvent(event, "isOldTx", time.Now())

	queryParam := map[string]interface{}{
		"tx_hash":   txHash,
		"log_index": logIndex,
	}

	// define the endpoint based on the type of event
	var endpoint string

	switch eventType {
	case util.StakingEvent:
		endpoint = helper.GetHeimdallServerEndpoint(util.StakingTxStatusURL)
	case util.TopupEvent:
		endpoint = helper.GetHeimdallServerEndpoint(util.TopupTxStatusURL)
	case util.ClerkEvent:
		endpoint = helper.GetHeimdallServerEndpoint(util.ClerkTxStatusURL)
	default:
		bp.Logger.Error("Invalid event type", "event", eventType)
	}

	url, err := util.CreateURLWithQuery(endpoint, queryParam)
	if err != nil {
		bp.Logger.Error("Error in creating url", "endpoint", endpoint, "error", err)
		return false, err
	}

	res, err := helper.FetchFromAPI(url)
	if err != nil {
		bp.Logger.Error("Error fetching tx status", "url", url, "error", err)
		return false, err
	}

	switch eventType {
	case util.StakingEvent:
		var response staketypes.QueryStakeIsOldTxResponse
		if err := bp.cliCtx.Codec.UnmarshalJSON(res, &response); err != nil {
			bp.Logger.Error("Error unmarshalling tx status received from Heimdall Server", "error", err)
			return false, err
		}
		return response.IsOld, nil
	case util.TopupEvent:
		var response topupTypes.QueryIsTopupTxOldResponse
		if err := bp.cliCtx.Codec.UnmarshalJSON(res, &response); err != nil {
			bp.Logger.Error("Error unmarshalling tx status received from Heimdall Server", "error", err)
			return false, err
		}
		return response.IsOld, nil
	case util.ClerkEvent:
		var response clerkTypes.IsClerkTxOldResponse
		if err := bp.cliCtx.Codec.UnmarshalJSON(res, &response); err != nil {
			bp.Logger.Error("Error unmarshalling tx status received from Heimdall Server", "error", err)
			return false, err
		}
		return response.IsOld, nil
	default:
		bp.Logger.Error("Invalid event type", "event", eventType)
	}

	return false, nil
}

// checkTxAgainstMempool checks if the transaction is already in the mempool or not
// It is consumed only for `clerk` processor
func (bp *BaseProcessor) checkTxAgainstMempool(msg types.Msg, event interface{}) (bool, error) {
	defer util.LogElapsedTimeForStateSyncedEvent(event, "checkTxAgainstMempool", time.Now())

	endpoint := helper.GetConfig().CometBFTRPCUrl + util.CometBFTUnconfirmedTxsURL

	resp, err := helper.Client.Get(endpoint)
	if err != nil || resp.StatusCode != http.StatusOK {
		bp.Logger.Error("Error fetching mempool tx", "url", endpoint, "error", err)
		return false, err
	}

	// Limit the number of bytes read from the response body
	limitedBody := http.MaxBytesReader(nil, resp.Body, helper.APIBodyLimit)

	body, err := io.ReadAll(limitedBody)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			bp.Logger.Error("Error closing response body:", err)
		}
	}()
	if err != nil {
		bp.Logger.Error("Error reading response body for mempool tx", "error", err)
		return false, err
	}

	// a minimal response of the unconfirmed txs
	var response util.CometBFTUnconfirmedTxs

	err = json.Unmarshal(body, &response)
	if err != nil {
		bp.Logger.Error("Error unmarshalling response received from Heimdall Server", "error", err)
		return false, err
	}

	// Iterate over txs present in the mempool.
	// We can verify if the message we're about to send is present by
	// checking the type of transaction, the transaction hash and log index
	// present in the data of the transaction

	status := false
Loop:
	for _, txn := range response.Result.Txs {
		// CometBFT encodes the transactions with base64 encoding. Decode it first.
		txBytes, err := base64.StdEncoding.DecodeString(txn)
		if err != nil {
			bp.Logger.Error("Error decoding tx (base64 decoder) while checking against mempool", "error", err)
			continue
		}

		// Unmarshal the transaction from bytes
		decodedTx, err := authlegacytx.DefaultTxDecoder(bp.cliCtx.Codec)(txBytes)
		if err != nil {
			bp.Logger.Error("Error decoding tx (tx decoder) while checking against mempool", "error", err)
			continue
		}
		txMsg := decodedTx.GetMsgs()[0]

		// We only need to check for `event-record` type transactions.
		switch txMsg.String() {
		case "event-record":

			// typecast the txs for the clerk type message
			mempoolTxMsg, ok := txMsg.(*clerkTypes.MsgEventRecord)
			if !ok {
				bp.Logger.Error("Unable to typecast message to clerk event record while checking against mempool")
				continue Loop
			}

			// typecast the msg for the clerk type message
			clerkMsg, ok := msg.(*clerkTypes.MsgEventRecord)
			if !ok {
				bp.Logger.Error("Unable to typecast message to clerk event record while checking against mempool")
				continue Loop
			}

			// check the transaction hash in the message
			if clerkMsg.GetTxHash() != mempoolTxMsg.GetTxHash() {
				continue Loop
			}

			// check the log index in the message
			if clerkMsg.GetLogIndex() != mempoolTxMsg.GetLogIndex() {
				continue Loop
			}

			// If we reach here, there's already a same transaction in the mempool
			status = true
			break Loop
		default:
			// ignore
		}
	}

	return status, nil
}
