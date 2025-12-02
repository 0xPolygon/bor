package heimdalld

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/libs/tempfile"
	cmttime "github.com/cometbft/cometbft/types/time"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	cosmossecp256k1 "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authTypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankTypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cmdhelper "github.com/0xPolygon/heimdall-v2/cmd"
	hmTypes "github.com/0xPolygon/heimdall-v2/types"
	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	stakingcli "github.com/0xPolygon/heimdall-v2/x/stake/client/cli"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	topupTypes "github.com/0xPolygon/heimdall-v2/x/topup/types"
)

var testnetCmdName = "create-testnet"

// testnetCmd initialises files required to start heimdall testnet
func testnetCmd(_ *server.Context, mbm module.BasicManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   testnetCmdName,
		Short: "Initialize files for a Heimdall testnet",
		Long: `testnet will create "v" + "n" number of directories and populate each with
necessary files (private validator, genesis, config, etc.).

Note, strict rout ability for addresses is turned off in the config file.
Optionally, it will fill in persistent_peers list in config file using either hostnames or IPs.

Example:
testnet --v 4 --n 8 --output-dir ./output --starting-ip-address 192.168.10.2
`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientCtx := client.GetClientContextFromCmd(cmd)
			cliCdc := clientCtx.Codec
			serverCtx := server.GetServerContextFromCmd(cmd)
			config := serverCtx.Config
			outDir := viper.GetString(flagOutputDir)

			// create chain id
			chainID := viper.GetString(flags.FlagChainID)
			if chainID == "" {
				suffix, err := cmdhelper.GenerateRandomString(6)
				if err != nil {
					return err
				}
				chainID = fmt.Sprintf("heimdall-%v", suffix)
			}

			// num of validators = validators in genesis files
			numValidators := viper.GetInt(flagNumValidators)

			// get the total number of validators to be generated
			totalValidators := getTotalNumberOfNodes()

			// first validators start ID
			// there is no validator with id = 0
			startID := viper.GetInt64(stakingcli.FlagValidatorID)
			if startID == 0 {
				startID = 1
			}

			// signers data to dump in the signer-dump file
			signers := make([]ValidatorAccountFormatter, totalValidators)

			// Initialise variables for all validators
			nodeIDs := make([]string, totalValidators)
			valPubKeys := make([]crypto.PubKey, totalValidators)
			privKeys := make([]crypto.PrivKey, totalValidators)
			validators := make([]*stakeTypes.Validator, numValidators)
			dividendAccounts := make([]hmTypes.DividendAccount, numValidators)
			genFiles := make([]string, totalValidators)
			var err error

			homeDir := viper.GetString(flags.FlagHome)
			appCfgFile := filepath.Join(homeDir, "config/app.toml")
			nodeDaemonHomeName := viper.GetString(flagNodeDaemonHome)
			nodeCliHomeName := viper.GetString(flagNodeCliHome)

			for i := 0; i < totalValidators; i++ {
				// get node dir name = PREFIX+INDEX
				nodeDirName := fmt.Sprintf("%s%d", viper.GetString(flagNodeDirPrefix), i)

				// generate node and client dir
				nodeDir := filepath.Join(outDir, nodeDirName, nodeDaemonHomeName)
				clientDir := filepath.Join(outDir, nodeDirName, nodeCliHomeName)

				// set root in config
				config.SetRoot(nodeDir)

				// create config folder
				err := os.MkdirAll(filepath.Join(nodeDir, "config"), nodeDirPerm)
				if err != nil {
					_ = os.RemoveAll(outDir)
					return err
				}

				err = os.MkdirAll(clientDir, nodeDirPerm)
				if err != nil {
					_ = os.RemoveAll(outDir)
					return err
				}

				nodeIDs[i], valPubKeys[i], privKeys[i], err = InitializeNodeValidatorFiles(config)
				if err != nil {
					return err
				}

				genFiles[i] = config.GenesisFile()

				cosmosPrivKey := &cosmossecp256k1.PrivKey{Key: privKeys[i].Bytes()}

				if i < numValidators {
					// create the validator account
					validators[i], err = stakeTypes.NewValidator(
						uint64(startID+int64(i)),
						0,
						0,
						1,
						10000,
						cosmosPrivKey.PubKey(),
						valPubKeys[i].Address().String(),
					)
					if err != nil {
						return err
					}

					// create the dividend account for the validator
					dividendAccounts[i] = hmTypes.DividendAccount{
						User:      validators[i].Signer,
						FeeAmount: big.NewInt(0).String(),
					}
				}

				signers[i] = GetSignerInfo(valPubKeys[i], privKeys[i].Bytes())

				cf, err := os.ReadFile(appCfgFile)
				if err != nil {
					return err
				}

				// write the config file
				err = os.WriteFile(filepath.Join(config.RootDir, "config/app.toml"), cf, 0o600)
				if err != nil {
					return err
				}
			}

			// other data
			genAccounts := make([]authTypes.GenesisAccount, 0, totalValidators)
			genBalances := make([]bankTypes.Balance, 0, totalValidators)
			for i := 0; i < totalValidators; i++ {
				accTokens := sdk.TokensFromConsensusPower(1000, sdk.DefaultPowerReduction)
				coins := sdk.Coins{
					sdk.NewCoin("pol", accTokens),
				}
				addr, err := sdk.AccAddressFromHex(valPubKeys[i].Address().String())
				if err != nil {
					return err
				}

				cosmosPrivKey := cosmossecp256k1.PrivKey{Key: privKeys[i].Bytes()}

				addrStr := valPubKeys[i].Address().String()
				genBalances = append(genBalances, bankTypes.Balance{Address: addrStr, Coins: coins.Sort()})
				genAccounts = append(genAccounts, authTypes.NewBaseAccount(addr, cosmosPrivKey.PubKey(), uint64(i+1), 0))
			}

			validatorSet := stakeTypes.NewValidatorSet(validators)

			for i := 0; i < totalValidators; i++ {
				populatePersistentPeersInConfigAndWriteIt(config)
			}

			appGenState := mbm.DefaultGenesis(cliCdc)

			// set auth genesis state
			var authGenState authTypes.GenesisState
			clientCtx.Codec.MustUnmarshalJSON(appGenState[authTypes.ModuleName], &authGenState)

			accounts, err := authTypes.PackAccounts(genAccounts)
			if err != nil {
				return err
			}

			authGenState.Accounts = accounts

			appGenState[authTypes.ModuleName] = clientCtx.Codec.MustMarshalJSON(&authGenState)

			// set bank genesis state
			var bankGenState bankTypes.GenesisState
			clientCtx.Codec.MustUnmarshalJSON(appGenState[bankTypes.ModuleName], &bankGenState)

			bankGenState.Balances = bankTypes.SanitizeGenesisBalances(genBalances)

			for _, bal := range bankGenState.Balances {
				bankGenState.Supply = bankGenState.Supply.Add(bal.Coins...)
			}

			appGenState[bankTypes.ModuleName] = clientCtx.Codec.MustMarshalJSON(&bankGenState)

			// set stake genesis state
			appGenState, err = stakeTypes.SetGenesisStateToAppState(cliCdc, appGenState, validators, *validatorSet)
			if err != nil {
				return err
			}

			// set bor genesis state
			appGenState, err = borTypes.SetGenesisStateToAppState(cliCdc, appGenState, *validatorSet)
			if err != nil {
				return err
			}

			// topup state change
			appGenState, err = topupTypes.SetGenesisStateToAppState(cliCdc, appGenState, dividendAccounts)
			if err != nil {
				return err
			}

			appStateJSON, err := json.MarshalIndent(appGenState, "", " ")
			if err != nil {
				return err
			}

			time := cmttime.Now()
			for i := 0; i < totalValidators; i++ {
				if err = genutil.ExportGenesisFileWithTime(genFiles[i], chainID, nil, appStateJSON, time); err != nil {
					return err
				}
			}

			// dump signer information into a JSON file
			// this is required when setting up node dirs for devnet
			dump := viper.GetBool("signer-dump")
			if dump {
				signerJSON, err := json.MarshalIndent(signers, "", "  ")
				if err != nil {
					return err
				}

				if err := tempfile.WriteFileAtomic(filepath.Join(outDir, "signer-dump.json"), signerJSON, 0o600); err != nil {
					fmt.Println("Error writing signer-dump", err)
					return err
				}
			}

			fmt.Printf("Successfully initialized %d node directories\n", totalValidators)
			return nil
		},
	}

	cmd.Flags().Int(flagNumValidators, 4,
		"Number of validators to initialize the testnet with",
	)

	cmd.Flags().Int(flagNumNonValidators, 8,
		"Number of non-validators to initialize the testnet with",
	)

	cmd.Flags().StringP(flagOutputDir, "o", "./mytestnet",
		"Directory to store initialization data for the testnet",
	)

	cmd.Flags().String(flagNodeDirPrefix, "node",
		"Prefix the directory name for each node with (node results in node0, node1, ...)",
	)

	cmd.Flags().String(flagNodeDaemonHome, "heimdalld",
		"Home directory of the node's daemon configuration",
	)

	cmd.Flags().String(flagNodeHostPrefix, "node",
		"Hostname prefix (node results in persistent peers list ID0@node0:26656, ID1@node1:26656, ...)")

	cmd.Flags().String(flags.FlagChainID, "", "genesis file chain-id, if left blank will be randomly created")
	cmd.Flags().Bool("signer-dump", true, "dumps all signer information in a json file")

	return cmd
}
