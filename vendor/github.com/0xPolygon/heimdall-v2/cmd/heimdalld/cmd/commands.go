package heimdalld

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cosmossdk.io/log"
	confixcmd "cosmossdk.io/tools/confix/cmd"
	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	cmtos "github.com/cometbft/cometbft/libs/os"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/debug"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/cosmos/cosmos-sdk/client/pruning"
	"github.com/cosmos/cosmos-sdk/client/rpc"
	"github.com/cosmos/cosmos-sdk/client/snapshot"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	"github.com/cosmos/cosmos-sdk/server/types"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authcmd "github.com/cosmos/cosmos-sdk/x/auth/client/cli"
	genutilcli "github.com/cosmos/cosmos-sdk/x/genutil/client/cli"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/console/prompt"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/0xPolygon/heimdall-v2/app"
	bridge "github.com/0xPolygon/heimdall-v2/bridge/service"
	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/version"
)

const (
	flagNodeDirPrefix    = "node-dir-prefix"
	flagNumValidators    = "v"
	flagNumNonValidators = "n"
	flagOutputDir        = "output-dir"
	flagNodeDaemonHome   = "node-daemon-home"
	flagNodeCliHome      = "node-cli-home"
	flagNodeHostPrefix   = "node-host-prefix"
)

const (
	nodeDirPerm                = 0o755
	restServerTimeOutInMinutes = 30
)

var tempDir = func() string {
	dir, err := os.MkdirTemp("", "heimdall")
	if err != nil {
		dir = app.DefaultNodeHome
	}
	defer func() {
		err := os.RemoveAll(dir)
		if err != nil {
			fmt.Printf("Failed to remove directory %s: %v\n", dir, err)
		}
	}()

	return dir
}

// ValidatorAccountFormatter helps to print local validator account information
type ValidatorAccountFormatter struct {
	Address string `json:"address,omitempty" yaml:"address"`
	PrivKey string `json:"priv_key,omitempty" yaml:"priv_key"`
	PubKey  string `json:"pub_key,omitempty" yaml:"pub_key"`
}

// GetSignerInfo returns signer information
func GetSignerInfo(pub crypto.PubKey, privKey []byte) ValidatorAccountFormatter {
	privKeyObject := secp256k1.PrivKey(privKey)
	pubKeyObject := secp256k1.PubKey(pub.Bytes())

	return ValidatorAccountFormatter{
		Address: ethCommon.BytesToAddress(pub.Address().Bytes()).String(),
		PubKey:  pubKeyObject.String(),
		PrivKey: "0x" + hex.EncodeToString(privKeyObject[:]),
	}
}

// initCometBFTConfig helps to override default CometBFT Config values.
// It returns cmtcfg.DefaultConfig if no custom configuration is required for the application.
func initCometBFTConfig() *cmtcfg.Config {
	customCMTConfig := cmtcfg.DefaultConfig()

	customCMTConfig.Consensus.TimeoutPropose = 1000 * time.Millisecond
	customCMTConfig.Consensus.TimeoutProposeDelta = 200 * time.Millisecond
	customCMTConfig.Consensus.TimeoutPrevote = 1000 * time.Millisecond
	customCMTConfig.Consensus.TimeoutPrevoteDelta = 200 * time.Millisecond
	customCMTConfig.Consensus.TimeoutPrecommit = 1000 * time.Millisecond
	customCMTConfig.Consensus.TimeoutPrecommitDelta = 200 * time.Millisecond
	customCMTConfig.Consensus.TimeoutCommit = 500 * time.Millisecond
	customCMTConfig.Consensus.PeerGossipSleepDuration = 25 * time.Millisecond
	customCMTConfig.Consensus.PeerQueryMaj23SleepDuration = 200 * time.Millisecond

	return customCMTConfig
}

// initAppConfig helps to override the default app config template and configs.
// It returns "", nil if no custom configuration is required for the application.
func initAppConfig() (string, interface{}) {
	srvConf := serverconfig.DefaultConfig()
	srvConf.API.Enable = true // enable REST server by default
	srvConf.API.Address = helper.DefaultHeimdallServerURL
	customAppConfig := helper.CustomAppConfig{
		Config: *srvConf,
		Custom: helper.GetDefaultHeimdallConfig(),
	}

	customAppTemplate := serverconfig.DefaultConfigTemplate + helper.DefaultConfigTemplate

	stringConfigValue := viper.GetViper().GetString(helper.ChainFlag)
	if stringConfigValue != "" {
		customAppConfig.Custom.Chain = stringConfigValue
	}

	return customAppTemplate, customAppConfig
}

func initRootCmd(
	rootCmd *cobra.Command,
	_ client.TxConfig,
	basicManager module.BasicManager,
	hApp *app.HeimdallApp,
) {
	ctx := server.NewDefaultContext()

	cfg := sdk.GetConfig()
	cfg.Seal()

	rootCmd.AddCommand(
		genutilcli.InitCmd(basicManager, app.DefaultNodeHome),
		debug.Cmd(),
		confixcmd.ConfigCommand(),
		pruning.Cmd(newApp, app.DefaultNodeHome),
		snapshot.Cmd(newApp),
		MigrateCommand(),
	)

	AddCommandsWithStartCmdOptions(rootCmd, app.DefaultNodeHome, newApp, appExport, server.StartCmdOptions{
		AddFlags: func(startCmd *cobra.Command) {
			startCmd.Flags().Bool(helper.RestServerFlag, true, "Enable the REST server")
			startCmd.Flags().Bool(helper.BridgeFlag, false, "Enable the bridge server")
			startCmd.Flags().Bool(helper.AllProcessesFlag, false, "Enable all bridge processes")
			startCmd.Flags().StringSlice(helper.OnlyProcessesFlag, []string{}, "Enable only the specified bridge process(es)")
		},
		PostSetup: func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
			helper.InitHeimdallConfig("")

			// wait for the rest server to start.
			resultChan := make(chan string, 1)

			// Create a cancellable context for the server check
			checkCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			timer := time.NewTimer(restServerTimeOutInMinutes * time.Minute)
			defer timer.Stop()

			go checkServerStatus(checkCtx, helper.GetHeimdallServerEndpoint(util.AccountParamsURL), resultChan)

			select {
			case result := <-resultChan:
				fmt.Println("Fetch successful, received data:", result)
				cancel() // stop the poller immediately

			case <-timer.C:
				fmt.Printf(
					"Fetch operation timed out - REST server did not respond within %d minutes\n",
					restServerTimeOutInMinutes,
				)

				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

			waitLoop:
				for {
					select {
					case result := <-resultChan:
						fmt.Println("Fetch successful, received data:", result)
						// stop polling now that we’re done
						cancel()
						// continue with PostSetup
						break waitLoop

					case <-ticker.C:
						// keep printing every second if rest-server is not responding
						fmt.Println("Warning: still waiting for REST server to respond... Something wrong with it, please check!")

					case <-ctx.Done():
						// if the app is shutting down, stop waiting and continue without the REST server
						fmt.Println("Startup context cancelled while waiting... Continuing without REST server")
						break waitLoop
					}
				}
			}

			chainParam, err := util.GetChainmanagerParams(clientCtx.Codec)
			if err != nil {
				return err
			}

			clientCtx = clientCtx.
				WithChainID(chainParam.ChainParams.HeimdallChainId)

			// start bridge
			if viper.GetBool(helper.BridgeFlag) {
				bridge.AdjustDBValue(rootCmd)
				g.Go(func() error {
					return bridge.StartWithCtx(ctx, clientCtx)
				})
			}

			return nil
		},
	})

	// add keybase, auxiliary RPC, query, genesis, and tx child commands
	rootCmd.AddCommand(
		server.StatusCommand(),
		queryCommand(),
		txCommand(hApp.BasicManager),
		keys.Commands(),
	)

	// add custom commands
	rootCmd.AddCommand(
		testnetCmd(ctx, hApp.BasicManager),
		generateKeystore(),
		importKeyStore(),
		generateValidatorKey(),
		importValidatorKey(),
		StakeCmd(),
		ApproveCmd(),
		version.Cmd,
	)

	rootCmd.AddCommand(showPrivateKeyCmd())
	rootCmd.AddCommand(VerifyGenesis(ctx, hApp))

	rootCmd.AddCommand(veDecodeCmd())
	rootCmd.AddCommand(showAccountCmd())
}

func checkServerStatus(ctx context.Context, url string, resultChan chan<- string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Create HTTP client
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	for {
		select {
		case <-ctx.Done():
			// Context canceled, stop checking
			return
		case <-ticker.C:
			// Check the rest server
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			fmt.Printf("Error creating HTTP request: %v\n", err)
			return
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			fmt.Printf("Error fetching URL %s: %v\n", url, err)
			continue
		}

		func() {
			defer func(Body io.ReadCloser) {
				if closeErr := Body.Close(); closeErr != nil {
					fmt.Printf("Error closing response body: %v\n", closeErr)
				}
			}(resp.Body)

			if resp.StatusCode == http.StatusOK {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					fmt.Printf("Error reading response body: %v\n", err)
					return
				}

				select {
				case resultChan <- string(body):
					// result successfully sent
				case <-ctx.Done():
					// Context canceled while trying to send the result
				}
				return
			} else {
				fmt.Printf("Received non-OK HTTP status from %s: %d\n", url, resp.StatusCode)
			}
		}()
	}
}

// AddCommandsWithStartCmdOptions adds server commands with the provided StartCmdOptions.
func AddCommandsWithStartCmdOptions(rootCmd *cobra.Command, defaultNodeHome string, appCreator types.AppCreator, appExport types.AppExporter, opts server.StartCmdOptions) {
	cometCmd := &cobra.Command{
		Use:     "comet",
		Aliases: []string{"cometbft", "tendermint"},
		Short:   "CometBFT subcommands",
	}

	cometCmd.AddCommand(
		server.ShowNodeIDCmd(),
		server.ShowValidatorCmd(),
		server.ShowAddressCmd(),
		server.VersionCmd(),
		cmtcmd.ResetAllCmd,
		cmtcmd.ResetStateCmd,
		server.BootstrapStateCmd(appCreator),
	)

	startCmd := server.StartCmdWithOptions(appCreator, defaultNodeHome, opts)

	rootCmd.AddCommand(
		startCmd,
		cometCmd,
		server.ExportCmd(appExport, defaultNodeHome),
		server.NewRollbackCmd(appCreator, defaultNodeHome),
	)
}

func queryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "query",
		Aliases:                    []string{"q"},
		Short:                      "Querying subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(
		rpc.QueryEventForTxCmd(),
		server.QueryBlockCmd(),
		authcmd.QueryTxsByEventsCmd(),
		server.QueryBlocksCmd(),
		authcmd.QueryTxCmd(),
		server.QueryBlockResultsCmd(),
		rpc.ValidatorCommand(),
	)

	return cmd
}

func txCommand(bm module.BasicManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "tx",
		Short:                      "Transactions subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	// add modules' tx commands
	bm.AddTxCommands(cmd)

	cmd.AddCommand(
		authcmd.GetSignCommand(),
		authcmd.GetSignBatchCommand(),
		authcmd.GetMultiSignCommand(),
		authcmd.GetMultiSignBatchCmd(),
		authcmd.GetValidateSignaturesCommand(),
		authcmd.GetBroadcastCommand(),
		authcmd.GetEncodeCommand(),
		authcmd.GetDecodeCommand(),
		authcmd.GetSimulateCmd(),
		authcmd.GetSignCommand(),
	)

	return cmd
}

// newApp creates the application
func newApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	appOpts servertypes.AppOptions,
) servertypes.Application {
	helper.InitHeimdallConfig("")
	baseappOptions := server.DefaultBaseappOptions(appOpts)

	return app.NewHeimdallApp(
		logger, db, traceStore, true,
		appOpts,
		baseappOptions...,
	)
}

// appExport creates a new heimdall app (optionally at a given height) and exports state.
func appExport(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	height int64,
	_ bool,
	_ []string,
	appOpts servertypes.AppOptions,
	modulesToExport []string,
) (servertypes.ExportedApp, error) {
	// this check is necessary as we use the flag in x/upgrade.
	// we can exit more gracefully by checking the flag here.
	homePath, ok := appOpts.Get(flags.FlagHome).(string)
	if !ok || homePath == "" {
		return servertypes.ExportedApp{}, errors.New("application home not set")
	}

	viperAppOpts, ok := appOpts.(*viper.Viper)
	if !ok {
		return servertypes.ExportedApp{}, errors.New("appOpts is not viper.Viper")
	}

	// overwrite the FlagInvCheckPeriod
	viperAppOpts.Set(server.FlagInvCheckPeriod, 1)
	appOpts = viperAppOpts

	var hApp *app.HeimdallApp
	if height != -1 {
		hApp = app.NewHeimdallApp(logger, db, traceStore, false, appOpts)

		if err := hApp.LoadHeight(height); err != nil {
			return servertypes.ExportedApp{}, err
		}
	} else {
		hApp = app.NewHeimdallApp(logger, db, traceStore, true, appOpts)
	}

	return hApp.ExportAppStateAndValidators(false, nil, modulesToExport)
}

// generateKeystore generate the keystore file from the private key or generates a new key
func generateKeystore() *cobra.Command {
	var generateNew bool

	cmd := &cobra.Command{
		Use: "generate-keystore [private-key] or generate-keystore --generate-new",
		Short: `Generates a keystore file from a private key, or generates a new key. If --generate-new is set, a new key will be created.
		If [private-key] is provided, it will be used instead of generating a new key. If both are provided, the private key will be used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var pk *ecdsa.PrivateKey
			var err error

			if len(args) > 0 {
				s := strings.ReplaceAll(args[0], "0x", "")
				pk, err = ethcrypto.HexToECDSA(s)
				if err != nil {
					return fmt.Errorf("invalid private key: %w", err)
				}
			} else if generateNew {
				pk, err = ethcrypto.GenerateKey()
				if err != nil {
					return fmt.Errorf("failed to generate key: %w", err)
				}
			} else {
				return fmt.Errorf("must provide a private key or use --generate-new flag")
			}

			if err = createKeyStore(pk); err != nil {
				return fmt.Errorf("failed to create keystore: %w", err)
			}

			fmt.Println("Keystore generated successfully.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&generateNew, "generate-new", false, "Generate a new private key")
	return cmd
}

// importKeyStore imports keystore from the private key in the given file path
func importKeyStore() *cobra.Command {
	return &cobra.Command{
		Use:   "import-keystore <keystore-file>",
		Short: "Import keystore from a private key stored in file (without 0x prefix)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pk, err := ethcrypto.LoadECDSA(args[0])
			if err != nil {
				return err
			}

			if err = createKeyStore(pk); err != nil {
				return err
			}

			return nil
		},
	}
}

// generateValidatorKey generate validator key
func generateValidatorKey() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate-validator-key",
		Short: "Generate validator key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := strings.ReplaceAll(args[0], "0x", "")

			ds, err := hex.DecodeString(s)
			if err != nil {
				return err
			}

			filepv := privval.NewFilePV(secp256k1.PrivKey(ds[:]), "priv_validator_key.json", "priv_validator_state.json")
			filepv.Save()

			return nil
		},
	}

	return cmd
}

// importValidatorKey imports validator private key from the given file path
func importValidatorKey() *cobra.Command {
	cdc := codec.NewLegacyAmino()
	cdc.RegisterInterface((*crypto.PubKey)(nil), nil)
	cdc.RegisterInterface((*crypto.PrivKey)(nil), nil)
	cdc.RegisterConcrete(secp256k1.PubKey{}, "cometbft/PubKeySecp256k1eth", nil)
	cdc.RegisterConcrete(secp256k1.PrivKey{}, "cometbft/PrivKeySecp256k1eth", nil)
	return &cobra.Command{
		Use:   "import-validator-key <private-key-file>",
		Short: "Import private key from a private key stored in file (without 0x prefix)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pk, err := ethcrypto.LoadECDSA(args[0])
			if err != nil {
				return err
			}

			bz := ethcrypto.FromECDSA(pk)
			// set the private object
			privKeyObject := secp256k1.PrivKey(bz)

			// node key
			nodeKey := privval.FilePVKey{
				Address: privKeyObject.PubKey().Address(),
				PubKey:  privKeyObject.PubKey(),
				PrivKey: privKeyObject,
			}

			jsonBytes, err := cdc.MarshalJSONIndent(nodeKey, "", "  ")
			if err != nil {
				return err
			}

			err = os.WriteFile("priv_validator_key.json", jsonBytes, 0o600)
			if err != nil {
				return err
			}

			fmt.Println("Private validator key saved to priv_validator_key.json")
			return nil
		},
	}
}

func showPrivateKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-private-key",
		Short: "Print the account's private key",
		Run: func(cmd *cobra.Command, args []string) {
			// init heimdall config
			helper.InitHeimdallConfig("")

			// get private and public keys
			privKeyObject := helper.GetPrivKey()

			account := &ValidatorAccountFormatter{
				PrivKey: "0x" + hex.EncodeToString(privKeyObject[:]),
			}

			b, err := json.MarshalIndent(account, "", "    ")
			if err != nil {
				panic(err)
			}

			// prints json info
			fmt.Printf("%s", string(b))
		},
	}
}

func showAccountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-account",
		Short: "Print the account's address and public key",
		Run: func(cmd *cobra.Command, args []string) {
			// init heimdall config
			helper.InitHeimdallConfig("")

			// get public keys
			pubObject := helper.GetPubKey()

			account := &ValidatorAccountFormatter{
				Address: ethCommon.BytesToAddress(pubObject.Address().Bytes()).String(),
				PubKey:  "0x" + hex.EncodeToString(pubObject[:]),
			}

			b, err := json.MarshalIndent(account, "", "    ")
			if err != nil {
				panic(err)
			}

			// prints json info
			fmt.Printf("%s", string(b))
		},
	}
}

// VerifyGenesis verifies the genesis file and brings it to sync with the on-chain contract
func VerifyGenesis(ctx *server.Context, hApp *app.HeimdallApp) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify-genesis",
		Short: "Verify if the genesis matches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config := ctx.Config
			config.SetRoot(viper.GetString(flags.FlagHome))
			helper.InitHeimdallConfig("")

			// Loading genesis doc
			genDoc, err := cmttypes.GenesisDocFromFile(filepath.Join(config.RootDir, "config/genesis.json"))
			if err != nil {
				return err
			}

			// get genesis state
			var genesisState app.GenesisState
			err = json.Unmarshal(genDoc.AppState, &genesisState)
			if err != nil {
				return err
			}

			clientCtx := client.GetClientContextFromCmd(cmd)
			cliCdc := clientCtx.Codec

			return hApp.BasicManager.ValidateGenesis(cliCdc, hApp.GetTxConfig(), genesisState)
		},
	}

	return cmd
}

// keyFileName implements the naming convention for keyfiles:
// UTC--<created_at UTC ISO8601>-<address hex>
func keyFileName(keyAddr ethCommon.Address) string {
	ts := time.Now().UTC()
	return fmt.Sprintf("UTC--%s--%s", toISO8601(ts), hex.EncodeToString(keyAddr[:]))
}

func toISO8601(t time.Time) string {
	var tz string

	name, offset := t.Zone()
	if name == "UTC" {
		tz = "Z"
	} else {
		tz = fmt.Sprintf("%03d00", offset/3600)
	}

	return fmt.Sprintf("%04d-%02d-%02dT%02d-%02d-%02d.%09d%s",
		t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), tz)
}

// promptPassphrase prompts the user for a passphrase.  Set confirmation to true
// to require the user to confirm the passphrase.
func promptPassphrase(confirmation bool) (string, error) {
	passphrase, err := prompt.Stdin.PromptPassword("Passphrase: ")
	if err != nil {
		return "", err
	}

	if confirmation {
		confirm, err := prompt.Stdin.PromptPassword("Repeat passphrase: ")
		if err != nil {
			return "", err
		}

		if passphrase != confirm {
			return "", errors.New("passphrases do not match")
		}
	}

	return passphrase, nil
}

// Total Validators to be included in the testnet
func getTotalNumberOfNodes() int {
	numValidators := viper.GetInt(flagNumValidators)
	numNonValidators := viper.GetInt(flagNumNonValidators)

	return numNonValidators + numValidators
}

// nodeDir gets the node directory path
func nodeDir(i int) string {
	outDir := viper.GetString(flagOutputDir)
	nodeDirName := fmt.Sprintf("%s%d", viper.GetString(flagNodeDirPrefix), i)
	nodeDaemonHomeName := viper.GetString(flagNodeDaemonHome)

	return filepath.Join(outDir, nodeDirName, nodeDaemonHomeName)
}

// hostnameOrIP returns the IP or the hostname of the node
func hostnameOrIP(i int) string {
	return fmt.Sprintf("%s%d", viper.GetString(flagNodeHostPrefix), i)
}

// populatePersistentPeersInConfigAndWriteIt populates persistent peers in config
func populatePersistentPeersInConfigAndWriteIt(config *cmtcfg.Config) {
	persistentPeers := make([]string, getTotalNumberOfNodes())

	for i := 0; i < getTotalNumberOfNodes(); i++ {
		config.SetRoot(nodeDir(i))

		nodeKey, err := p2p.LoadNodeKey(config.NodeKeyFile())
		if err != nil {
			return
		}

		persistentPeers[i] = p2p.IDAddressString(nodeKey.ID(), fmt.Sprintf("%s:%d", hostnameOrIP(i), 26656))
	}

	persistentPeersList := strings.Join(persistentPeers, ",")

	for i := 0; i < getTotalNumberOfNodes(); i++ {
		config.SetRoot(nodeDir(i))
		config.P2P.PersistentPeers = persistentPeersList
		config.P2P.AddrBookStrict = false

		// overwrite default config
		cmtcfg.WriteConfigFile(filepath.Join(nodeDir(i), "config", "config.toml"), config)
	}
}

// InitializeNodeValidatorFiles initializes node and priv validator files
func InitializeNodeValidatorFiles(
	config *cmtcfg.Config) (nodeID string, valPubKey crypto.PubKey, privKey crypto.PrivKey, err error,
) {
	nodeKey, err := p2p.LoadOrGenNodeKey(config.NodeKeyFile())
	if err != nil {
		return nodeID, valPubKey, privKey, err
	}

	nodeID = string(nodeKey.ID())

	pvKeyFile := config.PrivValidatorKeyFile()
	if err := cmtos.EnsureDir(filepath.Dir(pvKeyFile), 0o777); err != nil {
		return nodeID, valPubKey, privKey, err
	}

	pvStateFile := config.PrivValidatorStateFile()
	if err := cmtos.EnsureDir(filepath.Dir(pvStateFile), 0o777); err != nil {
		return nodeID, valPubKey, privKey, err
	}

	FilePv := privval.LoadOrGenFilePV(pvKeyFile, pvStateFile)
	valPubKey, err = FilePv.GetPubKey()
	if err != nil {
		return nodeID, valPubKey, privKey, err
	}

	return nodeID, valPubKey, FilePv.Key.PrivKey, nil
}

func createKeyStore(pk *ecdsa.PrivateKey) error {
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	key := &keystore.Key{
		Id:         id,
		Address:    ethcrypto.PubkeyToAddress(pk.PublicKey),
		PrivateKey: pk,
	}

	passphrase, err := promptPassphrase(true)
	if err != nil {
		return err
	}

	keyJson, err := keystore.EncryptKey(key, passphrase, keystore.StandardScryptN, keystore.StandardScryptP)
	if err != nil {
		return err
	}

	// Then write the new keyfile in place of the old one.
	if err := os.WriteFile(keyFileName(key.Address), keyJson, 0o600); err != nil {
		return err
	}
	return nil
}
