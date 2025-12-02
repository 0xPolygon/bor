package helper

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	logger "cosmossdk.io/log"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cometbft/cometbft/privval"
	"github.com/cosmos/cosmos-sdk/client/flags"
	addressCodec "github.com/cosmos/cosmos-sdk/codec/address"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/0xPolygon/heimdall-v2/file"
	borgrpc "github.com/0xPolygon/heimdall-v2/x/bor/grpc"
)

const (
	CometBFTNodeFlag       = "node"
	WithHeimdallConfigFlag = "app"
	RestServerFlag         = "rest-server"
	BridgeFlag             = "bridge"
	AllProcessesFlag       = "all"
	OnlyProcessesFlag      = "only"
	LogsWriterFileFlag     = "logs_writer_file"
	SeedsFlag              = "seeds"

	MainChain   = "mainnet"
	MumbaiChain = "mumbai"
	AmoyChain   = "amoy"

	// app config flags

	MainRPCUrlFlag  = "eth_rpc_url"
	BorRPCUrlFlag   = "bor_rpc_url"
	BorGRPCUrlFlag  = "bor_grpc_url"
	BorGRPCFlagFlag = "bor_grpc_flag"

	CometBFTNodeURLFlag          = "comet_bft_rpc_url"
	HeimdallServerURLFlag        = "heimdall_rest_server"
	GRPCServerURLFlag            = "grpc_server"
	AmqpURLFlag                  = "amqp_url"
	CheckpointerPollIntervalFlag = "checkpoint_poll_interval"
	SyncerPollIntervalFlag       = "syncer_poll_interval"
	NoACKPollIntervalFlag        = "noack_poll_interval"
	ClerkPollIntervalFlag        = "clerk_poll_interval"
	SpanPollIntervalFlag         = "span_poll_interval"
	MilestonePollIntervalFlag    = "milestone_poll_interval"
	MainChainGasLimitFlag        = "main_chain_gas_limit"
	MainChainMaxGasPriceFlag     = "main_chain_max_gas_price"

	NoACKWaitTimeFlag = "no_ack_wait_time"
	ChainFlag         = "chain"
	ProducerVotesFlag = "producer_votes"

	DefaultMainRPCUrl  = "http://localhost:9545"
	DefaultBorRPCUrl   = "http://localhost:8545"
	DefaultBorGRPCUrl  = "localhost:3131"
	DefaultBorGRPCFlag = false

	DefaultEthRPCTimeout = 5 * time.Second
	DefaultBorRPCTimeout = 1 * time.Second

	// DefaultAmqpURL represents default AMQP url
	DefaultAmqpURL = "amqp://guest:guest@localhost:5672/"

	DefaultHeimdallServerURL = "tcp://0.0.0.0:1317"

	DefaultCometBFTNodeURL = "http://0.0.0.0:26657"

	NoACKWaitTime = 1800 * time.Second // Time ack service waits to clear buffer and elect new proposer (1800 seconds ~ 30 min)

	DefaultCheckpointPollInterval = 5 * time.Minute
	DefaultSyncerPollInterval     = 1 * time.Minute
	DefaultNoACKPollInterval      = 1010 * time.Second
	DefaultClerkPollInterval      = 10 * time.Second
	DefaultSpanPollInterval       = 1 * time.Minute

	DefaultMilestonePollInterval = 30 * time.Second

	// Self healing defaults
	DefaultEnableSH                = false
	DefaultSHStateSyncedInterval   = 3 * time.Hour
	DefaultSHStakeUpdateInterval   = 3 * time.Hour
	DefaultSHCheckpointAckInterval = 30 * time.Minute
	DefaultSHMaxDepthDuration      = 24 * time.Hour

	DefaultMainChainGasLimit = uint64(5000000)

	DefaultMainChainMaxGasPrice = 400000000000 // 400 Gwei

	DefaultBorChainID      = "15001"
	DefaultHeimdallChainID = "heimdall-15001"

	DefaultLogsType = "json"
	DefaultChain    = MainChain

	DefaultCometBFTNode = "tcp://0.0.0.0:26657"

	DefaultMainnetSeeds     = "e019e16d4e376723f3adc58eb1761809fea9bee0@35.234.150.253:26656,7f3049e88ac7f820fd86d9120506aaec0dc54b27@34.89.75.187:26656,1f5aff3b4f3193404423c3dd1797ce60cd9fea43@34.142.43.249:26656,2d5484feef4257e56ece025633a6ea132d8cadca@35.246.99.203:26656,17e9efcbd173e81a31579310c502e8cdd8b8ff2e@35.197.233.240:26656,72a83490309f9f63fdca3a0bef16c290e5cbb09c@35.246.95.65:26656,00677b1b2c6282fb060b7bb6e9cc7d2d05cdd599@34.105.180.11:26656,721dd4cebfc4b78760c7ee5d7b1b44d29a0aa854@34.147.169.102:26656,4760b3fc04648522a0bcb2d96a10aadee141ee89@34.89.55.74:26656"
	DefaultAmoyTestnetSeeds = "e4eabef3111155890156221f018b0ea3b8b64820@35.197.249.21:26656,811c3127677a4a34df907b021aad0c9d22f84bf4@34.89.39.114:26656,2ec15d1d33261e8cf42f57236fa93cfdc21c1cfb@35.242.167.175:26656,38120f9d2c003071a7230788da1e3129b6fb9d3f@34.89.15.223:26656,2f16f3857c6c99cc11e493c2082b744b8f36b127@34.105.128.110:26656,2833f06a5e33da2e80541fb1bfde2a7229877fcb@34.89.21.99:26656,2e6f1342416c5d758f5ae32f388bb76f7712a317@34.89.101.16:26656,a596f98b41851993c24de00a28b767c7c5ff8b42@34.89.11.233:26656"

	DefaultMainnetProducers = "91,92,93"

	DefaultAmoyTestnetProducers = "1,2,3"

	DefaultLocalTestnetProducers = "1,2,3"

	secretFilePerm = 0o600

	// MaxStateSyncSize is the new max state sync size after SpanOverrideHeight hard fork
	MaxStateSyncSize = 30000
)

func init() {
	Logger = logger.NewLogger(os.Stdout, logger.LevelOption(zerolog.InfoLevel))
}

// CustomConfig represents heimdall config
type CustomConfig struct {
	EthRPCUrl      string `mapstructure:"eth_rpc_url"`       // RPC endpoint for main chain
	BorRPCUrl      string `mapstructure:"bor_rpc_url"`       // RPC endpoint for bor chain
	BorGRPCFlag    bool   `mapstructure:"bor_grpc_flag"`     // gRPC flag for bor chain
	BorGRPCUrl     string `mapstructure:"bor_grpc_url"`      // gRPC endpoint for bor chain
	CometBFTRPCUrl string `mapstructure:"comet_bft_rpc_url"` // cometBft node url
	SubGraphUrl    string `mapstructure:"sub_graph_url"`     // sub graph url

	EthRPCTimeout time.Duration `mapstructure:"eth_rpc_timeout"` // timeout for eth rpc
	BorRPCTimeout time.Duration `mapstructure:"bor_rpc_timeout"` // timeout for bor rpc

	AmqpURL string `mapstructure:"amqp_url"` // amqp url

	MainChainGasLimit uint64 `mapstructure:"main_chain_gas_limit"` // gas-limit for main chain txs, e.g., submit checkpoint

	MainChainMaxGasPrice int64 `mapstructure:"main_chain_max_gas_price"` // max-gas-price for main chain txs

	// config related to bridge
	CheckpointPollInterval  time.Duration `mapstructure:"checkpoint_poll_interval"` // Poll interval for checkpointer service to send new checkpoints or missing ACK
	SyncerPollInterval      time.Duration `mapstructure:"syncer_poll_interval"`     // Poll interval for syncer service to sync for changes on the main chain
	NoACKPollInterval       time.Duration `mapstructure:"noack_poll_interval"`      // Poll interval for ack service to send no-ack in case of no checkpoints
	ClerkPollInterval       time.Duration `mapstructure:"clerk_poll_interval"`
	SpanPollInterval        time.Duration `mapstructure:"span_poll_interval"`
	MilestonePollInterval   time.Duration `mapstructure:"milestone_poll_interval"`
	EnableSH                bool          `mapstructure:"enable_self_heal"`           // Enable self-healing
	SHStateSyncedInterval   time.Duration `mapstructure:"sh_state_synced_interval"`   // Interval to self-heal StateSynced events if missing
	SHStakeUpdateInterval   time.Duration `mapstructure:"sh_stake_update_interval"`   // Interval to self-heal StakeUpdate events if missing
	SHCheckpointAckInterval time.Duration `mapstructure:"sh_checkpoint_ack_interval"` // Interval to self-heal Checkpoint ACKs (New Header Blocks) events if missing
	SHMaxDepthDuration      time.Duration `mapstructure:"sh_max_depth_duration"`      // Max duration that allows to suggest self-healing is not needed

	// wait-time-related options
	NoACKWaitTime time.Duration `mapstructure:"no_ack_wait_time"` // Time ack service waits to clear buffer and elect new proposer

	// Log related options
	LogsType       string `mapstructure:"logs_type"`        // if true, enable logging in json format
	LogsWriterFile string `mapstructure:"logs_writer_file"` // if given, Logs will be written to this file else os.Stdout

	Chain string `mapstructure:"chain"`

	ProducerVotes string `mapstructure:"producer_votes"`
}

type CustomAppConfig struct {
	serverconfig.Config `mapstructure:",squash"`
	Custom              CustomConfig `mapstructure:"custom"`
}

var conf CustomAppConfig

// MainChainClient stores eth client for mainChain
var (
	mainChainClient *ethclient.Client
	mainRPCClient   *rpc.Client
)

// borClient stores eth/rpc client for bor
var (
	borClient     *ethclient.Client
	borRPCClient  *rpc.Client
	borGRPCClient *borgrpc.BorGRPCClient
)

// private key object
var privKeyObject secp256k1.PrivKey

var pubKeyObject secp256k1.PubKey

var producerVotes []uint64

// Logger stores global logger object
var Logger logger.Logger

var veblopHeight int64 = 0

var tallyFixHeight int64 = 0

var disableVPCheckHeight int64 = 0

var disableValSetCheckHeight int64 = 0

var initialHeight int64 = 0

var milestoneDeletionHeight int64 = 0

var faultyMilestoneNumber int64 = 0

type ChainManagerAddressMigration struct {
	PolTokenAddress       string
	RootChainAddress      string
	StakingManagerAddress string
	SlashManagerAddress   string
	StakingInfoAddress    string
	StateSenderAddress    string
}

var chainManagerAddressMigrations = map[string]map[int64]ChainManagerAddressMigration{
	MainChain:   {},
	MumbaiChain: {},
	AmoyChain:   {},
	"default":   {},
}

// parseProducerVotes parses a comma-separated string of producer IDs into a slice of uint64
func parseProducerVotes(producerVotesStr string) []uint64 {
	if producerVotesStr == "" {
		return []uint64{}
	}

	producerStrings := strings.Split(producerVotesStr, ",")
	if len(producerStrings) > 0 && producerStrings[0] != "" {
		votes := make([]uint64, len(producerStrings))
		for i, p := range producerStrings {
			pTrimmed := strings.TrimSpace(p)
			if pTrimmed == "" {
				log.Fatalf("Empty producer ID found in producer votes list: '%s'", producerVotesStr)
			}
			var parseErr error
			votes[i], parseErr = strconv.ParseUint(pTrimmed, 10, 64)
			if parseErr != nil {
				log.Fatalf("Failed to parse producer ID '%s': %v", pTrimmed, parseErr)
			}
		}
		return votes
	}

	return []uint64{}
}

// InitHeimdallConfig initializes with viper config (from heimdall configuration)
func InitHeimdallConfig(homeDir string) {
	if strings.Compare(homeDir, "") == 0 {
		// get home dir from viper
		homeDir = viper.GetString(flags.FlagHome)
	}

	// get heimdall config filepath from the viper/cobra flag
	heimdallConfigFileFromFlag := viper.GetString(WithHeimdallConfigFlag)

	// init heimdall with changed config files
	InitHeimdallConfigWith(homeDir, heimdallConfigFileFromFlag)
}

// InitHeimdallConfigWith initializes passed heimdall/tendermint config files
func InitHeimdallConfigWith(homeDir string, heimdallConfigFileFromFlag string) {
	var err error

	if strings.Compare(homeDir, "") == 0 {
		panic("home directory is not specified")
	}

	if strings.Compare(conf.Custom.BorRPCUrl, "") != 0 || strings.Compare(conf.Custom.BorGRPCUrl, "") != 0 {
		return
	}

	// read configuration from the standard configuration file
	configDir := filepath.Join(homeDir, "config")
	heimdallViper := viper.New()
	heimdallViper.SetEnvPrefix("HEIMDALL")
	heimdallViper.AutomaticEnv()

	if heimdallConfigFileFromFlag == "" {
		heimdallViper.SetConfigName("app")     // name of the config file (without extension)
		heimdallViper.AddConfigPath(configDir) // call multiple times to add many search paths
	} else {
		heimdallViper.SetConfigFile(heimdallConfigFileFromFlag) // set the config file explicitly
	}

	// Handle errors reading the config file
	if err = heimdallViper.ReadInConfig(); err != nil {
		log.Fatal(err)
	}

	// unmarshal configuration from the standard configuration file
	if err = heimdallViper.UnmarshalExact(&conf); err != nil {
		log.Fatalln("unable to unmarshall config", "Error", err)
	}

	//  if there is a file with overrides submitted via flags => read it and merge it with the already read standard configuration
	if heimdallConfigFileFromFlag != "" {
		heimdallViperFromFlag := viper.New()
		heimdallViperFromFlag.SetConfigFile(heimdallConfigFileFromFlag) // set the flag config file explicitly

		err = heimdallViperFromFlag.ReadInConfig()
		if err != nil { // Handle errors reading the config file submitted as a flag
			log.Fatalln("unable to read config file submitted via flag", "Error", err)
		}

		var confFromFlag CustomConfig
		// unmarshal configuration from the configuration file submitted as a flag
		if err = heimdallViperFromFlag.UnmarshalExact(&confFromFlag); err != nil {
			log.Fatalln("unable to unmarshall config file submitted via flag", "Error", err)
		}

		conf.Merge(&confFromFlag)
	}

	// update configuration data with submitted flags
	if err = conf.UpdateWithFlags(viper.GetViper(), Logger); err != nil {
		log.Fatalln("unable to read flag values. Check log for details.", "Error", err)
	}

	// perform check for json logging
	logLevelStr := viper.GetString(flags.FlagLogLevel)
	logLevel, err := zerolog.ParseLevel(logLevelStr)
	if err != nil {
		// Default to info in case of error
		logLevel = zerolog.InfoLevel
	}
	if conf.Custom.LogsType == "json" {
		Logger = logger.NewLogger(GetLogsWriter(conf.Custom.LogsWriterFile), logger.LevelOption(logLevel), logger.OutputJSONOption())
	} else {
		// default fallback
		Logger = logger.NewLogger(GetLogsWriter(conf.Custom.LogsWriterFile), logger.LevelOption(logLevel))
	}

	// perform checks for timeout
	if conf.Custom.EthRPCTimeout == 0 {
		// fallback to default
		Logger.Debug("Missing ETH RPC timeout or invalid value provided, falling back to default", "timeout", DefaultEthRPCTimeout)
		conf.Custom.EthRPCTimeout = DefaultEthRPCTimeout
	}

	if conf.Custom.BorRPCTimeout == 0 {
		// fallback to default
		Logger.Debug("Missing BOR RPC timeout or invalid value provided, falling back to default", "timeout", DefaultBorRPCTimeout)
		conf.Custom.BorRPCTimeout = DefaultBorRPCTimeout
	}

	if conf.Custom.SHStateSyncedInterval == 0 {
		// fallback to default
		Logger.Debug("Missing self-healing StateSynced interval or invalid value provided, falling back to default", "interval", DefaultSHStateSyncedInterval)
		conf.Custom.SHStateSyncedInterval = DefaultSHStateSyncedInterval
	}

	if conf.Custom.SHStakeUpdateInterval == 0 {
		// fallback to default
		Logger.Debug("Missing self-healing StakeUpdate interval or invalid value provided, falling back to default", "interval", DefaultSHStakeUpdateInterval)
		conf.Custom.SHStakeUpdateInterval = DefaultSHStakeUpdateInterval
	}

	if conf.Custom.SHCheckpointAckInterval == 0 {
		// fallback to default
		Logger.Debug("Missing self-healing Checkpoint ACK interval or invalid value provided, falling back to default", "interval", DefaultSHCheckpointAckInterval)
		conf.Custom.SHCheckpointAckInterval = DefaultSHCheckpointAckInterval
	}

	if conf.Custom.SHMaxDepthDuration == 0 {
		// fallback to default
		Logger.Debug("Missing self-healing max depth duration or invalid value provided, falling back to default", "duration", DefaultSHMaxDepthDuration)
		conf.Custom.SHMaxDepthDuration = DefaultSHMaxDepthDuration
	}

	if mainRPCClient, err = rpc.Dial(conf.Custom.EthRPCUrl); err != nil {
		log.Fatalln("Unable to dial via ethClient", "URL", conf.Custom.EthRPCUrl, "chain", "eth", "error", err)
	}

	mainChainClient = ethclient.NewClient(mainRPCClient)

	if borRPCClient, err = rpc.Dial(conf.Custom.BorRPCUrl); err != nil {
		log.Fatal(err)
	}

	borClient = ethclient.NewClient(borRPCClient)

	borGRPCClient = borgrpc.NewBorGRPCClient(conf.Custom.BorGRPCUrl)

	// Set default producers based on chain if not already set by config or flags
	if conf.Custom.ProducerVotes == "" {
		switch conf.Custom.Chain {
		case MainChain:
			conf.Custom.ProducerVotes = DefaultMainnetProducers
			Logger.Debug("Using default mainnet producers", "producers", DefaultMainnetProducers)
		case AmoyChain:
			conf.Custom.ProducerVotes = DefaultAmoyTestnetProducers
			Logger.Debug("Using default amoy producers", "producers", DefaultAmoyTestnetProducers)
		default:
			conf.Custom.ProducerVotes = DefaultLocalTestnetProducers
			Logger.Debug("Using default local producers", "producers", DefaultLocalTestnetProducers)
		}
	}

	producerVotes = parseProducerVotes(conf.Custom.ProducerVotes)
	if len(producerVotes) == 0 {
		Logger.Info("No producer votes configured or parsed.")
	}

	// load pv file, unmarshall and set to privKeyObject
	err = file.PermCheck(file.Rootify("priv_validator_key.json", configDir), secretFilePerm)
	if err != nil {
		Logger.Error(err.Error())
	}

	privVal := privval.LoadFilePV(filepath.Join(configDir, "priv_validator_key.json"), filepath.Join(configDir, "priv_validator_key.json"))
	privKeyObject = privVal.Key.PrivKey.Bytes()
	pubKeyObject = privVal.Key.PubKey.Bytes()

	switch conf.Custom.Chain {
	case MainChain:
		milestoneDeletionHeight = 28525000
		faultyMilestoneNumber = 1941439
		veblopHeight = 0
		tallyFixHeight = 28913694
		disableVPCheckHeight = 25723000
		disableValSetCheckHeight = 25723063
		initialHeight = 24404501
	case MumbaiChain:
		milestoneDeletionHeight = 0
		faultyMilestoneNumber = -1
		veblopHeight = 0
		tallyFixHeight = 0
		disableVPCheckHeight = 0
		disableValSetCheckHeight = 0
		initialHeight = 0
	case AmoyChain:
		milestoneDeletionHeight = 0
		faultyMilestoneNumber = -1
		veblopHeight = 0
		tallyFixHeight = 13143851
		disableVPCheckHeight = 10618199
		disableValSetCheckHeight = 10618299
		initialHeight = 8788501
	default:
		milestoneDeletionHeight = 0
		faultyMilestoneNumber = -1
		veblopHeight = 0
		tallyFixHeight = 0
		disableVPCheckHeight = 0
		disableValSetCheckHeight = 0
		initialHeight = 0
	}
}

// GetDefaultHeimdallConfig returns configuration with default params
func GetDefaultHeimdallConfig() CustomConfig {
	return CustomConfig{
		EthRPCUrl:   DefaultMainRPCUrl,
		BorRPCUrl:   DefaultBorRPCUrl,
		BorGRPCFlag: DefaultBorGRPCFlag,
		BorGRPCUrl:  DefaultBorGRPCUrl,

		ProducerVotes: DefaultMainnetProducers,

		CometBFTRPCUrl: DefaultCometBFTNodeURL,

		EthRPCTimeout: DefaultEthRPCTimeout,
		BorRPCTimeout: DefaultBorRPCTimeout,

		AmqpURL: DefaultAmqpURL,

		MainChainGasLimit: DefaultMainChainGasLimit,

		MainChainMaxGasPrice: DefaultMainChainMaxGasPrice,

		CheckpointPollInterval:  DefaultCheckpointPollInterval,
		SyncerPollInterval:      DefaultSyncerPollInterval,
		NoACKPollInterval:       DefaultNoACKPollInterval,
		ClerkPollInterval:       DefaultClerkPollInterval,
		SpanPollInterval:        DefaultSpanPollInterval,
		MilestonePollInterval:   DefaultMilestonePollInterval,
		EnableSH:                DefaultEnableSH,
		SHStateSyncedInterval:   DefaultSHStateSyncedInterval,
		SHStakeUpdateInterval:   DefaultSHStakeUpdateInterval,
		SHCheckpointAckInterval: DefaultSHCheckpointAckInterval,
		SHMaxDepthDuration:      DefaultSHMaxDepthDuration,

		NoACKWaitTime: NoACKWaitTime,

		LogsType:       DefaultLogsType,
		Chain:          DefaultChain,
		LogsWriterFile: "", // default to stdout
	}
}

// GetConfig returns the cached configuration object
func GetConfig() CustomConfig {
	return conf.Custom
}

//
// Get main/pos clients
//

// GetMainChainRPCClient returns main chain RPC client
func GetMainChainRPCClient() *rpc.Client {
	return mainRPCClient
}

// GetMainClient returns main chain's eth client
func GetMainClient() *ethclient.Client {
	return mainChainClient
}

// GetBorClient returns bor eth client
func GetBorClient() *ethclient.Client {
	return borClient
}

// GetBorRPCClient returns bor RPC client
func GetBorRPCClient() *rpc.Client {
	return borRPCClient
}

// GetPrivKey returns priv key object
func GetPrivKey() secp256k1.PrivKey {
	return privKeyObject
}

// GetPubKey returns pub key object
func GetPubKey() secp256k1.PubKey {
	return pubKeyObject
}

// GetAddress returns address object
func GetAddress() []byte {
	return GetPubKey().Address()
}

// GetAddressString returns the address object as string
func GetAddressString() (string, error) {
	address := GetAddress()
	ac := addressCodec.NewHexCodec()
	addressString, err := ac.BytesToString(address)
	if err != nil {
		return "", err
	}
	return addressString, nil
}

// GetValidChains returns all the valid chains
func GetValidChains() []string {
	return []string{"mainnet", "mumbai", "amoy", "local"}
}

func GetVeblopHeight() int64 {
	return veblopHeight
}

func IsVeblop(blockNum uint64) bool {
	if veblopHeight == 0 {
		return false
	}
	return blockNum >= uint64(veblopHeight)
}

func SetVeblopHeight(height int64) {
	veblopHeight = height
}

func GetTallyFixHeight() int64 {
	return tallyFixHeight
}

func GetDisableVPCheckHeight() int64 {
	return disableVPCheckHeight
}

func GetDisableValSetCheckHeight() int64 {
	return disableValSetCheckHeight
}

func GetInitialHeight() int64 {
	return initialHeight
}

func GetMilestoneDeletionHeight() int64 {
	return milestoneDeletionHeight
}

func GetFaultyMilestoneNumber() uint64 {
	return uint64(faultyMilestoneNumber)
}

func GetChainManagerAddressMigration(blockNum int64) (ChainManagerAddressMigration, bool) {
	chainMigration := chainManagerAddressMigrations[conf.Custom.Chain]
	if chainMigration == nil {
		chainMigration = chainManagerAddressMigrations["default"]
	}

	result, found := chainMigration[blockNum]

	return result, found
}

func GetProducerVotes() []uint64 {
	return producerVotes
}

func GetFallbackProducerVotes() []uint64 {
	switch conf.Custom.Chain {
	case MainChain:
		return parseProducerVotes(DefaultMainnetProducers)
	case AmoyChain:
		return parseProducerVotes(DefaultAmoyTestnetProducers)
	default:
		return parseProducerVotes(DefaultLocalTestnetProducers)
	}
}

// DecorateWithHeimdallFlags adds persistent flags for app configs and bind flags with command
func DecorateWithHeimdallFlags(cmd *cobra.Command, v *viper.Viper, loggerInstance logger.Logger, caller string) {
	// add the with-app-config flag
	cmd.PersistentFlags().String(
		WithHeimdallConfigFlag,
		"",
		"Override of Heimdall app config file (default <home>/config/config.json)",
	)

	if err := v.BindPFlag(WithHeimdallConfigFlag, cmd.PersistentFlags().Lookup(WithHeimdallConfigFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, WithHeimdallConfigFlag), "Error", err)
	}

	// add MainRPCUrlFlag flag
	cmd.PersistentFlags().String(
		MainRPCUrlFlag,
		"",
		"Set RPC endpoint for ethereum chain",
	)

	if err := v.BindPFlag(MainRPCUrlFlag, cmd.PersistentFlags().Lookup(MainRPCUrlFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, MainRPCUrlFlag), "Error", err)
	}

	// add BorRPCUrlFlag flag
	cmd.PersistentFlags().String(
		BorRPCUrlFlag,
		"",
		"Set RPC endpoint for bor chain",
	)

	if err := v.BindPFlag(BorRPCUrlFlag, cmd.PersistentFlags().Lookup(BorRPCUrlFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, BorRPCUrlFlag), "Error", err)
	}

	// add BorGRPCUrlFlag flag
	cmd.PersistentFlags().String(
		BorGRPCUrlFlag,
		"",
		"Set gRPC endpoint for bor chain",
	)

	if err := v.BindPFlag(BorGRPCUrlFlag, cmd.PersistentFlags().Lookup(BorGRPCUrlFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, BorGRPCUrlFlag), "Error", err)
	}

	// add BorGRPCFlagFlag flag
	cmd.PersistentFlags().String(
		BorGRPCFlagFlag,
		"",
		"gRPC flag for bor chain",
	)

	if err := v.BindPFlag(BorGRPCFlagFlag, cmd.PersistentFlags().Lookup(BorGRPCFlagFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, BorGRPCFlagFlag), "Error", err)
	}

	// add CometBFTNodeURLFlag flag
	cmd.PersistentFlags().String(
		CometBFTNodeURLFlag,
		"",
		"Set RPC endpoint for CometBFT",
	)

	if err := v.BindPFlag(CometBFTNodeURLFlag, cmd.PersistentFlags().Lookup(CometBFTNodeURLFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, CometBFTNodeURLFlag), "Error", err)
	}

	// add HeimdallServerURLFlag flag
	cmd.PersistentFlags().String(
		HeimdallServerURLFlag,
		"",
		"Set Heimdall REST server endpoint",
	)

	if err := v.BindPFlag(HeimdallServerURLFlag, cmd.PersistentFlags().Lookup(HeimdallServerURLFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, HeimdallServerURLFlag), "Error", err)
	}

	// add GRPCServerURL flag
	cmd.PersistentFlags().String(
		GRPCServerURLFlag,
		"",
		"Set GRPC Server Endpoint",
	)

	if err := v.BindPFlag(GRPCServerURLFlag, cmd.PersistentFlags().Lookup(GRPCServerURLFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, GRPCServerURLFlag), "Error", err)
	}

	// add AmqpURLFlag flag
	cmd.PersistentFlags().String(
		AmqpURLFlag,
		"",
		"Set AMQP endpoint",
	)

	if err := v.BindPFlag(AmqpURLFlag, cmd.PersistentFlags().Lookup(AmqpURLFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, AmqpURLFlag), "Error", err)
	}

	// add CheckpointerPollIntervalFlag flag
	cmd.PersistentFlags().String(
		CheckpointerPollIntervalFlag,
		"",
		"Set check point pull interval",
	)

	if err := v.BindPFlag(CheckpointerPollIntervalFlag, cmd.PersistentFlags().Lookup(CheckpointerPollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, CheckpointerPollIntervalFlag), "Error", err)
	}

	// add SyncerPollIntervalFlag flag
	cmd.PersistentFlags().String(
		SyncerPollIntervalFlag,
		"",
		"Set syncer pull interval",
	)

	if err := v.BindPFlag(SyncerPollIntervalFlag, cmd.PersistentFlags().Lookup(SyncerPollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, SyncerPollIntervalFlag), "Error", err)
	}

	// add NoACKPollIntervalFlag flag
	cmd.PersistentFlags().String(
		NoACKPollIntervalFlag,
		"",
		"Set no acknowledge pull interval",
	)

	if err := v.BindPFlag(NoACKPollIntervalFlag, cmd.PersistentFlags().Lookup(NoACKPollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, NoACKPollIntervalFlag), "Error", err)
	}

	// add ClerkPollIntervalFlag flag
	cmd.PersistentFlags().String(
		ClerkPollIntervalFlag,
		"",
		"Set clerk pull interval",
	)

	if err := v.BindPFlag(ClerkPollIntervalFlag, cmd.PersistentFlags().Lookup(ClerkPollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, ClerkPollIntervalFlag), "Error", err)
	}

	// add SpanPollIntervalFlag flag
	cmd.PersistentFlags().String(
		SpanPollIntervalFlag,
		"",
		"Set span pull interval",
	)

	if err := v.BindPFlag(SpanPollIntervalFlag, cmd.PersistentFlags().Lookup(SpanPollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, SpanPollIntervalFlag), "Error", err)
	}

	// add MilestonePollIntervalFlag flag
	cmd.PersistentFlags().String(
		MilestonePollIntervalFlag,
		DefaultMilestonePollInterval.String(),
		"Set milestone interval",
	)

	if err := v.BindPFlag(MilestonePollIntervalFlag, cmd.PersistentFlags().Lookup(MilestonePollIntervalFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, MilestonePollIntervalFlag), "Error", err)
	}

	// add MainChainGasLimitFlag flag
	cmd.PersistentFlags().Uint64(
		MainChainGasLimitFlag,
		0,
		"Set main chain gas limit",
	)

	if err := v.BindPFlag(MainChainGasLimitFlag, cmd.PersistentFlags().Lookup(MainChainGasLimitFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, MainChainGasLimitFlag), "Error", err)
	}

	// add MainChainMaxGasPriceFlag flag
	cmd.PersistentFlags().Int64(
		MainChainMaxGasPriceFlag,
		0,
		"Set main chain max gas limit",
	)

	if err := v.BindPFlag(MainChainMaxGasPriceFlag, cmd.PersistentFlags().Lookup(MainChainMaxGasPriceFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, MainChainMaxGasPriceFlag), "Error", err)
	}

	// add NoACKWaitTimeFlag flag
	cmd.PersistentFlags().String(
		NoACKWaitTimeFlag,
		"",
		"Set time ack service waits to clear buffer and elect new proposer",
	)

	if err := v.BindPFlag(NoACKWaitTimeFlag, cmd.PersistentFlags().Lookup(NoACKWaitTimeFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, NoACKWaitTimeFlag), "Error", err)
	}

	// add chain flag
	cmd.PersistentFlags().String(
		ChainFlag,
		"",
		fmt.Sprintf("Set one of the chains: [%s]", strings.Join(GetValidChains(), ",")),
	)

	if err := v.BindPFlag(ChainFlag, cmd.PersistentFlags().Lookup(ChainFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, ChainFlag), "Error", err)
	}

	// add logsWriterFile flag
	cmd.PersistentFlags().String(
		LogsWriterFileFlag,
		"",
		"Set logs writer file, Default is os.Stdout",
	)

	if err := v.BindPFlag(LogsWriterFileFlag, cmd.PersistentFlags().Lookup(LogsWriterFileFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, LogsWriterFileFlag), "Error", err)
	}

	// add producers flag
	cmd.PersistentFlags().String(
		ProducerVotesFlag,
		"",
		"Set comma-separated list of producer IDs",
	)

	if err := v.BindPFlag(ProducerVotesFlag, cmd.PersistentFlags().Lookup(ProducerVotesFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", caller, ProducerVotesFlag), "Error", err)
	}
}

func (c *CustomAppConfig) UpdateWithFlags(v *viper.Viper, loggerInstance logger.Logger) error {
	const logErrMsg = "Unable to read flag."

	// get the endpoint for the ethereum chain from viper/cobra
	stringConfigValue := v.GetString(MainRPCUrlFlag)
	if stringConfigValue != "" {
		c.Custom.EthRPCUrl = stringConfigValue
	}

	// get endpoint for bor chain from viper/cobra
	stringConfigValue = v.GetString(BorRPCUrlFlag)
	if stringConfigValue != "" {
		c.Custom.BorRPCUrl = stringConfigValue
	}

	// get gRPC flag for bor chain from viper/cobra
	boolConfigValue := v.GetBool(BorGRPCFlagFlag)
	if boolConfigValue {
		c.Custom.BorGRPCFlag = boolConfigValue
	}

	// get endpoint for bor chain from viper/cobra
	stringConfigValue = v.GetString(BorGRPCUrlFlag)
	if stringConfigValue != "" {
		c.Custom.BorGRPCUrl = stringConfigValue
	}

	// get endpoint for cometBFT from viper/cobra
	stringConfigValue = v.GetString(CometBFTNodeURLFlag)
	if stringConfigValue != "" {
		c.Custom.CometBFTRPCUrl = stringConfigValue
	}

	// get endpoint for CometBFT from viper/cobra
	stringConfigValue = v.GetString(AmqpURLFlag)
	if stringConfigValue != "" {
		c.Custom.AmqpURL = stringConfigValue
	}

	// get Heimdall REST server endpoint from viper/cobra
	stringConfigValue = v.GetString(HeimdallServerURLFlag)
	if stringConfigValue != "" {
		c.API.Enable = true
		c.API.Address = stringConfigValue
	}

	// get Heimdall GRPC server endpoint from viper/cobra
	stringConfigValue = v.GetString(GRPCServerURLFlag)
	if stringConfigValue != "" {
		c.GRPC.Enable = true
		c.GRPC.Address = stringConfigValue
	}

	// need this error for parsing Duration values
	var err error

	// get the checkpoint poll interval from viper/cobra
	stringConfigValue = v.GetString(CheckpointerPollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.CheckpointPollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", CheckpointerPollIntervalFlag, "Error", err)
			return err
		}
	}

	// get syncer pull interval from viper/cobra
	stringConfigValue = v.GetString(SyncerPollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.SyncerPollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", SyncerPollIntervalFlag, "Error", err)
			return err
		}
	}

	// get the poll interval for ack service to send no-ack in case of no checkpoints from viper/cobra
	stringConfigValue = v.GetString(NoACKPollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.NoACKPollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", NoACKPollIntervalFlag, "Error", err)
			return err
		}
	}

	// get clerk poll interval from viper/cobra
	stringConfigValue = v.GetString(ClerkPollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.ClerkPollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", ClerkPollIntervalFlag, "Error", err)
			return err
		}
	}

	// get span poll interval from viper/cobra
	stringConfigValue = v.GetString(SpanPollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.SpanPollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", SpanPollIntervalFlag, "Error", err)
			return err
		}
	}

	// get milestone poll interval from viper/cobra
	stringConfigValue = v.GetString(MilestonePollIntervalFlag)
	if stringConfigValue != "" {
		if c.Custom.MilestonePollInterval, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", MilestonePollIntervalFlag, "Error", err)
			return err
		}
	}

	// get time that ack service waits to clear buffer and elect the new proposer from viper/cobra
	stringConfigValue = v.GetString(NoACKWaitTimeFlag)
	if stringConfigValue != "" {
		if c.Custom.NoACKWaitTime, err = time.ParseDuration(stringConfigValue); err != nil {
			loggerInstance.Error(logErrMsg, "Flag", NoACKWaitTimeFlag, "Error", err)
			return err
		}
	}

	// get mainChain gas limit from viper/cobra
	uint64ConfigValue := v.GetUint64(MainChainGasLimitFlag)
	if uint64ConfigValue != 0 {
		c.Custom.MainChainGasLimit = uint64ConfigValue
	}

	// get mainChain max gas price from viper/cobra. if it is greater than zero, set it as a configuration parameter
	int64ConfigValue := v.GetInt64(MainChainMaxGasPriceFlag)
	if int64ConfigValue > 0 {
		c.Custom.MainChainMaxGasPrice = int64ConfigValue
	}

	// get chain from viper/cobra flag
	stringConfigValue = v.GetString(ChainFlag)
	if stringConfigValue != "" {
		c.Custom.Chain = stringConfigValue
	}

	stringConfigValue = v.GetString(LogsWriterFileFlag)
	if stringConfigValue != "" {
		c.Custom.LogsWriterFile = stringConfigValue
	}

	// get producer votes from viper/cobra flag
	stringConfigValue = v.GetString(ProducerVotesFlag)
	if stringConfigValue != "" {
		c.Custom.ProducerVotes = stringConfigValue
	}

	return nil
}

func (c *CustomAppConfig) Merge(cc *CustomConfig) {
	if cc.EthRPCUrl != "" {
		c.Custom.EthRPCUrl = cc.EthRPCUrl
	}

	if cc.BorRPCUrl != "" {
		c.Custom.BorRPCUrl = cc.BorRPCUrl
	}

	if !cc.BorGRPCFlag {
		c.Custom.BorGRPCFlag = cc.BorGRPCFlag
	}

	if cc.BorGRPCUrl != "" {
		c.Custom.BorGRPCUrl = cc.BorGRPCUrl
	}

	if cc.CometBFTRPCUrl != "" {
		c.Custom.CometBFTRPCUrl = cc.CometBFTRPCUrl
	}

	if cc.AmqpURL != "" {
		c.Custom.AmqpURL = cc.AmqpURL
	}

	if cc.MainChainGasLimit != 0 {
		c.Custom.MainChainGasLimit = cc.MainChainGasLimit
	}

	if cc.MainChainMaxGasPrice != 0 {
		c.Custom.MainChainMaxGasPrice = cc.MainChainMaxGasPrice
	}

	if cc.CheckpointPollInterval != 0 {
		c.Custom.CheckpointPollInterval = cc.CheckpointPollInterval
	}

	if cc.SyncerPollInterval != 0 {
		c.Custom.SyncerPollInterval = cc.SyncerPollInterval
	}

	if cc.NoACKPollInterval != 0 {
		c.Custom.NoACKPollInterval = cc.NoACKPollInterval
	}

	if cc.ClerkPollInterval != 0 {
		c.Custom.ClerkPollInterval = cc.ClerkPollInterval
	}

	if cc.SpanPollInterval != 0 {
		c.Custom.SpanPollInterval = cc.SpanPollInterval
	}

	if cc.MilestonePollInterval != 0 {
		c.Custom.MilestonePollInterval = cc.MilestonePollInterval
	}

	if cc.SHCheckpointAckInterval != 0 {
		c.Custom.SHCheckpointAckInterval = cc.SHCheckpointAckInterval
	}

	if cc.NoACKWaitTime != 0 {
		c.Custom.NoACKWaitTime = cc.NoACKWaitTime
	}

	if cc.Chain != "" {
		c.Custom.Chain = cc.Chain
	}

	if cc.LogsWriterFile != "" {
		c.Custom.LogsWriterFile = cc.LogsWriterFile
	}

	// Add merge logic for Producers if necessary, though flags and direct config usually take precedence.
	// If direct config file sets it, it's already in c.Custom.Producers before merge.
	// If override file (cc) sets it, we might want to let it override.
	if cc.ProducerVotes != "" {
		c.Custom.ProducerVotes = cc.ProducerVotes
	}
}

// DecorateWithCometBFTFlags creates cometBFT flags for the desired command and binds them to viper
func DecorateWithCometBFTFlags(cmd *cobra.Command, v *viper.Viper, loggerInstance logger.Logger, message string) {
	// add seeds flag
	cmd.PersistentFlags().String(
		SeedsFlag,
		"",
		"Override seeds",
	)

	if err := v.BindPFlag(SeedsFlag, cmd.PersistentFlags().Lookup(SeedsFlag)); err != nil {
		loggerInstance.Error(fmt.Sprintf("%v | BindPFlag | %v", message, SeedsFlag), "Error", err)
	}
}

// UpdateCometBFTConfig updates cometBFT config with flags and default values if needed
func UpdateCometBFTConfig(cometBFTConfig *cfg.Config, v *viper.Viper) {
	// update cometBFTConfig.P2P.Seeds
	seedsFlagValue := v.GetString(SeedsFlag)
	if seedsFlagValue != "" {
		cometBFTConfig.P2P.Seeds = seedsFlagValue
	}

	if cometBFTConfig.P2P.Seeds == "" {
		switch conf.Custom.Chain {
		case MainChain:
			cometBFTConfig.P2P.Seeds = DefaultMainnetSeeds
		case AmoyChain:
			cometBFTConfig.P2P.Seeds = DefaultAmoyTestnetSeeds
		}
	}
}

func GetLogsWriter(logsWriterFile string) io.Writer {
	if logsWriterFile != "" {
		logWriter, err := os.OpenFile(logsWriterFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
		if err != nil {
			log.Fatalf("error opening log writer file: %v", err)
		}

		return logWriter
	} else {
		return os.Stdout
	}
}

// GetBorGRPCClient returns bor gRPC client
func GetBorGRPCClient() *borgrpc.BorGRPCClient {
	return borGRPCClient
}
