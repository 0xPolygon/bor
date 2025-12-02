package heimdalld

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/x/tx/signing"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codecTypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	authTypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankTypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govTypes "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	paramTypes "github.com/cosmos/cosmos-sdk/x/params/types/proposal"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cobra"

	v036gov "github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/gov/v036"
	v036params "github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/params/v036"
	"github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/utils"
	"github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/verify"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
)

var (
	appCodec    *codec.ProtoCodec
	legacyAmino *codec.LegacyAmino
)

const (
	flagChainId       = "chain-id"
	flagGenesisTime   = "genesis-time"
	flagInitialHeight = "initial-height"
	flagVerifyData    = "verify-data"
)

// MigrateCommand returns a command that migrates the heimdall v1 genesis file to heimdall v2.
func MigrateCommand() *cobra.Command {
	cmd := cobra.Command{
		Use:   "migrate [genesis-file] --chain-id=[chain-id] --genesis-time=[genesis-time] --initial-height=[initial-height] [--verify-data=true|false]",
		Short: "Migrate application state",
		Long:  `Run migrations to update the application state (e.g., for a chain upgrade) based on the provided genesis file.`,
		Args:  cobra.ExactArgs(1),
		RunE:  runMigrate,
	}

	cmd.Flags().String(flagChainId, "", "The new network chain id")
	cmd.Flags().String(flagGenesisTime, "", "The new network genesis time")
	cmd.Flags().Uint64(flagInitialHeight, 0, "The new network initial height")
	cmd.Flags().Bool(flagVerifyData, true, "Enable or disable data verification")

	if err := cmd.MarkFlagRequired(flagChainId); err != nil {
		panic(err)
	}

	if err := cmd.MarkFlagRequired(flagGenesisTime); err != nil {
		panic(err)
	}

	if err := cmd.MarkFlagRequired(flagInitialHeight); err != nil {
		panic(err)
	}

	return &cmd
}

// runMigrate handles the execution of the migrate command, performing the migration process.
func runMigrate(cmd *cobra.Command, args []string) error {
	chainId, err := cmd.Flags().GetString(flagChainId)
	if err != nil {
		return err
	}

	genesisTime, err := cmd.Flags().GetString(flagGenesisTime)
	if err != nil {
		return err
	}

	initialHeight, err := cmd.Flags().GetUint64(flagInitialHeight)
	if err != nil {
		return err
	}

	verifyData, err := cmd.Flags().GetBool(flagVerifyData)
	if err != nil {
		return fmt.Errorf("failed to parse --verify-data flag: %w", err)
	}

	flagsToCheck := []string{flagChainId, flagGenesisTime, flagInitialHeight}
	for _, flag := range flagsToCheck {
		if !cmd.Flags().Changed(flag) {
			return fmt.Errorf("flag --%s must be provided", flag)
		}
	}

	genesisFileV1, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	if _, err := os.Stat(genesisFileV1); os.IsNotExist(err) {
		return fmt.Errorf("genesis file does not exist: %s", genesisFileV1)
	}

	dir := filepath.Dir(genesisFileV1)
	base := filepath.Base(genesisFileV1)
	genesisFileV2 := filepath.Join(dir, fmt.Sprintf("migrated_%s", base))

	// If genesisFileV2 already exists, continue with verification
	if _, err := os.Stat(genesisFileV2); err == nil {
		logger.Info("Migrated genesis file already exists", "file", genesisFileV2)

		if verifyData {
			if err := verify.RunMigrationVerification(genesisFileV1, genesisFileV2, logger); err != nil {
				logger.Error("Verification failed", "error", err)
				return err
			}
		}

		return nil
	}

	interfaceRegistry, err := codecTypes.NewInterfaceRegistryWithOptions(codecTypes.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.HexCodec{},
			ValidatorAddressCodec: address.HexCodec{},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create interface registry: %w", err)
	}

	appCodec = codec.NewProtoCodec(interfaceRegistry)
	cryptocodec.RegisterInterfaces(interfaceRegistry)
	authTypes.RegisterInterfaces(interfaceRegistry)
	govTypes.RegisterInterfaces(interfaceRegistry)
	paramTypes.RegisterInterfaces(interfaceRegistry)

	legacyAmino = codec.NewLegacyAmino()
	v036gov.RegisterLegacyAminoCodec(legacyAmino)
	v036params.RegisterLegacyAminoCodec(legacyAmino)

	{
		genesisData, err := performMigrations(genesisFileV1, chainId, genesisTime, initialHeight)
		if err != nil {
			logger.Error("Migration failed", "error", err)
			return err
		}

		logger.Info("Deleting supply")
		// delete supply (not present in v2)
		delete(genesisData, "supply")
		logger.Info("Supply deleted")

		// Re-marshal to JSON and unmarshal into a plain interface{}
		raw, err := json.Marshal(genesisData)
		if err != nil {
			return fmt.Errorf("failed to marshal genesis data: %w", err)
		}

		var generic interface{}
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("failed to unmarshal into generic map: %w", err)
		}

		// Perform the replacement
		replaceMaticWithPol(generic)

		// Save the updated generic version
		if err := saveGenesisFile(generic.(map[string]interface{}), genesisFileV2); err != nil {
			logger.Error("Failed to save migrated genesis file", "error", err)

			return err
		}

		//nolint:ineffassign
		genesisData = nil

		runtime.GC()
	}

	if verifyData {
		if err := verify.RunMigrationVerification(genesisFileV1, genesisFileV2, logger); err != nil {
			logger.Error("Verification failed", "error", err)
			return err
		}
	}

	return nil
}

// performMigrations loads v1 genesis file, executes all the migration functions on the genesis data.
func performMigrations(genesisFileV1, chainId, genesisTime string, initialHeight uint64) (map[string]interface{}, error) {
	globalStart := time.Now()

	logger.Info("Loading genesis file...", "file", genesisFileV1)

	start := time.Now()
	genesisData, err := utils.LoadJSONFromFile(genesisFileV1)
	if err != nil {
		return nil, fmt.Errorf("failed to load genesis file: %w", err)
	}
	logger.Info(fmt.Sprintf("LoadJSONFromFile took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Performing custom migrations...")

	// addMissingCometBFTConsensusParams
	start = time.Now()
	if err := addMissingCometBFTConsensusParams(genesisData, initialHeight); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("addMissingCometBFTConsensusParams took %.2f minutes", time.Since(start).Minutes()))

	// removeUnusedTendermintConsensusParams
	start = time.Now()
	if err := removeUnusedTendermintConsensusParams(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("removeUnusedTendermintConsensusParams took %.2f minutes", time.Since(start).Minutes()))

	// migrateBankModule
	start = time.Now()
	if err := migrateBankModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateBankModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateAuthModule
	start = time.Now()
	if err := migrateAuthModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateAuthModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateGovModule
	start = time.Now()
	if err := migrateGovModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateGovModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateClerkModule
	start = time.Now()
	if err := migrateClerkModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateClerkModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateBorModule
	start = time.Now()
	if err := migrateBorModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateBorModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateCheckpointModule
	start = time.Now()
	if err := migrateCheckpointModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateCheckpointModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateTopupModule
	start = time.Now()
	if err := migrateTopupModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateTopupModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateMilestoneModule
	start = time.Now()
	if err := migrateMilestoneModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateMilestoneModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateChainManagerModule
	start = time.Now()
	if err := migrateChainManagerModule(genesisData, chainId); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateChainManagerModule took %.2f minutes", time.Since(start).Minutes()))

	// migrateStakeModule
	start = time.Now()
	if err := migrateStakeModule(genesisData); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("migrateStakeModule took %.2f minutes", time.Since(start).Minutes()))

	// Set final values
	genesisData["chain_id"] = chainId
	genesisData["genesis_time"] = genesisTime
	genesisData["initial_height"] = strconv.FormatUint(initialHeight, 10)

	logger.Info(fmt.Sprintf("performMigrations took %.2f minutes", time.Since(globalStart).Minutes()))

	return genesisData, nil
}

// saveGenesisFile saves the migrated genesis data to a new file.
func saveGenesisFile(genesisData map[string]interface{}, genesisFileV2 string) error {
	logger.Info("Saving migrated genesis file...", "file", genesisFileV2)

	if err := utils.SaveJSONToFile(genesisData, genesisFileV2); err != nil {
		return fmt.Errorf("failed to save migrated genesis file: %w", err)
	}

	logger.Info("Migration completed successfully")

	return nil
}

// migrateStakeModule renames the staking module to stake, renames current_val_set to current_validator_set
// and migrates the validators and proposer data.
func migrateStakeModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating stake module...")

	if err := utils.RenameProperty(genesisData, "app_state", "staking", "stake"); err != nil {
		return fmt.Errorf("failed to rename staking module: %w", err)
	}

	stakeModule, ok := genesisData["app_state"].(map[string]interface{})["stake"]
	if !ok {
		return fmt.Errorf("stake module not found in app_state")
	}

	stakeData, ok := stakeModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast stake module data")
	}

	if err := utils.RenameProperty(stakeData, ".", "current_val_set", "current_validator_set"); err != nil {
		return fmt.Errorf("failed to rename current_val_set field: %w", err)
	}

	if err := utils.MigrateValidators(stakeData["validators"]); err != nil {
		return fmt.Errorf("failed to migrate validators in stake module: %w", err)
	}

	currentValidatorSet, ok := stakeData["current_validator_set"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find current_validator_set in stake module")
	}

	if err := utils.MigrateValidators(currentValidatorSet["validators"]); err != nil {
		return fmt.Errorf("failed to migrate validators in current_validator_set: %w", err)
	}

	proposer, ok := currentValidatorSet["proposer"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find proposer in current_validator_set")
	}

	if err := utils.MigrateValidator(proposer); err != nil {
		return fmt.Errorf("failed to migrate proposer: %w", err)
	}

	if err := utils.AddProperty(genesisData, "app_state.stake", "previous_block_validator_set", currentValidatorSet); err != nil {
		return fmt.Errorf("failed to add previous blocks validators set to stake module: %w", err)
	}

	emptyLastBlockTxs := map[string]interface{}{
		"txs": []interface{}{},
	}

	if err := utils.AddProperty(genesisData, "app_state.stake", "last_block_txs", emptyLastBlockTxs); err != nil {
		return fmt.Errorf("failed to add empty last_block_txs to stake module: %w", err)
	}

	logger.Info("Stake module migration completed successfully")

	return nil
}

// migrateChainManagerModule renames the chainmanager module params fields to match the new naming convention.
func migrateChainManagerModule(genesisData map[string]interface{}, chainId string) error {
	logger.Info("Migrating chainmanager module...")

	chainmanagerModule, ok := genesisData["app_state"].(map[string]interface{})["chainmanager"]
	if !ok {
		return fmt.Errorf("chainmanager module not found in app_state")
	}

	chainmanagerData, ok := chainmanagerModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast chainmanager module data")
	}

	if err := utils.RenameProperty(chainmanagerData, "params", "mainchain_tx_confirmations", "main_chain_tx_confirmations"); err != nil {
		return fmt.Errorf("failed to rename mainchain_tx_confirmations field: %w", err)
	}

	if err := utils.RenameProperty(chainmanagerData, "params", "maticchain_tx_confirmations", "bor_chain_tx_confirmations"); err != nil {
		return fmt.Errorf("failed to rename mainchain_tx_timeout field: %w", err)
	}

	if err := utils.RenameProperty(chainmanagerData, "params.chain_params", "matic_token_address", "pol_token_address"); err != nil {
		return fmt.Errorf("failed to rename matic_token_address field: %w", err)
	}

	if err := utils.AddProperty(chainmanagerData, "params.chain_params", "heimdall_chain_id", chainId); err != nil {
		return fmt.Errorf("failed to add heimdall_chain_id field: %w", err)
	}

	logger.Info("Chainmanager module migration completed successfully")

	return nil
}

// migrateMilestoneModule adds genesis state for the milestone module because it's not exported from heimdall-v1.
func migrateMilestoneModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating milestone module...")

	checkpointModule, ok := genesisData["app_state"].(map[string]interface{})["checkpoint"]
	if !ok {
		return fmt.Errorf("checkpoint module not found in app_state")
	}

	checkpointData, ok := checkpointModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast checkpoint module data")
	}

	milestones, milestonesFound := checkpointData["milestones"].([]interface{})
	if milestonesFound {
		utils.SortByTimestamp(milestones)

		for i, milestone := range milestones {
			milestoneMap, ok := milestone.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid milestone format at index %d", i)
			}

			hashHex, ok := milestoneMap["hash"].(string)
			if !ok {
				return fmt.Errorf("hash not found in milestone at index %d", i)
			}

			if !strings.HasPrefix(hashHex, "0x") {
				return fmt.Errorf("invalid hash format at index %d", i)
			}

			hashHex = hashHex[2:]

			hashBytes, err := hex.DecodeString(hashHex)
			if err != nil {
				return fmt.Errorf("failed to decode hash at index %d: %w", i, err)
			}

			hashBase64 := base64.StdEncoding.EncodeToString(hashBytes)

			milestoneMap["hash"] = hashBase64
		}
	}

	if err := utils.DeleteProperty(genesisData, "app_state.checkpoint", "milestones"); err != nil {
		return fmt.Errorf("failed to delete milestones from checkpoint module: %w", err)
	}

	genesisData["app_state"].(map[string]interface{})["milestone"] = map[string]interface{}{}

	if !milestonesFound {
		milestones = []interface{}{}
	}

	if err := utils.AddProperty(genesisData, "app_state.milestone", "milestones", milestones); err != nil {
		return fmt.Errorf("failed to add milestones to milestone module: %w", err)
	}

	params := milestoneTypes.DefaultParams()

	if err := utils.AddProperty(genesisData, "app_state.milestone", "params", params); err != nil {
		return fmt.Errorf("failed to add params to milestone module: %w", err)
	}

	logger.Info("Milestone module migration completed successfully")

	return nil
}

// migrateTopupModule renames the tx_sequences field to topup_sequences.
func migrateTopupModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating topup module...")

	topupModule, ok := genesisData["app_state"].(map[string]interface{})["topup"]
	if !ok {
		return fmt.Errorf("topup module not found in app_state")
	}

	topupData, ok := topupModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast topup module data")
	}

	if err := utils.RenameProperty(topupData, ".", "tx_sequences", "topup_sequences"); err != nil {
		return fmt.Errorf("failed to rename topup_sequences field: %w", err)
	}

	logger.Info("Topup module migration completed successfully")

	return nil
}

// migrateGovModule migrates the proposals' votes and content to the new format and adds the new params.
func migrateGovModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating gov module...")

	govModule, ok := genesisData["app_state"].(map[string]interface{})["gov"]
	if !ok {
		return fmt.Errorf("gov module not found in app_state")
	}

	govJSON, err := json.Marshal(govModule)
	if err != nil {
		return fmt.Errorf("failed to marshal gov module: %w", err)
	}

	oldGovState := v036gov.GenesisState{}

	legacyAmino.MustUnmarshalJSON(govJSON, &oldGovState)

	newDeposits := make([]*govTypes.Deposit, len(oldGovState.Deposits))
	for i, oldDeposit := range oldGovState.Deposits {
		newDeposits[i] = &govTypes.Deposit{
			ProposalId: oldDeposit.ProposalID,
			Depositor:  oldDeposit.Depositor.String(),
			Amount:     oldDeposit.Amount,
		}
	}

	newVotes := make([]*govTypes.Vote, len(oldGovState.Votes))
	for i, oldVote := range oldGovState.Votes {
		newVotes[i] = &govTypes.Vote{
			ProposalId: oldVote.ProposalID,
			Voter:      oldVote.Voter.String(),
			Options:    govTypes.NewNonSplitVoteOption(utils.MigrateVoteOption(oldVote.Option)),
		}
	}

	newProposals := make([]*govTypes.Proposal, len(oldGovState.Proposals))
	for i, oldProposal := range oldGovState.Proposals {
		newProposals[i] = &govTypes.Proposal{
			Id:       oldProposal.ProposalID,
			Messages: []*codecTypes.Any{utils.MigrateGovProposalContent(oldProposal.Content)},
			Title:    oldProposal.GetTitle(),
			Summary:  oldProposal.GetDescription(),
			Status:   govTypes.ProposalStatus(oldProposal.Status),
			FinalTallyResult: &govTypes.TallyResult{
				YesCount:        oldProposal.FinalTallyResult.Yes,
				AbstainCount:    oldProposal.FinalTallyResult.Abstain,
				NoCount:         oldProposal.FinalTallyResult.No,
				NoWithVetoCount: oldProposal.FinalTallyResult.NoWithVeto,
			},
			SubmitTime:      &oldProposal.SubmitTime,
			DepositEndTime:  &oldProposal.DepositEndTime,
			TotalDeposit:    oldProposal.TotalDeposit,
			VotingStartTime: &oldProposal.VotingStartTime,
			VotingEndTime:   &oldProposal.VotingEndTime,
		}
	}

	defaultParams := govTypes.DefaultParams()

	params := govTypes.NewParams(
		oldGovState.DepositParams.MinDeposit,
		defaultParams.ExpeditedMinDeposit,
		oldGovState.DepositParams.MaxDepositPeriod,
		oldGovState.VotingParams.VotingPeriod,
		12*time.Hour, // Because the default voting period is 1 day
		oldGovState.TallyParams.Quorum,
		oldGovState.TallyParams.Threshold,
		defaultParams.ExpeditedThreshold,
		oldGovState.TallyParams.Veto,
		defaultParams.MinInitialDepositRatio,
		defaultParams.ProposalCancelRatio,
		defaultParams.ProposalCancelDest,
		defaultParams.BurnProposalDepositPrevote,
		defaultParams.BurnVoteQuorum,
		defaultParams.BurnVoteVeto,
		defaultParams.MinDepositRatio,
	)

	if params.ExpeditedVotingPeriod != nil && params.VotingPeriod != nil {
		if *params.ExpeditedVotingPeriod >= *params.VotingPeriod {
			*params.ExpeditedVotingPeriod = *params.VotingPeriod / 2
		}
	}

	newGovState := govTypes.GenesisState{
		StartingProposalId: oldGovState.StartingProposalID,
		Deposits:           newDeposits,
		Votes:              newVotes,
		Proposals:          newProposals,
		Params:             &params,
		Constitution: "Heimdall chain serves as the coordination and consensus layer of the Polygon PoS protocol. " +
			"Its governance is responsible for managing validator sets, protocol upgrades, and system parameters critical to the network's security and liveness. " +
			"Decisions must be guided by principles of decentralization, transparency, and the long-term resilience of the Polygon ecosystem. " +
			"All governance participants are expected to act in good faith and in alignment with the collective interest of the network.",
	}

	newGovStateMarshaled := appCodec.MustMarshalJSON(&newGovState)

	genesisData["app_state"].(map[string]interface{})["gov"] = json.RawMessage(newGovStateMarshaled)

	logger.Info("Gov module migration completed successfully")

	return nil
}

// migrateBankModule fetches the auth accounts and migrates them to bank balances
// and fetches the total supply from the deprecated supply module and adds it to bank genesis.
func migrateBankModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating bank module...")

	bankModule, ok := genesisData["app_state"].(map[string]interface{})["bank"]
	if !ok {
		return fmt.Errorf("bank module not found in app_state")
	}

	bankData, ok := bankModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast bank module data")
	}

	authModule, ok := genesisData["app_state"].(map[string]interface{})["auth"]
	if !ok {
		return fmt.Errorf("auth module not found in app_state")
	}

	authData, ok := authModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast auth module data")
	}

	accounts, ok := authData["accounts"].([]interface{})
	if !ok {
		return fmt.Errorf("accounts not found or invalid format")
	}

	balances, err := utils.MigrateAuthAccountsToBankBalances(accounts)
	if err != nil {
		return err
	}

	sendEnabled, ok := bankData["send_enabled"].(bool)
	if !ok {
		return fmt.Errorf("send_enabled not found in bank module")
	}

	newBankGenesis := bankTypes.GenesisState{
		Params:        bankTypes.NewParams(sendEnabled),
		Balances:      balances,
		DenomMetadata: []bankTypes.Metadata{},
		SendEnabled:   []bankTypes.SendEnabled{},
	}

	marshaledGenesisState := appCodec.MustMarshalJSON(&newBankGenesis)

	genesisData["app_state"].(map[string]interface{})["bank"] = json.RawMessage(marshaledGenesisState)

	logger.Info("Bank module migration completed successfully")

	return nil
}

// migrateAuthModule converts the auth accounts into the new format
// and changes the type of some of the params from string to uint64.
func migrateAuthModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating auth module...")

	authModule, ok := genesisData["app_state"].(map[string]interface{})["auth"]
	if !ok {
		return fmt.Errorf("auth module not found in app_state")
	}

	authData, ok := authModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast auth module data")
	}

	baseAccounts, err := utils.MigrateAuthAccounts(authData)
	if err != nil {
		return err
	}

	paramsData, ok := authData["params"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("params not found in auth module")
	}

	newParams := authTypes.Params{
		MaxMemoCharacters: utils.ParseUint(paramsData["max_memo_characters"]),
		// Override tx_sig_limit to "1" explicitly to comply with new v2 params
		TxSigLimit:             authTypes.DefaultTxSigLimit,
		TxSizeCostPerByte:      utils.ParseUint(paramsData["tx_size_cost_per_byte"]),
		SigVerifyCostED25519:   utils.ParseUint(paramsData["sig_verify_cost_ed25519"]),
		SigVerifyCostSecp256k1: utils.ParseUint(paramsData["sig_verify_cost_secp256k1"]),
		MaxTxGas:               utils.ParseUint(paramsData["max_tx_gas"]),
		TxFees:                 paramsData["tx_fees"].(string),
	}

	genesisState := authTypes.GenesisState{
		Accounts: baseAccounts,
		Params:   newParams,
	}

	marshaledGenesisState := appCodec.MustMarshalJSON(&genesisState)

	genesisData["app_state"].(map[string]interface{})["auth"] = json.RawMessage(marshaledGenesisState)

	logger.Info("Auth module migration completed successfully")

	return nil
}

// migrateCheckpointModule converts checkpoint_buffer_time from string nanoseconds timestamp to seconds duration,
// renames child_chain_block_interval to child_block_interval and iterates over the checkpoints to convert the root_hash
// from hex to base64.
func migrateCheckpointModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating checkpoint module...")

	checkpointModule, ok := genesisData["app_state"].(map[string]interface{})["checkpoint"]
	if !ok {
		return fmt.Errorf("checkpoint module not found in app_state")
	}

	checkpointData, ok := checkpointModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast checkpoint module data")
	}

	params, ok := checkpointData["params"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("checkpoint params not found")
	}

	// Get the current checkpoint_buffer_time (which is in nanoseconds)
	checkpointBufferTimeStr, ok := params["checkpoint_buffer_time"].(string)
	if !ok {
		return fmt.Errorf("checkpoint_buffer_time not found or invalid format")
	}

	// Convert the checkpoint_buffer_time from string to int64 (nanoseconds)
	checkpointBufferTimeNs, err := strconv.ParseInt(checkpointBufferTimeStr, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse checkpoint_buffer_time: %w", err)
	}

	// Convert nanoseconds to time.Duration
	checkpointBufferTimeDuration := time.Duration(checkpointBufferTimeNs)

	// Convert the duration to a human-readable format (e.g., seconds)
	checkpointBufferTimeReadable := fmt.Sprintf("%ds", int64(checkpointBufferTimeDuration.Seconds()))

	// Update the checkpoint_buffer_time with the human-readable value
	params["checkpoint_buffer_time"] = checkpointBufferTimeReadable

	bufferedCheckpoint, ok := checkpointData["buffered_checkpoint"].(map[string]interface{})
	if ok {

		bufferedRootHashHex, ok := bufferedCheckpoint["root_hash"].(string)
		if !ok {
			return fmt.Errorf("root_hash not found in buffered_checkpoint")
		}

		if !strings.HasPrefix(bufferedRootHashHex, "0x") {
			return fmt.Errorf("invalid root_hash format in buffered_checkpoint")
		}

		bufferedRootHashHex = bufferedRootHashHex[2:]

		bufferedRootHashBytes, err := hex.DecodeString(bufferedRootHashHex)
		if err != nil {
			return fmt.Errorf("failed to decode buffered root_hash: %w", err)
		}

		bufferedRootHashBase64 := base64.StdEncoding.EncodeToString(bufferedRootHashBytes)
		bufferedCheckpoint["root_hash"] = bufferedRootHashBase64
	}

	checkpoints, ok := checkpointData["checkpoints"].([]interface{})
	if ok {
		utils.SortByTimestamp(checkpoints)

		for i, checkpoint := range checkpoints {
			checkpointMap, ok := checkpoint.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid checkpoint format at index %d", i)
			}

			rootHashHex, ok := checkpointMap["root_hash"].(string)
			if !ok {
				return fmt.Errorf("root_hash not found in checkpoint at index %d", i)
			}

			if !strings.HasPrefix(rootHashHex, "0x") {
				return fmt.Errorf("invalid root_hash format at index %d", i)
			}

			rootHashHex = rootHashHex[2:]

			rootHashBytes, err := hex.DecodeString(rootHashHex)
			if err != nil {
				return fmt.Errorf("failed to decode root_hash at index %d: %w", i, err)
			}

			rootHashBase64 := base64.StdEncoding.EncodeToString(rootHashBytes)

			checkpointMap["root_hash"] = rootHashBase64
		}
	}

	// Iterate over ack_count and assign checkpoint id
	ackCountStr, ok := checkpointData["ack_count"].(string)
	if !ok {
		return fmt.Errorf("ack_count not found or invalid in checkpoint module")
	}

	ackCount, err := strconv.Atoi(ackCountStr)
	if err != nil {
		return fmt.Errorf("failed to convert ack_count to integer: %w", err)
	}

	// ackCount should be equal to the number of checkpoints
	if ackCount != len(checkpoints) {
		return fmt.Errorf("ackCount (%d) does not match the number of checkpoints (%d)", ackCount, len(checkpoints))
	}

	for i := 0; i < len(checkpoints); i++ {
		checkpointMap, ok := checkpoints[i].(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid checkpoint format at index %d", i)
		}
		checkpointMap["id"] = strconv.Itoa(i + 1)
	}

	// assign id to bufferedCheckpoint if present
	if bufferedCheckpoint != nil {
		bufferedCheckpoint["id"] = strconv.Itoa(len(checkpoints) + 1)
	}

	emptyCheckpointSignatures := map[string]interface{}{
		"signatures": []interface{}{},
	}
	if err := utils.AddProperty(genesisData, "app_state.checkpoint", "checkpoint_signatures", emptyCheckpointSignatures); err != nil {
		return fmt.Errorf("failed to add empty checkpoint signatures to checkpoint module: %w", err)
	}

	emptyCheckpointSignaturesTxhash := ""
	if err := utils.AddProperty(genesisData, "app_state.checkpoint", "checkpoint_signatures_txhash", emptyCheckpointSignaturesTxhash); err != nil {
		return fmt.Errorf("failed to add empty checkpoint signatures tx hash to checkpoint module: %w", err)
	}

	logger.Info("Checkpoint module migration completed successfully")

	return nil
}

// migrateBorModule will iterate over the spans to migrate all the validators and proposers.
// It will also rename some fields to new names.
func migrateBorModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating bor module...")

	borModule, ok := genesisData["app_state"].(map[string]interface{})["bor"]
	if !ok {
		return fmt.Errorf("bor module not found in app_state")
	}

	borData, ok := borModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast bor module data")
	}

	spans, ok := borData["spans"].([]interface{})
	if !ok {
		return fmt.Errorf("failed to find spans in bor module")
	}

	for _, span := range spans {
		spanMap, ok := span.(map[string]interface{})
		if !ok {
			return fmt.Errorf("failed to cast span data")
		}

		if err := utils.RenameProperty(spanMap, ".", "span_id", "id"); err != nil {
			return fmt.Errorf("failed to rename bor_chain_id field: %w", err)
		}

		if err := utils.MigrateValidators(spanMap["selected_producers"]); err != nil {
			return fmt.Errorf("failed to migrate selected_producers in span: %w", err)
		}

		validatorSet, ok := spanMap["validator_set"].(map[string]interface{})
		if !ok {
			return fmt.Errorf("failed to find validator_set in span")
		}

		proposer, _ := validatorSet["proposer"].(map[string]interface{})
		if proposer != nil {
			if err := utils.MigrateValidator(proposer); err != nil {
				return fmt.Errorf("failed to migrate proposer: %w", err)
			}
		}

		if err := utils.MigrateValidators(validatorSet["validators"]); err != nil {
			return fmt.Errorf("failed to migrate validators in validator_set: %w", err)
		}
	}

	logger.Info("Bor module migration completed successfully")

	return nil
}

// migrateClerkModule will iterate over the event_records and convert the data field from hex to base64.
func migrateClerkModule(genesisData map[string]interface{}) error {
	logger.Info("Migrating clerk module...")

	clerkModule, ok := genesisData["app_state"].(map[string]interface{})["clerk"]
	if !ok {
		return fmt.Errorf("clerk module not found in app_state")
	}

	clerkData, ok := clerkModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast clerk module data")
	}

	eventRecords, ok := clerkData["event_records"]
	if !ok || eventRecords == nil {
		return fmt.Errorf("event_records not found in clerk module")
	}

	records, ok := eventRecords.([]interface{})
	if !ok {
		return fmt.Errorf("invalid event_records format")
	}

	for i, record := range records {
		recMap, ok := record.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid record format at index %d", i)
		}

		dataHex, ok := recMap["data"].(string)
		if !ok {
			return fmt.Errorf("data field not found or invalid at index %d", i)
		}

		dataHex = strings.TrimPrefix(dataHex, "0x")

		decodedData, err := hex.DecodeString(dataHex)
		if err != nil {
			return fmt.Errorf("failed to decode hex data at index %d: %w", i, err)
		}

		base64Data := base64.StdEncoding.EncodeToString(decodedData)

		recMap["data"] = base64Data
	}

	logger.Info("Clerk module migration completed successfully")

	return nil
}

// addMissingCometBFTConsensusParams adds consensus parameters that are missing in Tendermint but are required by CometBFT.
// The new values are being copied from genesis.json generated by CometBFT 0.38.5.
func addMissingCometBFTConsensusParams(genesisData map[string]interface{}, initialHeight uint64) error {
	logger.Info("Adding missing CometBFT consensus parameters...")

	if err := utils.AddProperty(genesisData, "consensus_params.evidence", "max_age_num_blocks", "100000"); err != nil {
		return err
	}

	if err := utils.AddProperty(genesisData, "consensus_params.evidence", "max_age_duration", "172800000000000"); err != nil {
		return err
	}

	if err := utils.AddProperty(genesisData, "consensus_params.evidence", "max_bytes", "10240"); err != nil {
		return err
	}

	if err := utils.AddProperty(genesisData, "consensus_params", "block", make(map[string]interface{})); err != nil {
		return err
	}

	if err := utils.AddProperty(genesisData, "consensus_params.block", "max_gas", "10000000"); err != nil {
		return err
	}

	if err := utils.AddProperty(genesisData, "consensus_params.block", "max_bytes", "2097152"); err != nil {
		return err
	}

	validatorParams := make(map[string]interface{})
	validatorParams["pub_key_types"] = []interface{}{"secp256k1"}
	if err := utils.AddProperty(genesisData, "consensus_params", "validator", validatorParams); err != nil {
		return err
	}

	versionParams := make(map[string]interface{})
	versionParams["app"] = "0"
	if err := utils.AddProperty(genesisData, "consensus_params", "version", versionParams); err != nil {
		return err
	}

	abciParams := make(map[string]interface{})
	abciParams["vote_extensions_enable_height"] = strconv.FormatUint(initialHeight, 10)
	if err := utils.AddProperty(genesisData, "consensus_params", "abci", abciParams); err != nil {
		return err
	}

	logger.Info("CometBFT consensus parameters added successfully")

	return nil
}

// removeUnusedTendermintConsensusParams removes consensus parameters that don't exist in CometBFT 0.38.5 genesis.
func removeUnusedTendermintConsensusParams(genesisData map[string]interface{}) error {
	logger.Info("Removing unused Tendermint consensus parameters...")

	if err := utils.DeleteProperty(genesisData, "consensus_params.evidence", "max_age"); err != nil {
		return err
	}

	logger.Info("Unused Tendermint consensus parameters removed successfully")

	return nil
}

func replaceMaticWithPol(v interface{}) {
	const matic = "matic"
	const pol = "pol"

	switch val := v.(type) {
	case map[string]interface{}:
		for key, item := range val {
			// If the value is exactly "matic", replace it
			if strVal, ok := item.(string); ok && strVal == matic {
				val[key] = pol
				continue
			}
			// Recurse
			replaceMaticWithPol(item)
		}
	case []interface{}:
		for i := range val {
			// If the element is exactly "matic", replace it
			if strVal, ok := val[i].(string); ok && strVal == matic {
				val[i] = pol
				continue
			}
			// Recurse
			replaceMaticWithPol(val[i])
		}
	}
}
