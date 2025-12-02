package processor

import (
	common "github.com/cometbft/cometbft/libs/service"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/spf13/viper"

	"github.com/0xPolygon/heimdall-v2/bridge/broadcaster"
	"github.com/0xPolygon/heimdall-v2/bridge/queue"
	"github.com/0xPolygon/heimdall-v2/helper"
)

const (
	processorServiceStr = "processor-service"
)

// Service starts and stops all event processors
type Service struct {
	// Base service
	common.BaseService

	// queue connector
	queueConnector *queue.Connector

	processors []Processor
}

// NewProcessorService returns a new service object for processing queue msg
func NewProcessorService(
	cdc codec.Codec,
	queueConnector *queue.Connector,
	httpClient *rpchttp.HTTP,
	txBroadcaster *broadcaster.TxBroadcaster,
) *Service {
	// creating the processor object
	processorService := &Service{
		queueConnector: queueConnector,
	}

	contractCaller, err := helper.NewContractCaller()
	if err != nil {
		panic(err)
	}

	processorService.BaseService = *common.NewBaseService(nil, processorServiceStr, processorService)

	//
	// Initialize processors
	//

	// initialize checkpoint processor
	checkpointProcessor := NewCheckpointProcessor(&contractCaller.RootChainABI)
	checkpointProcessor.BaseProcessor = *NewBaseProcessor(cdc, queueConnector, httpClient, txBroadcaster, "checkpoint", checkpointProcessor)
	checkpointProcessor.cliCtx = txBroadcaster.CliCtx

	// initialize fee processor
	feeProcessor := NewFeeProcessor(&contractCaller.StakingInfoABI)
	feeProcessor.BaseProcessor = *NewBaseProcessor(cdc, queueConnector, httpClient, txBroadcaster, "fee", feeProcessor)
	feeProcessor.cliCtx = txBroadcaster.CliCtx

	// initialize staking processor
	stakingProcessor := NewStakingProcessor(&contractCaller.StakingInfoABI)
	stakingProcessor.BaseProcessor = *NewBaseProcessor(cdc, queueConnector, httpClient, txBroadcaster, "staking", stakingProcessor)
	stakingProcessor.cliCtx = txBroadcaster.CliCtx

	// initialize clerk processor
	clerkProcessor := NewClerkProcessor(&contractCaller.StateSenderABI)
	clerkProcessor.BaseProcessor = *NewBaseProcessor(cdc, queueConnector, httpClient, txBroadcaster, "clerk", clerkProcessor)
	clerkProcessor.cliCtx = txBroadcaster.CliCtx

	// initialize span processor
	spanProcessor := &SpanProcessor{}
	spanProcessor.BaseProcessor = *NewBaseProcessor(cdc, queueConnector, httpClient, txBroadcaster, "span", spanProcessor)
	spanProcessor.cliCtx = txBroadcaster.CliCtx

	//
	// Select processors
	//

	// add into the processor list
	startAll := viper.GetBool(helper.AllProcessesFlag)
	onlyServices := viper.GetStringSlice(helper.OnlyProcessesFlag)

	if startAll {
		processorService.processors = append(processorService.processors,
			checkpointProcessor,
			stakingProcessor,
			clerkProcessor,
			feeProcessor,
			spanProcessor,
		)
	} else {
		for _, service := range onlyServices {
			switch service {
			case "checkpoint":
				processorService.processors = append(processorService.processors, checkpointProcessor)
			case "staking":
				processorService.processors = append(processorService.processors, stakingProcessor)
			case "clerk":
				processorService.processors = append(processorService.processors, clerkProcessor)
			case "fee":
				processorService.processors = append(processorService.processors, feeProcessor)
			case "span":
				processorService.processors = append(processorService.processors, spanProcessor)
			}
		}
	}

	if len(processorService.processors) == 0 {
		panic("No processors selected. Use --all or --only <comma-separated processors>")
	}

	return processorService
}

// OnStart starts the new block subscription
func (processorService *Service) OnStart() error {
	if err := processorService.BaseService.OnStart(); err != nil {
		processorService.Logger.Error("OnStart | OnStart", "Error", err)
	} // Always call the overridden method.

	// start processors
	for _, processor := range processorService.processors {
		processor.RegisterTasks()

		go func(processor Processor) {
			if err := processor.Start(); err != nil {
				processorService.Logger.Error("OnStart | processor.Start", "Error", err)
			}
		}(processor)
	}

	return nil
}

// OnStop stops all necessary go routines
func (processorService *Service) OnStop() {
	processorService.BaseService.OnStop() // always call the overridden method.
	// start chain listeners
	for _, processor := range processorService.processors {
		processor.Stop()
	}

	processorService.Logger.Info("all processors stopped")
}
