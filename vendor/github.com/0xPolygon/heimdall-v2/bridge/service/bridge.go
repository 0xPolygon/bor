package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"cosmossdk.io/x/tx/signing"
	common "github.com/cometbft/cometbft/libs/service"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	authTypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/0xPolygon/heimdall-v2/app"
	"github.com/0xPolygon/heimdall-v2/bridge/broadcaster"
	"github.com/0xPolygon/heimdall-v2/bridge/listener"
	"github.com/0xPolygon/heimdall-v2/bridge/processor"
	"github.com/0xPolygon/heimdall-v2/bridge/queue"
	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	topupTypes "github.com/0xPolygon/heimdall-v2/x/topup/types"
)

const (
	waitDuration  = 1 * time.Minute
	borChainIDKey = "bor-chain-id"
	logsTypeKey   = "logs-type"
)

var logger = helper.Logger.With("module", "bridge/service/")

// AdjustDBValue sets/normalizes viper-config for bridge runtime based on flags present on root/start cmd
func AdjustDBValue(cmd *cobra.Command) {
	cometBftNode, _ := cmd.Flags().GetString(helper.CometBFTNodeFlag)
	withHeimdallConfigValue, _ := cmd.Flags().GetString(helper.WithHeimdallConfigFlag)
	bridgeDBValue, _ := cmd.Flags().GetString(util.BridgeDBFlag)
	borChainIDValue, _ := cmd.Flags().GetString(borChainIDKey)
	logsTypeValue, _ := cmd.Flags().GetString(logsTypeKey)

	// default bridge storage dir: <home>/bridge/storage
	if bridgeDBValue == "" {
		home := viper.GetString(flags.FlagHome)
		if home == "" {
			home = app.DefaultNodeHome
		}
		bridgeDBValue = filepath.Join(home, "bridge", "storage")
	}

	// set to viper
	viper.Set(helper.CometBFTNodeFlag, cometBftNode)
	viper.Set(helper.WithHeimdallConfigFlag, withHeimdallConfigValue)
	viper.Set(util.BridgeDBFlag, bridgeDBValue)
	if borChainIDValue != "" {
		viper.Set(borChainIDKey, borChainIDValue)
	}
	if logsTypeValue != "" {
		viper.Set(logsTypeKey, logsTypeValue)
	}
}

// StartWithCtx starts the bridge runtime as a side service of heimdalld and shuts down gracefully.
func StartWithCtx(ctx context.Context, clientCtx client.Context) error {
	// setup codec and registry
	cdc, err := makeCodec()
	if err != nil {
		panic(err)
	}
	clientCtx = attachCodecIfMissing(clientCtx, cdc)

	// setup queue and CometBFT RPC
	qc := queue.NewQueueConnector(helper.GetConfig().AmqpURL)
	qc.StartWorker()

	httpClient, err := createAndStartRPC(helper.GetConfig().CometBFTRPCUrl)
	if err != nil {
		logger.Error("Error connecting to server", "err", err)
		return err
	}

	// set chain ID
	chainID, err := resolveChainID(ctx, clientCtx)
	if err != nil {
		logger.Error("Error while determining chain ID", "err", err)
		return err
	}
	clientCtx = clientCtx.WithChainID(chainID)
	clientCtx.BroadcastMode = flags.BroadcastAsync

	// wait until the node is synced
	if err := waitUntilSynced(ctx, clientCtx, waitDuration); err != nil {
		// context cancelled while waiting is not an error for shutdown
		return err
	}

	// wire bridge services
	txBroadcaster := broadcaster.NewTxBroadcaster(cdc, ctx, clientCtx, nil)
	services := []common.Service{
		listener.NewListenerService(cdc, qc, httpClient),
		processor.NewProcessorService(cdc, qc, httpClient, txBroadcaster),
	}

	// run services and handle graceful shutdown
	return runServices(ctx, services, httpClient)
}

// makeCodec creates a new codec with the necessary interface registry and registers all required interfaces.
func makeCodec() (codec.Codec, error) {
	ir, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.HexCodec{},
			ValidatorAddressCodec: address.HexCodec{},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("interface registry: %w", err)
	}

	cryptocodec.RegisterInterfaces(ir)
	authTypes.RegisterInterfaces(ir)
	checkpointTypes.RegisterInterfaces(ir)
	milestoneTypes.RegisterInterfaces(ir)
	clerkTypes.RegisterInterfaces(ir)
	stakeTypes.RegisterInterfaces(ir)
	topupTypes.RegisterInterfaces(ir)

	return codec.NewProtoCodec(ir), nil
}

// attachCodecIfMissing checks if the client context has a codec set, and if not, attaches the provided codec.
func attachCodecIfMissing(clientCtx client.Context, cdc codec.Codec) client.Context {
	if clientCtx.Codec == nil {
		return clientCtx.WithCodec(cdc)
	}
	return clientCtx
}

// createAndStartRPC creates and starts a CometBFT HTTP client for the given RPC URL.
func createAndStartRPC(rpcURL string) (*rpchttp.HTTP, error) {
	httpClient, err := rpchttp.New(rpcURL, "/websocket")
	if err != nil {
		return nil, fmt.Errorf("creating cometbft http client: %w", err)
	}
	if err := httpClient.Start(); err != nil {
		return nil, fmt.Errorf("starting cometbft http client: %w", err)
	}
	return httpClient, nil
}

// resolveChainID retrieves the chain ID from the client context or node status.
func resolveChainID(ctx context.Context, clientCtx client.Context) (string, error) {
	if cid := clientCtx.ChainID; cid != "" {
		logger.Info("ChainID set in clientCtx", "chainId", cid)
		return cid, nil
	}

	logger.Info("ChainID is empty in clientCtx at bridge startup, fetching from node status")

	nodeStatus, err := helper.GetNodeStatus(clientCtx, ctx)
	if err != nil {
		return "", fmt.Errorf("fetching node status: %w", err)
	}
	if nodeStatus.NodeInfo.Network == "" {
		return "", errors.New("network is empty in node status, cannot determine chain ID")
	}

	logger.Info("ChainID fetched from node status", "chainId", nodeStatus.NodeInfo.Network)
	return nodeStatus.NodeInfo.Network, nil
}

// waitUntilSynced checks if the node is synced and waits until it is up to date.
func waitUntilSynced(ctx context.Context, clientCtx client.Context, d time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			if !util.IsCatchingUp(clientCtx, ctx) {
				logger.Info("Node up to date, starting bridge services")
				return nil
			}
			logger.Info("Waiting for heimdall to be synced")
		}
	}
}

// runServices starts all the bridge services and handles graceful shutdown.
func runServices(ctx context.Context, services []common.Service, httpClient *rpchttp.HTTP) error {
	var g errgroup.Group

	// start each service
	for _, svc := range services {
		s := svc
		g.Go(func() error {
			if err := s.Start(); err != nil {
				logger.Error("service.Start failed", "err", err)
				return err
			}
			<-s.Quit()
			return nil
		})
	}

	// shutdown controller
	g.Go(func() error {
		<-ctx.Done()
		logger.Info("Received stop signal - Stopping all heimdall bridge services")

		// stop services
		for _, s := range services {
			if s.IsRunning() {
				if err := s.Stop(); err != nil {
					logger.Error("service.Stop failed", "err", err)
					return err
				}
			}
		}

		// stop comet client
		if err := httpClient.Stop(); err != nil {
			logger.Error("httpClient.Stop failed", "err", err)
			return err
		}

		// close DB
		util.CloseBridgeDBInstance()
		return nil
	})

	if err := g.Wait(); err != nil {
		logger.Error("Bridge stopped", "err", err)
		return err
	}
	return nil
}
