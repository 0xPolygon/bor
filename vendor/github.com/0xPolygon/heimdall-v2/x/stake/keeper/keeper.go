package keeper

import (
	"context"

	"cosmossdk.io/collections"
	addresscodec "cosmossdk.io/core/address"
	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/helper"
	cmKeeper "github.com/0xPolygon/heimdall-v2/x/chainmanager/keeper"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// Keeper stores all stake related data
type Keeper struct {
	cdc          codec.BinaryCodec
	storeService storetypes.KVStoreService
	schema       collections.Schema

	checkpointKeeper      types.CheckpointKeeper
	bankKeeper            types.BankKeeper
	cmKeeper              cmKeeper.Keeper
	validatorAddressCodec addresscodec.Codec
	contractCaller        helper.IContractCaller

	validators   collections.Map[string, types.Validator]
	validatorSet collections.Map[[]byte, types.ValidatorSet]
	signer       collections.Map[uint64, string]
	sequences    collections.Map[string, bool]
	lastBlockTxs collections.Item[types.LastBlockTxs]

	setupComplete bool
}

// NewKeeper creates a new stake Keeper instance
func NewKeeper(
	cdc codec.BinaryCodec,
	storeService storetypes.KVStoreService,
	bankKeeper types.BankKeeper,
	cmKeeper cmKeeper.Keeper,
	validatorAddressCodec addresscodec.Codec,
	contractCaller helper.IContractCaller,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService:          storeService,
		cdc:                   cdc,
		bankKeeper:            bankKeeper,
		cmKeeper:              cmKeeper,
		validatorAddressCodec: validatorAddressCodec,
		contractCaller:        contractCaller,

		validators:   collections.NewMap(sb, types.ValidatorsKey, "validator", collections.StringKey, codec.CollValue[types.Validator](cdc)),
		validatorSet: collections.NewMap(sb, types.ValidatorSetKey, "validator_set", collections.BytesKey, codec.CollValue[types.ValidatorSet](cdc)),
		sequences:    collections.NewMap(sb, types.StakeSequenceKey, "stake_sequence", collections.StringKey, collections.BoolValue),
		signer:       collections.NewMap(sb, types.SignerKey, "signer", collections.Uint64Key, collections.StringValue),
		lastBlockTxs: collections.NewItem(sb, types.LastBlockTxsKey, "last_block_txs", codec.CollValue[types.LastBlockTxs](cdc)),

		setupComplete: false,
	}

	// build the schema and set it in the keeper
	s, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.schema = s

	return k
}

// Logger returns a module-specific logger.
func (k Keeper) Logger(ctx context.Context) log.Logger {
	k.PanicIfSetupIsIncomplete()
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+types.ModuleName)
}

// SetLastBlockTxs sets the last block's txs in the store
func (k Keeper) SetLastBlockTxs(ctx context.Context, txs [][]byte) error {
	k.PanicIfSetupIsIncomplete()
	blockTxs := types.LastBlockTxs{Txs: txs}
	return k.lastBlockTxs.Set(ctx, blockTxs)
}

// GetLastBlockTxs gets the last block's txs from the store
func (k Keeper) GetLastBlockTxs(ctx context.Context) (types.LastBlockTxs, error) {
	k.PanicIfSetupIsIncomplete()
	return k.lastBlockTxs.Get(ctx)
}

// SetCheckpointKeeper sets the checkpoint keeper in the stake keeper
// This solves the circular dependency between the two keepers
// The setupComplete flag is set to true after the checkpoint keeper is set, otherwise all the functions will panic
func (k *Keeper) SetCheckpointKeeper(checkpointKeeper types.CheckpointKeeper) {
	k.checkpointKeeper = checkpointKeeper
	k.setupComplete = true
}

// PanicIfSetupIsIncomplete panics if the setup is incomplete, meaning that the checkpointKeeper is not set
func (k *Keeper) PanicIfSetupIsIncomplete() {
	if !k.setupComplete {
		panic("stake keeper is not yet ready, checkpoint keeper is missing")
	}
}
