package heimdalld

import (
	"os"

	"cosmossdk.io/log"
	"github.com/cometbft/cometbft/cmd/cometbft/commands"
	db "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/config"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/server"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	txmodule "github.com/cosmos/cosmos-sdk/x/auth/tx/config"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/0xPolygon/heimdall-v2/app"
	"github.com/0xPolygon/heimdall-v2/helper"
)

var logger = helper.Logger.With("module", "cmd/heimdalld")

type EncodingConfig struct {
	InterfaceRegistry codectypes.InterfaceRegistry
	Codec             codec.Codec
	TxConfig          client.TxConfig
	Amino             *codec.LegacyAmino
}

// NewRootCmd creates a new root command for heimdalld. It is called once in the
// main function.
func NewRootCmd() *cobra.Command {
	// we "pre"-instantiate the application for getting the injected/configured encoding configuration
	// note, this is not necessary when using app wiring, as depInject can be directly used (see root_v2.go)

	// Since this is only a temp app, we don't need info logs, only warn and error logs.
	tempLogger := log.NewLogger(os.Stdout, log.LevelOption(zerolog.WarnLevel))
	tempApp := app.NewHeimdallApp(tempLogger, db.NewMemDB(), nil, true, simtestutil.NewAppOptionsWithFlagHome(tempDir()))
	encodingConfig := EncodingConfig{
		InterfaceRegistry: tempApp.InterfaceRegistry(),
		Codec:             tempApp.AppCodec(),
		TxConfig:          tempApp.GetTxConfig(),
		Amino:             tempApp.LegacyAmino(),
	}

	initClientCtx := client.Context{}.
		WithCodec(encodingConfig.Codec).
		WithInterfaceRegistry(encodingConfig.InterfaceRegistry).
		WithTxConfig(encodingConfig.TxConfig).
		WithLegacyAmino(encodingConfig.Amino).
		WithInput(os.Stdin).
		WithAccountRetriever(types.AccountRetriever{}).
		WithHomeDir(app.DefaultNodeHome).
		WithViper("HD") // prefix for env variables

	rootCmd := &cobra.Command{
		Use:           "heimdalld",
		Short:         "Heimdall Daemon (server)",
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// set the default command outputs
			cmd.SetOut(cmd.OutOrStdout())
			cmd.SetErr(cmd.ErrOrStderr())

			initClientCtx = initClientCtx.WithCmdContext(cmd.Context())
			initClientCtx, err := client.ReadPersistentCommandFlags(initClientCtx, cmd.Flags())
			if err != nil {
				return err
			}

			initClientCtx, err = config.ReadFromClientConfig(initClientCtx)
			if err != nil {
				return err
			}

			// This needs to go after ReadFromClientConfig, as that function
			// sets the RPC client needed for SIGN_MODE_TEXTUAL. This sign mode
			// is only available if the client is online.
			if !initClientCtx.Offline {
				enabledSignModes := append(tx.DefaultSignModes, signing.SignMode_SIGN_MODE_TEXTUAL)
				txConfigOpts := tx.ConfigOptions{
					EnabledSignModes:           enabledSignModes,
					TextualCoinMetadataQueryFn: txmodule.NewGRPCCoinMetadataQueryFn(initClientCtx),
				}
				txConfig, err := tx.NewTxConfigWithOptions(
					initClientCtx.Codec,
					txConfigOpts,
				)
				if err != nil {
					return err
				}

				initClientCtx = initClientCtx.WithTxConfig(txConfig)
			}

			if err := client.SetCmdClientContextHandler(initClientCtx, cmd); err != nil {
				return err
			}

			customAppTemplate, customAppConfig := initAppConfig()
			customCMTConfig := initCometBFTConfig()

			if cmd.Name() != commands.InitFilesCmd.Name() &&
				cmd.Name() != commands.VersionCmd.Name() &&
				cmd.Name() != MigrateCommand().Name() &&
				cmd.Name() != testnetCmdName {
				helper.InitHeimdallConfig("")
			}

			serverCtx, err := server.InterceptConfigsAndCreateContext(cmd, customAppTemplate, customAppConfig, customCMTConfig)
			if err != nil {
				return err
			}

			// Overwrite default server logger
			logger, err := server.CreateSDKLogger(serverCtx, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			serverCtx.Logger = logger.With(log.ModuleKey, "server")

			// Get log_level from serverCtx.Viper
			logLevelStr := serverCtx.Viper.GetString(flags.FlagLogLevel)

			// Set log_level value to viper
			viper.Set(flags.FlagLogLevel, logLevelStr)

			// Overwrite default heimdall logger
			logLevel, err := zerolog.ParseLevel(logLevelStr)
			if err != nil {
				return err
			}
			helper.Logger = log.NewLogger(cmd.OutOrStdout(), log.LevelOption(logLevel))

			// Set server context
			return server.SetCmdServerContext(cmd, serverCtx)
		},
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			// set the default command outputs
			cmd.SetOut(cmd.OutOrStdout())
			cmd.SetErr(cmd.ErrOrStderr())

			helper.InitHeimdallConfig("")
		},
	}

	// adding heimdall configuration flags to the root command
	helper.DecorateWithHeimdallFlags(rootCmd, viper.GetViper(), logger, "main")
	helper.DecorateWithCometBFTFlags(rootCmd, viper.GetViper(), logger, "main")

	initRootCmd(rootCmd, encodingConfig.TxConfig, tempApp.BasicManager, tempApp)

	// add keyring to autocli opts
	autoCliOpts := tempApp.AutoCliOpts()
	autoCliOpts.ClientCtx = initClientCtx

	if err := autoCliOpts.EnhanceRootCommand(rootCmd); err != nil {
		panic(err)
	}

	return rootCmd
}
