package verify

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	heimdallApp "github.com/0xPolygon/heimdall-v2/app"
	"github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/utils"
	hmTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// RunMigrationVerification verifies the migration from Heimdall v1 to Heimdall v2 by consuming the migrated genesis file
// and verifying balances, validators, bor spans, clerk events, and checkpoints
func RunMigrationVerification(hv1GenesisPath, hv2GenesisPath string, logger log.Logger) error {
	globalStart := time.Now()
	logger.Info("Verifying migration")

	db := dbm.NewMemDB()

	appOptions := make(simtestutil.AppOptionsMap)
	appOptions[flags.FlagHome] = heimdallApp.DefaultNodeHome

	app := heimdallApp.NewHeimdallApp(logger, db, nil, true, appOptions)

	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	genesisState, err := getGenesisAppState(hv2GenesisPath)
	if err != nil {
		return err
	}

	hv1Genesis, err := utils.LoadJSONFromFile(hv1GenesisPath)
	if err != nil {
		return err
	}

	logger.Info("Verifying modules' data lists")
	start := time.Now()
	if err := verifyDataLists(hv1GenesisPath, hv2GenesisPath, logger); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyDataLists took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Initializing genesis state")
	start = time.Now()
	if _, err := app.ModuleManager.InitGenesis(ctx, app.AppCodec(), genesisState); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("InitGenesis took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify event records")
	start = time.Now()
	if err := verifyClerkEventRecords(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyClerkEventRecords took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify spans")
	start = time.Now()
	if err := verifySpans(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifySpans took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify checkpoints")
	start = time.Now()
	if err := verifyCheckpoints(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyCheckpoints took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify milestones")
	start = time.Now()
	if err := verifyMilestones(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyMilestones took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify validators")
	start = time.Now()
	if err := verifyValidators(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyValidators took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify topup")
	start = time.Now()
	if err := verifyTopup(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyTopup took %.2f minutes", time.Since(start).Minutes()))

	logger.Info("Verify balances")
	start = time.Now()
	if err := verifyBalances(ctx, app, hv1Genesis); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("verifyBalances took %.2f minutes", time.Since(start).Minutes()))
	logger.Info("Migration verified successfully")
	logger.Info(fmt.Sprintf("performMigrations took %.2f minutes", time.Since(globalStart).Minutes()))

	return nil
}

// verifyBalances verifies the balances of all the accounts in the genesis file to the balances in the database
func verifyBalances(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	authModule, ok := hv1Genesis["app_state"].(map[string]interface{})["auth"]
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

	addressCoins := map[string]types.Coins{}

	for i, account := range accounts {
		accountMap, ok := account.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid account format at index %d", i)
		}

		addressRaw, ok := accountMap["address"]
		if !ok || addressRaw == nil {
			return fmt.Errorf("missing address for account at index %d", i)
		}
		accAddress, ok := addressRaw.(string)
		if !ok {
			return fmt.Errorf("address field is not a string at index %d", i)
		}

		coinsRaw := accountMap["coins"]
		var coins []interface{}
		if coinsRaw != nil {
			coins, ok = coinsRaw.([]interface{})
			if !ok {
				return fmt.Errorf("coins field is not a list at index %d", i)
			}
		}
		// If coinsRaw == nil ➔ coins == nil ➔ zero coins: OK

		for _, coin := range coins {
			coinMap, ok := coin.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid coin format at index %d", i)
			}

			// normalize "matic" to "pol"
			denom, _ := coinMap["denom"].(string)
			if denom == "matic" {
				denom = "pol"
			}
			amountStr, _ := coinMap["amount"].(string)

			amount, ok := math.NewIntFromString(amountStr)
			if !ok {
				return fmt.Errorf("failed to parse amount at index %d: %s", i, amountStr)
			}

			if coins, ok := addressCoins[accAddress]; ok {
				addressCoins[accAddress] = append(coins, types.NewCoin(denom, amount))
			} else {
				addressCoins[accAddress] = types.NewCoins(types.NewCoin(denom, amount))
			}
		}
	}

	dbBalances := app.BankKeeper.GetAccountsBalances(ctx)
	for _, balance := range dbBalances {
		if coins, ok := addressCoins[balance.Address]; ok {
			for _, coin := range coins {
				if !balance.Coins.AmountOf(coin.Denom).Equal(coin.Amount) {
					return fmt.Errorf("mismatch in balance for address %s: expected %s, got %s", balance.Address, coin, balance.Coins.AmountOf(coin.Denom))
				}
			}
		} else {
			return fmt.Errorf("balance not found for address %s", balance.Address)
		}
	}

	return nil
}

// verifyValidators verifies the validators in the genesis file to the validators in the database using basic validator info
func verifyValidators(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	stakingModule, ok := hv1Genesis["app_state"].(map[string]interface{})["staking"]
	if !ok {
		return fmt.Errorf("staking module not found in app_state")
	}

	stakingModuleData, ok := stakingModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast staking module data")
	}

	curValSetDB := app.StakeKeeper.GetCurrentValidators(ctx)

	currentValSet, ok := stakingModuleData["current_val_set"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("current_val_set not found or invalid format")
	}

	curValSetGenesis, ok := currentValSet["validators"].([]interface{})
	if !ok {
		return fmt.Errorf("current_val_set validators not found or invalid format")
	}

	if len(curValSetGenesis) > 0 {
		curValSetDbMap := map[string]hmTypes.Validator{}

		for _, valDB := range curValSetDB {
			curValSetDbMap[valDB.Signer] = valDB
		}

		for i, validator := range curValSetGenesis {
			basicValInfo, err := getValidatorBasicInfo(validator)
			if err != nil {
				return fmt.Errorf("failed to get validator basic info at index %d: %w", i, err)
			}

			validatorDB, ok := curValSetDbMap[basicValInfo.signer]
			if !ok {
				return fmt.Errorf("validator not found in current validator set database: %s", basicValInfo.signer)
			}

			if err := compareValidators(validatorDB, basicValInfo); err != nil {
				return fmt.Errorf("validator mismatch %s: %w", basicValInfo.signer, err)
			}

			delete(curValSetDbMap, basicValInfo.signer)
		}

		return nil
	}

	allValidatorsGenesis, ok := stakingModuleData["validators"].([]interface{})
	if !ok {
		return fmt.Errorf("validators not found or invalid format")
	}

	allValidatorsDB := app.StakeKeeper.GetAllValidators(ctx)

	validatorsDbMap := map[string]*hmTypes.Validator{}
	for _, valDB := range allValidatorsDB {
		validatorsDbMap[valDB.Signer] = valDB
	}

	for i, validator := range allValidatorsGenesis {
		basicValInfo, err := getValidatorBasicInfo(validator)
		if err != nil {
			return fmt.Errorf("failed to get validator basic info at index %d: %w", i, err)
		}

		validatorDB, ok := validatorsDbMap[basicValInfo.signer]
		if !ok {
			return fmt.Errorf("validator not found in database: %s", basicValInfo.signer)
		}

		if err := compareValidators(*validatorDB, basicValInfo); err != nil {
			return fmt.Errorf("validator mismatch %s: %w", basicValInfo.signer, err)
		}

		delete(validatorsDbMap, basicValInfo.signer)
	}

	return nil
}

// compareValidators compares the validator in the database to the validator in the genesis file based on basic info
func compareValidators(validatorDB hmTypes.Validator, validatorGenesis *validatorBasicInfo) error {
	if validatorDB.Signer != validatorGenesis.signer {
		return fmt.Errorf("mismatch in signer for validator %s: expected %s, got %s", validatorDB.Signer, validatorGenesis.signer, validatorDB.Signer)
	}

	if validatorDB.VotingPower != validatorGenesis.power {
		return fmt.Errorf("mismatch in power for validator %s: expected %d, got %d", validatorDB.Signer, validatorGenesis.power, validatorDB.VotingPower)
	}

	if validatorDB.Nonce != validatorGenesis.nonce {
		return fmt.Errorf("mismatch in nonce for validator %s: expected %d, got %d", validatorDB.Signer, validatorGenesis.nonce, validatorDB.Nonce)
	}

	if validatorDB.Jailed != validatorGenesis.jailed {
		return fmt.Errorf("mismatch in jailed status for validator %s: expected %t, got %t", validatorDB.Signer, validatorGenesis.jailed, validatorDB.Jailed)
	}

	return nil
}

// verifyDataLists checks list counts in v1 vs. v2 using potentially different keys.
func verifyDataLists(hv1Path, hv2Path string, logger log.Logger) error {
	type keyMapping struct {
		moduleV1 string
		keyV1    string
		moduleV2 string
		keyV2    string
	}
	keyMappings := []keyMapping{
		{"auth", "accounts", "auth", "accounts"},
		{"bor", "spans", "bor", "spans"},
		{"clerk", "event_records", "clerk", "event_records"},
		{"clerk", "record_sequences", "clerk", "record_sequences"},
		{"checkpoint", "checkpoints", "checkpoint", "checkpoints"},
		{"gov", "proposals", "gov", "proposals"},
		{"staking", "validators", "stake", "validators"},
		{"topup", "dividend_accounts", "topup", "dividend_accounts"},
		{"topup", "tx_sequences", "topup", "topup_sequences"},
	}

	for _, km := range keyMappings {
		count1, err := countJSONArrayEntries(hv1Path, km.moduleV1, km.keyV1)
		if err != nil {
			return fmt.Errorf("v1 %s.%s: %w", km.moduleV1, km.keyV1, err)
		}
		count2, err := countJSONArrayEntries(hv2Path, km.moduleV2, km.keyV2)
		if err != nil {
			return fmt.Errorf("v2 %s.%s: %w", km.moduleV2, km.keyV2, err)
		}
		if km.moduleV1 == "auth" && km.keyV1 == "accounts" {
			// in v1, the accounts also consider the module accounts, which are not present in v2
			if count1 < count2 {
				logger.Error("count mismatch",
					"v1_module", km.moduleV1, "v1_key", km.keyV1, "v1_count", count1,
					"v2_module", km.moduleV2, "v2_key", km.keyV2, "v2_count", count2)
				return fmt.Errorf("mismatch in auth accounts: %s.%s=%d > %s.%s=%d",
					km.moduleV1, km.keyV1, count1,
					km.moduleV2, km.keyV2, count2)
			}
		} else if km.moduleV1 == "staking" && km.keyV1 == "validators" {
			// in v1, the accounts also consider the module accounts, which are not present in v2
			if count1 < count2 {
				logger.Error("count mismatch",
					"v1_module", km.moduleV1, "v1_key", km.keyV1, "v1_count", count1,
					"v2_module", km.moduleV2, "v2_key", km.keyV2, "v2_count", count2)
				return fmt.Errorf("mismatch in auth accounts: %s.%s=%d > %s.%s=%d",
					km.moduleV1, km.keyV1, count1,
					km.moduleV2, km.keyV2, count2)
			}
		} else {
			if count1 != count2 {
				logger.Error("count mismatch",
					"v1_module", km.moduleV1, "v1_key", km.keyV1, "v1_count", count1,
					"v2_module", km.moduleV2, "v2_key", km.keyV2, "v2_count", count2)
				return fmt.Errorf("mismatch: %s.%s=%d ≠ %s.%s=%d",
					km.moduleV1, km.keyV1, count1,
					km.moduleV2, km.keyV2, count2)
			} else {
				fmt.Printf("found %d entries for key %s in module %s for v1 and %d entries for key %s in module %s in v2\n", count1, km.keyV1, km.moduleV1, count2, km.keyV2, km.moduleV2)
			}
		}
	}

	logger.Info("All streaming list comparisons passed")
	return nil
}

// verifyCheckpoints verifies the checkpoints in the genesis files by comparing the data in both versions
func verifyCheckpoints(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	hv1CheckpointData, ok := hv1Genesis["app_state"].(map[string]interface{})["checkpoint"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("checkpoint module not found in v1 app_state")
	}

	checkpointsRaw := hv1CheckpointData["checkpoints"]
	var checkpoints []interface{}
	if checkpointsRaw != nil {
		checkpoints, ok = checkpointsRaw.([]interface{})
		if !ok {
			return fmt.Errorf("checkpoints key is not a list")
		}
	}
	// If checkpointsRaw == nil → checkpoints will be nil → OK (zero checkpoints)

	utils.SortByStartBlock(checkpoints)

	// Get and sort V2 checkpoints by startBlock
	dbCheckpoints, err := app.CheckpointKeeper.GetCheckpoints(ctx)
	if err != nil {
		return fmt.Errorf("failed to get checkpoints from v2 database: %w", err)
	}
	sort.Slice(dbCheckpoints, func(i, j int) bool {
		return dbCheckpoints[i].StartBlock < dbCheckpoints[j].StartBlock
	})

	// Compare the checkpoints one by one
	if len(checkpoints) != len(dbCheckpoints) {
		return fmt.Errorf("number of checkpoints mismatch: v1 has %d, v2 has %d", len(checkpoints), len(dbCheckpoints))
	}

	for i := 0; i < len(dbCheckpoints); i++ {
		cp := checkpoints[i].(map[string]interface{})
		checkpoint := dbCheckpoints[i]

		startBlock, err := strconv.Atoi(cp["start_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert start_block to int: %w", err)
		}
		if int(checkpoint.StartBlock) != startBlock {
			return fmt.Errorf("mismatch in checkpoint start block at index %d: expected %d, got %d", i, startBlock, checkpoint.StartBlock)
		}

		endBlock, err := strconv.Atoi(cp["end_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert end_block to int: %w", err)
		}
		if int(checkpoint.EndBlock) != endBlock {
			return fmt.Errorf("mismatch in checkpoint end block at index %d: expected %d, got %d", i, endBlock, checkpoint.EndBlock)
		}

		if checkpoint.Proposer != cp["proposer"].(string) {
			return fmt.Errorf("mismatch in checkpoint proposer at index %d: expected %s, got %s", i, cp["proposer"], checkpoint.Proposer)
		}

		rootHashBytes, err := hex.DecodeString(cp["root_hash"].(string)[2:])
		if err != nil {
			return fmt.Errorf("failed to decode root_hash at index %d: %w", i, err)
		}
		if !bytes.Equal(checkpoint.RootHash, rootHashBytes) {
			return fmt.Errorf("mismatch in checkpoint root hash at index %d: expected %x, got %x", i, rootHashBytes, checkpoint.RootHash)
		}
	}

	// Ensure ack_count is consistent
	hv1AckCountStr, ok := hv1CheckpointData["ack_count"].(string)
	if !ok {
		return fmt.Errorf("ack_count not found or invalid in v1")
	}
	hv1AckCount, err := strconv.Atoi(hv1AckCountStr)
	if err != nil {
		return fmt.Errorf("failed to convert v1 ack_count to integer: %w", err)
	}

	dbAckCount, err := app.CheckpointKeeper.GetAckCount(ctx)
	if err != nil {
		return fmt.Errorf("failed to get ack_count from v2 database: %w", err)
	}

	if hv1AckCount != int(dbAckCount) {
		return fmt.Errorf("mismatch in ack_count: v1 has %d, v2 has %d", hv1AckCount, dbAckCount)
	}

	// Check V2 ordering and sequential ID
	for i := 1; i < len(dbCheckpoints); i++ {
		if dbCheckpoints[i-1].StartBlock >= dbCheckpoints[i].StartBlock {
			return fmt.Errorf("checkpoints in v2 are not ordered by growing start_block at index %d", i)
		}
		if int(dbCheckpoints[i].Id) != i+1 {
			return fmt.Errorf("checkpoints in v2 have non-sequential IDs at index %d", i)
		}
	}

	return nil
}

// verifySpans verifies the bor data in the genesis files by comparing the data in both versions
func verifySpans(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	hv1SpansData, ok := hv1Genesis["app_state"].(map[string]interface{})["bor"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("bor module not found in v1 app_state")
	}

	spansRaw := hv1SpansData["spans"]
	var spans []interface{}
	if spansRaw != nil {
		spans, ok = spansRaw.([]interface{})
		if !ok {
			return fmt.Errorf("spans key is not a list")
		}
	}
	// If spansRaw == nil → spans stays nil → OK (zero spans)

	// Index v1 spans by span_id
	v1SpansByID := make(map[int]map[string]interface{})
	for _, s := range spans {
		m, ok := s.(map[string]interface{})
		if !ok {
			return fmt.Errorf("span is not a valid map")
		}
		idStr, ok := m["span_id"].(string)
		if !ok {
			return fmt.Errorf("span_id is not a string")
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return fmt.Errorf("failed to convert span_id to int: %w", err)
		}
		v1SpansByID[id] = m
	}

	// Load spans from DB
	dbSpans, err := app.BorKeeper.GetAllSpans(ctx)
	if err != nil {
		return fmt.Errorf("failed to get spans from v2 database: %w", err)
	}

	// Compare each db span to v1 span by ID
	for i, span := range dbSpans {
		v1Span, ok := v1SpansByID[int(span.Id)]
		if !ok {
			return fmt.Errorf("span with ID %d not found in v1 genesis", span.Id)
		}

		startBlock, err := strconv.Atoi(v1Span["start_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert start_block to int: %w", err)
		}
		if int(span.StartBlock) != startBlock {
			return fmt.Errorf("start_block mismatch for span ID %d: expected %d, got %d", span.Id, startBlock, span.StartBlock)
		}

		endBlock, err := strconv.Atoi(v1Span["end_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert end_block to int: %w", err)
		}
		if int(span.EndBlock) != endBlock {
			return fmt.Errorf("end_block mismatch for span ID %d: expected %d, got %d", span.Id, endBlock, span.EndBlock)
		}

		// Check sequential and ordered spans
		if i > 0 {
			if dbSpans[i-1].StartBlock >= span.StartBlock {
				return fmt.Errorf("spans in v2 are not ordered by growing start_block at index %d", i)
			}
		}
		if int(span.Id) != i {
			return fmt.Errorf("spans in v2 have non-sequential IDs at index %d: expected %d, got %d", i, i, span.Id)
		}
	}

	// Ensure no extra spans exist in v1
	if len(v1SpansByID) != len(dbSpans) {
		return fmt.Errorf("span count mismatch: v1 has %d, v2 has %d", len(v1SpansByID), len(dbSpans))
	}

	return nil
}

// verifyTopup verifies the topup data in the genesis files by comparing the data in both versions
func verifyTopup(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	hv1TopupData, ok := hv1Genesis["app_state"].(map[string]interface{})["topup"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("topup module not found in v1 app_state")
	}

	dividendAccountsRaw := hv1TopupData["dividend_accounts"]
	var dividendAccounts []interface{}
	if dividendAccountsRaw != nil {
		dividendAccounts, ok = dividendAccountsRaw.([]interface{})
		if !ok {
			return fmt.Errorf("dividend accounts key not a list")
		}
	}

	txSequencesRaw := hv1TopupData["tx_sequences"]
	var txSequences []interface{}
	if txSequencesRaw != nil {
		txSequences, ok = txSequencesRaw.([]interface{})
		if !ok {
			return fmt.Errorf("tx sequences key not a list")
		}
	}
	// If txSequencesRaw == nil, txSequences will stay nil -> treated as empty!

	dbDividendAccounts, err := app.TopupKeeper.GetAllDividendAccounts(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dividend accounts from v2 database: %w", err)
	}
	if len(dbDividendAccounts) != len(dividendAccounts) {
		return fmt.Errorf("mismatch in topup dividend accounts: expected %d, got %d", len(dividendAccounts), len(dbDividendAccounts))
	}

	dbSequences, err := app.TopupKeeper.GetAllTopupSequences(ctx)
	if err != nil {
		return fmt.Errorf("failed to get topup sequences from v2 database: %w", err)
	}
	if len(dbSequences) != len(txSequences) {
		return fmt.Errorf("mismatch in topup sequences: expected %d, got %d", len(txSequences), len(dbSequences))
	}

	return nil
}

// verifyClerkEventRecords verifies the clerk event records in the genesis files by comparing the data in both versions
func verifyClerkEventRecords(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	hv1ClerkData, ok := hv1Genesis["app_state"].(map[string]interface{})["clerk"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("clerk module not found in v1 app_state")
	}

	recordsRaw := hv1ClerkData["event_records"]
	var records []interface{}
	if recordsRaw != nil {
		records, ok = recordsRaw.([]interface{})
		if !ok {
			return fmt.Errorf("event_records key is not a list")
		}
	}
	// If recordsRaw == nil → records is nil → OK, treated as zero records

	dbRecords := app.ClerkKeeper.GetAllEventRecords(ctx)

	sort.SliceStable(dbRecords, func(i, j int) bool {
		if !dbRecords[i].RecordTime.Equal(dbRecords[j].RecordTime) {
			return dbRecords[i].RecordTime.Before(dbRecords[j].RecordTime)
		}
		return dbRecords[i].Id < dbRecords[j].Id
	})

	v1RecordsByID := make(map[int]map[string]interface{})
	for _, r := range records {
		m, ok := r.(map[string]interface{})
		if !ok {
			return fmt.Errorf("event record is not a valid map")
		}
		idStr, ok := m["id"].(string)
		if !ok {
			return fmt.Errorf("id is not a string in record")
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return fmt.Errorf("failed to convert id to int: %w", err)
		}
		v1RecordsByID[id] = m
	}

	for _, record := range dbRecords {
		v1Record, ok := v1RecordsByID[int(record.Id)]
		if !ok {
			return fmt.Errorf("record with ID %d not found in v1 data", record.Id)
		}

		if record.TxHash != v1Record["tx_hash"].(string) {
			return fmt.Errorf("tx hash mismatch for ID %d: expected %s, got %s", record.Id, v1Record["tx_hash"], record.TxHash)
		}
		if record.Contract != v1Record["contract"].(string) {
			return fmt.Errorf("contract mismatch for ID %d: expected %s, got %s", record.Id, v1Record["contract"], record.Contract)
		}
		dataBytes, err := hex.DecodeString(v1Record["data"].(string)[2:])
		if err != nil {
			return fmt.Errorf("failed to decode event record data: %w", err)
		}
		if !bytes.Equal(record.Data, dataBytes) {
			return fmt.Errorf("mismatch in event record data: expected %s, got %s", dataBytes, record.Data)
		}
	}

	// ensure events are ordered by growing record time
	for i := 1; i < len(dbRecords); i++ {
		if dbRecords[i-1].RecordTime.After(dbRecords[i].RecordTime) {
			return fmt.Errorf("records not ordered correctly at index %d", i)
		}
	}

	// just log if IDs are not sequential
	var nonSequential []uint64
	for i := 1; i < len(dbRecords); i++ {
		expected := dbRecords[i-1].Id + 1
		if dbRecords[i].Id != expected {
			nonSequential = append(nonSequential, dbRecords[i].Id)
		}
	}
	if len(nonSequential) > 0 {
		fmt.Printf("Found %d non-sequential IDs after sorting dbRecords by RecordTime.\n", len(nonSequential))
	} else {
		fmt.Println("All dbRecord IDs are strictly sequential after sorting by RecordTime.")
	}

	sort.SliceStable(dbRecords, func(i, j int) bool {
		return dbRecords[i].Id < dbRecords[j].Id
	})

	nonChronoCount := 0
	for i := 1; i < len(dbRecords); i++ {
		if dbRecords[i].RecordTime.Before(dbRecords[i-1].RecordTime) {
			nonChronoCount++
		}
	}

	if nonChronoCount > 0 {
		fmt.Printf("Found %d record_time violations after sorting dbRecords by ID.\n", nonChronoCount)
	} else {
		fmt.Println("All dbRecord record_times are strictly increasing after sorting by ID.")
	}

	return nil
}

// getGenesisAppState reads the genesis file and returns the app state
func getGenesisAppState(hv2GenesisPath string) (heimdallApp.GenesisState, error) {
	genDoc, err := cmttypes.GenesisDocFromFile(hv2GenesisPath)
	if err != nil {
		return nil, err
	}

	var genesisState heimdallApp.GenesisState
	if err := json.Unmarshal(genDoc.AppState, &genesisState); err != nil {
		return nil, err
	}

	return genesisState, nil
}

// countJSONArrayEntries streams through a genesis file to count entries in a nested array.
// If the array is not found or is null, it assumes zero entries instead of returning an error.
func countJSONArrayEntries(path, module, key string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			fmt.Printf("Error closing file %s: %v\n", path, err)
		}
	}(f)

	dec := json.NewDecoder(f)
	inAppState, inModule := false, false
	var depth int

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			return 0, fmt.Errorf("json stream error: %w", err)
		}

		if keyStr, ok := tok.(string); ok {
			if !inAppState && keyStr == "app_state" {
				inAppState = true
				continue
			}
			if inAppState && !inModule && keyStr == module {
				inModule = true
				continue
			}
			if inAppState && inModule && keyStr == key {
				// Instead of dec.Token(), decode the full value
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return 0, fmt.Errorf("expected value after key %s: %w", key, err)
				}
				if len(raw) == 0 || string(raw) == "null" {
					// Null or empty
					return 0, nil
				}
				if raw[0] != '[' {
					// Not an array
					return 0, nil
				}

				// It's an array → count elements
				arrayDec := json.NewDecoder(bytes.NewReader(raw))
				tok, err := arrayDec.Token()
				if err != nil || tok != json.Delim('[') {
					return 0, fmt.Errorf("invalid array structure at %s.%s", module, key)
				}

				count := 0
				for arrayDec.More() {
					var discard json.RawMessage
					if err := arrayDec.Decode(&discard); err != nil {
						return 0, fmt.Errorf("failed to decode item in %s.%s: %w", module, key, err)
					}
					count++
				}
				return count, nil
			}
		}

		// Track depth
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				depth++
			case '}':
				depth--
				if inModule && depth == 2 {
					inModule = false
				}
				if inAppState && depth == 1 {
					inAppState = false
				}
			}
		}
	}

	// Key not found
	return 0, nil
}

// getValidatorBasicInfo extracts the basic info of a validator from the genesis file
func getValidatorBasicInfo(validator interface{}) (*validatorBasicInfo, error) {
	validatorMap, ok := validator.(map[string]interface{})
	if !ok {
		return nil, errors.New("invalid validator format")
	}

	jailed, ok := validatorMap["jailed"].(bool)
	if !ok {
		return nil, errors.New("jailed not found or invalid format")
	}

	signer, ok := validatorMap["signer"].(string)
	if !ok {
		return nil, errors.New("signer not found or invalid format")
	}

	powerStr, ok := validatorMap["power"].(string)
	if !ok {
		return nil, errors.New("power not found or invalid format")
	}

	nonceStr, ok := validatorMap["nonce"].(string)
	if !ok {
		return nil, errors.New("nonce not found or invalid format")
	}

	power, err := strconv.ParseInt(powerStr, 10, 64)
	if err != nil {
		return nil, err
	}

	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		return nil, err
	}

	return &validatorBasicInfo{
		power:  power,
		signer: signer,
		nonce:  nonce,
		jailed: jailed,
	}, nil
}

// validatorBasicInfo contains the basic info of a validator
type validatorBasicInfo struct {
	power  int64
	signer string
	nonce  uint64
	jailed bool
}

// verifyMilestones verifies the milestones in the genesis files by comparing the data in both versions
func verifyMilestones(ctx types.Context, app *heimdallApp.HeimdallApp, hv1Genesis map[string]interface{}) error {
	checkpointModule, ok := hv1Genesis["app_state"].(map[string]interface{})["checkpoint"]
	if !ok {
		return fmt.Errorf("checkpoint module not found in app_state")
	}

	checkpointData, ok := checkpointModule.(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to cast checkpoint module data")
	}

	milestonesRaw := checkpointData["milestones"]
	var milestones []interface{}
	if milestonesRaw != nil {
		milestones, ok = milestonesRaw.([]interface{})
		if !ok {
			return fmt.Errorf("milestones key not a list")
		}
	}

	utils.SortByStartBlock(milestones)

	// Get and sort V2 milestones by startBlock
	dbMilestones, err := app.MilestoneKeeper.GetMilestones(ctx)
	if err != nil {
		return fmt.Errorf("failed to get milestones from v2 database: %w", err)
	}
	sort.Slice(dbMilestones, func(i, j int) bool {
		return dbMilestones[i].StartBlock < dbMilestones[j].StartBlock
	})

	// Compare the milestones one by one
	if len(milestones) != len(dbMilestones) {
		return fmt.Errorf("number of milestones mismatch: v1 has %d, v2 has %d", len(milestones), len(dbMilestones))
	}

	for i := 0; i < len(dbMilestones); i++ {
		milestone := milestones[i].(map[string]interface{})
		dbMilestone := dbMilestones[i]

		startBlock, err := strconv.Atoi(milestone["start_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert start_block to int: %w", err)
		}
		if int(dbMilestone.StartBlock) != startBlock {
			return fmt.Errorf("mismatch in milestone start block at index %d: expected %d, got %d", i, startBlock, dbMilestone.StartBlock)
		}

		endBlock, err := strconv.Atoi(milestone["end_block"].(string))
		if err != nil {
			return fmt.Errorf("failed to convert end_block to int: %w", err)
		}
		if int(dbMilestone.EndBlock) != endBlock {
			return fmt.Errorf("mismatch in milestone end block at index %d: expected %d, got %d", i, endBlock, dbMilestone.EndBlock)
		}

		if dbMilestone.Proposer != milestone["proposer"].(string) {
			return fmt.Errorf("mismatch in milestone proposer at index %d: expected %s, got %s", i, milestone["proposer"], dbMilestone.Proposer)
		}

		hashBytes, err := hex.DecodeString(milestone["hash"].(string)[2:])
		if err != nil {
			return fmt.Errorf("failed to decode root_hash at index %d: %w", i, err)
		}
		if !bytes.Equal(dbMilestone.Hash, hashBytes) {
			return fmt.Errorf("mismatch in milestone root hash at index %d: expected %x, got %x", i, hashBytes, dbMilestone.Hash)
		}

		if dbMilestone.MilestoneId != milestone["milestone_id"].(string) {
			return fmt.Errorf("mismatch in milestone id at index %d: expected %s, got %s", i, milestone["milestone_id"], dbMilestone.MilestoneId)
		}

		// validate that milestoneID is composed by `UUID - HexAddressOfTheProposer`
		splitMilestoneID := strings.Split(dbMilestone.MilestoneId, " - ")
		if len(splitMilestoneID) != 2 {
			return fmt.Errorf("invalid milestoneID %s, it should be composed by `UUID - HexAddressOfTheProposer`", splitMilestoneID)
		}

		_, err = uuid.Parse(splitMilestoneID[0])
		if err != nil {
			return fmt.Errorf("invalid milestoneID, first part is not a valid uuid %s", splitMilestoneID[0])
		}

		if !common.IsHexAddress(splitMilestoneID[1]) {
			return fmt.Errorf("invalid milestoneID, second part is not a valid address %s", splitMilestoneID[1])
		}
	}

	// Check V2 ordering and sequential ID
	for i := 1; i < len(dbMilestones); i++ {
		if dbMilestones[i-1].StartBlock >= dbMilestones[i].StartBlock {
			return fmt.Errorf("milestones in v2 are not ordered by growing start_block at index %d", i)
		}
	}

	return nil
}
