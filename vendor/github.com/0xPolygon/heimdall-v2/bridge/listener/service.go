package listener

import (
	"os"

	logger "github.com/cometbft/cometbft/libs/log"
	common "github.com/cometbft/cometbft/libs/service"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/0xPolygon/heimdall-v2/bridge/queue"
	"github.com/0xPolygon/heimdall-v2/helper"
)

const (
	listenerServiceStr = "listener"

	rootChainListenerStr = "rootchain"
	heimdallListenerStr  = "heimdall"
	borChainListenerStr  = "borchain"
)

// Service starts and stops all chain event listeners
type Service struct {
	// Base service
	common.BaseService
	listeners []Listener
}

// NewListenerService returns the new service object for listening to events
func NewListenerService(cdc codec.Codec, queueConnector *queue.Connector, httpClient *rpchttp.HTTP) *Service {
	// creating the listener object
	listenerService := &Service{}

	listenerService.BaseService = *common.NewBaseService(logger.NewTMLogger(logger.NewSyncWriter(os.Stdout)).With("service", "listener"), listenerServiceStr, listenerService)

	rootChainListener := NewRootChainListener()
	rootChainListener.BaseListener = *NewBaseListener(cdc, queueConnector, httpClient, helper.GetMainClient(), rootChainListenerStr, rootChainListener)
	listenerService.listeners = append(listenerService.listeners, rootChainListener)

	borChainListener := &BorChainListener{}
	borChainListener.BaseListener = *NewBaseListener(cdc, queueConnector, httpClient, helper.GetBorClient(), borChainListenerStr, borChainListener)
	listenerService.listeners = append(listenerService.listeners, borChainListener)

	heimdallListener := &HeimdallListener{}
	heimdallListener.BaseListener = *NewBaseListener(cdc, queueConnector, httpClient, nil, heimdallListenerStr, heimdallListener)
	listenerService.listeners = append(listenerService.listeners, heimdallListener)

	return listenerService
}

// OnStart starts the new block subscription
func (listenerService *Service) OnStart() error {
	if err := listenerService.BaseService.OnStart(); err != nil {
		listenerService.Logger.Error("OnStart | OnStart", "Error", err)
	} // Always call the overridden method.

	// start chain listeners
	for _, listener := range listenerService.listeners {
		if err := listener.Start(); err != nil {
			listenerService.Logger.Error("OnStart | Start", "Error", err)
		}
	}

	listenerService.Logger.Info("all listeners Started")

	return nil
}

// OnStop stops all necessary go routines
func (listenerService *Service) OnStop() {
	listenerService.BaseService.OnStop() // Always call the overridden method.

	// start chain listeners
	for _, listener := range listenerService.listeners {
		listener.Stop()
	}

	listenerService.Logger.Info("all listeners stopped")
}
