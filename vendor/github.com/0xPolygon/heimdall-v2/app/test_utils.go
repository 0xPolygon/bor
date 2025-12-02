package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcrypto "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtTypes "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

const (
	VoteExtBlockHeight = 2
	CurrentHeight      = 3
	TxHash1            = "000000000000000000000000000000000000000000000000000000000001dead"
	TxHash2            = "000000000000000000000000000000000000000000000000000000000002dead"
	TxHash3            = "000000000000000000000000000000000000000000000000000000000003dead"
	ValAddr1           = "0x000000000000000000000000000000000001dEaD"
	ValAddr2           = "0x000000000000000000000000000000000002dEaD"
	ValAddr3           = "0x000000000000000000000000000000000003dEaD"
)

type SetupAppResult struct {
	App           *HeimdallApp
	DB            *dbm.MemDB
	Logger        log.Logger
	ValidatorKeys []cmtcrypto.PrivKey
}

func SetupApp(t *testing.T, numOfVals uint64) SetupAppResult {
	t.Helper()

	// generate validators, accounts and balances
	validatorPrivKeys, validators, accounts, balances := generateValidators(t, numOfVals)

	// setup app with validator set and respective accounts
	app, db, logger, privKeys := setupAppWithValidatorSet(t, validatorPrivKeys, validators, accounts, balances)

	return SetupAppResult{
		App:           app,
		DB:            db,
		Logger:        logger,
		ValidatorKeys: privKeys,
	}
}

func generateValidators(t *testing.T, numOfVals uint64) ([]cmtcrypto.PrivKey, []*stakeTypes.Validator, []authtypes.GenesisAccount, []banktypes.Balance) {
	t.Helper()

	validatorPrivKeys := make([]cmtcrypto.PrivKey, 0, numOfVals)
	validators := make([]*stakeTypes.Validator, 0, numOfVals)
	accounts := make([]authtypes.GenesisAccount, 0, numOfVals)
	balances := make([]banktypes.Balance, 0, numOfVals)

	var i uint64
	for ; i < numOfVals; i++ {
		privKey := cmtcrypto.GenPrivKey()
		pubKey := privKey.PubKey()
		pk, err := cryptocodec.FromCmtPubKeyInterface(pubKey)
		if err != nil {
			_ = fmt.Errorf("failed to convert pubkey: %w", err)
		}

		// create validator set
		val, _ := stakeTypes.NewValidator(i, 0, 0, i, 100, pk, pubKey.Address().String())

		validatorPrivKeys = append(validatorPrivKeys, privKey)
		validators = append(validators, val)

		senderPubKey := secp256k1.GenPrivKey().PubKey()
		acc := authtypes.NewBaseAccount(senderPubKey.Address().Bytes(), senderPubKey, i+1, 0) // fee_collector is the first initialized (module) account (AccountNumber = 0)
		balance := banktypes.Balance{
			Address: acc.GetAddress().String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, sdkmath.NewInt(100000000000000))),
		}

		accounts = append(accounts, acc)
		balances = append(balances, balance)
	}

	return validatorPrivKeys, validators, accounts, balances
}

func setupAppWithValidatorSet(t *testing.T, validatorPrivKeys []cmtcrypto.PrivKey, validators []*stakeTypes.Validator, accounts []authtypes.GenesisAccount, balances []banktypes.Balance, testOpts ...*helper.TestOpts) (*HeimdallApp, *dbm.MemDB, log.Logger, []cmtcrypto.PrivKey) {
	t.Helper()

	db := dbm.NewMemDB()

	appOptions := make(simtestutil.AppOptionsMap)
	appOptions[flags.FlagHome] = DefaultNodeHome

	logger := log.NewTestLogger(t)
	app := NewHeimdallApp(logger, db, nil, true, appOptions)
	genesisState := app.DefaultGenesis()

	// initialize validator set
	valSet := stakeTypes.NewValidatorSet(validators)

	genesisState, err := GenesisStateWithValSet(app.AppCodec(), genesisState, valSet, accounts, balances...)
	require.NoError(t, err)

	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)

	// initialize chain with the validator set and genesis accounts
	req := &abci.RequestInitChain{
		Validators:      []abci.ValidatorUpdate{},
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   stateBytes,
		InitialHeight:   VoteExtBlockHeight,
	}
	if len(testOpts) > 0 && testOpts[0] != nil {
		req.ChainId = testOpts[0].GetChainId()
	}

	helper.SetTestInitialHeight(VoteExtBlockHeight)

	_, err = app.InitChain(req)
	require.NoError(t, err)

	vals := make([]stakeTypes.Validator, 0, len(validators))
	for _, val := range validators {
		vals = append(vals, *val)
	}

	requestFinalizeBlock(t, app, VoteExtBlockHeight, vals)

	_, err = app.Commit()
	require.NoError(t, err)

	return app, db, logger, validatorPrivKeys
}

func RequestFinalizeBlock(t *testing.T, app *HeimdallApp, height int64) {
	t.Helper()
	validators := app.StakeKeeper.GetCurrentValidators(app.NewContext(true))
	requestFinalizeBlock(t, app, height, validators)
}

func requestFinalizeBlock(t *testing.T, app *HeimdallApp, height int64, validators []stakeTypes.Validator) {
	t.Helper()
	dummyExt, err := GetDummyNonRpVoteExtension(height, app.ChainID())
	require.NoError(t, err)
	consolidatedSideTxRes := sidetxs.VoteExtension{
		SideTxResponses: []sidetxs.SideTxResponse{},
		Height:          height - 1,
	}

	txResExt, err := consolidatedSideTxRes.Marshal()
	require.NoError(t, err)

	extCommitInfo := new(abci.ExtendedCommitInfo)
	extCommitInfo.Votes = make([]abci.ExtendedVoteInfo, 0)
	for _, validator := range validators {
		extCommitInfo.Votes = append(extCommitInfo.Votes, abci.ExtendedVoteInfo{
			VoteExtension:      txResExt,
			NonRpVoteExtension: dummyExt,
			BlockIdFlag:        cmtTypes.BlockIDFlagCommit,
			Validator: abci.Validator{
				Address: common.FromHex(validator.Signer),
				Power:   validator.VotingPower,
			},
		})
	}
	commitInfo, err := extCommitInfo.Marshal()
	require.NoError(t, err)
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Txs:             [][]byte{commitInfo},
		Height:          height,
		ProposerAddress: common.FromHex(validators[0].Signer),
	})
	require.NoError(t, err)
}

func RequestFinalizeBlockWithTxs(t *testing.T, app *HeimdallApp, height int64, txs ...[]byte) *abci.ResponseFinalizeBlock {
	t.Helper()
	extCommitInfo := new(abci.ExtendedCommitInfo)
	commitInfo, err := extCommitInfo.Marshal()
	require.NoError(t, err)
	allTxs := [][]byte{commitInfo}
	allTxs = append(allTxs, txs...)
	res, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Txs:    allTxs,
		Height: height,
	})
	require.NoError(t, err)
	return res
}

func mustMarshalSideTxResponses(t *testing.T, respVotes ...[]sidetxs.SideTxResponse) []byte {
	t.Helper()
	responses := make([]sidetxs.SideTxResponse, 0)
	for _, r := range respVotes {
		responses = append(responses, r...)
	}

	sideTxResponses := sidetxs.VoteExtension{
		SideTxResponses: responses,
		Height:          VoteExtBlockHeight,
	}

	voteExtension, err := sideTxResponses.Marshal()
	require.NoError(t, err)
	return voteExtension
}

func createSideTxResponses(vote sidetxs.Vote, txHashes ...string) []sidetxs.SideTxResponse {
	responses := make([]sidetxs.SideTxResponse, len(txHashes))
	for i, txHash := range txHashes {
		responses[i] = sidetxs.SideTxResponse{
			TxHash: common.FromHex(txHash),
			Result: vote,
		}
	}
	return responses
}

// GenesisStateWithValSet returns a new genesis state with the validator set
func GenesisStateWithValSet(codec codec.Codec, genesisState map[string]json.RawMessage, valSet *stakeTypes.ValidatorSet, genAccs []authtypes.GenesisAccount, balances ...banktypes.Balance) (map[string]json.RawMessage, error) {
	// set genesis accounts
	authGenesis := authtypes.NewGenesisState(authtypes.DefaultParams(), genAccs)
	genesisState[authtypes.ModuleName] = codec.MustMarshalJSON(authGenesis)

	validators := make([]*stakeTypes.Validator, 0, len(valSet.Validators))
	seqs := make([]string, 0, len(valSet.Validators))

	for i, val := range valSet.Validators {

		validator := stakeTypes.Validator{
			ValId:       uint64(i),
			StartEpoch:  0,
			EndEpoch:    0,
			Nonce:       uint64(i),
			VotingPower: 100,
			PubKey:      val.PubKey,
			Signer:      val.Signer,
			LastUpdated: time.Now().String(),
		}

		validators = append(validators, &validator)

		// Generate a secure random integer between 1 and 1,000,000
		n, err := helper.SecureRandomInt(1, 1000000)
		if err != nil {
			return nil, fmt.Errorf("failed to generate secure random number: %w", err)
		}

		seqs = append(seqs, strconv.FormatInt(n, 10))
	}

	// set validators and delegations
	stakingGenesis := stakeTypes.NewGenesisState(validators, *valSet, seqs)
	genesisState[stakeTypes.ModuleName] = codec.MustMarshalJSON(stakingGenesis)

	totalSupply := sdk.NewCoins()
	for _, b := range balances {
		// add genesis acc tokens to total supply
		totalSupply = totalSupply.Add(b.Coins...)
	}

	// update total supply
	bankGenesis := banktypes.NewGenesisState(banktypes.DefaultGenesisState().Params, balances, totalSupply, []banktypes.Metadata{}, []banktypes.SendEnabled{})
	genesisState[banktypes.ModuleName] = codec.MustMarshalJSON(bankGenesis)

	return genesisState, nil
}
