package keeper

import (
	"context"
	"errors"
	"math/big"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
	"github.com/0xPolygon/heimdall-v2/helper"
	hTypes "github.com/0xPolygon/heimdall-v2/types"
	"github.com/0xPolygon/heimdall-v2/x/topup/types"
)

// Keeper stores all topup related data
type Keeper struct {
	cdc          codec.BinaryCodec
	storeService store.KVStoreService
	schema       collections.Schema

	BankKeeper     types.BankKeeper
	ChainKeeper    types.ChainKeeper
	contractCaller helper.IContractCaller

	sequences        collections.KeySet[string]
	dividendAccounts collections.Map[string, hTypes.DividendAccount]
}

// NewKeeper creates a new x/topup keeper
func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	bankKeeper types.BankKeeper,
	chainKeeper types.ChainKeeper,
	contractCaller helper.IContractCaller,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		cdc:            cdc,
		storeService:   storeService,
		BankKeeper:     bankKeeper,
		ChainKeeper:    chainKeeper,
		contractCaller: contractCaller,

		sequences:        collections.NewKeySet(sb, types.TopupSequencePrefixKey, "topup_sequence", collections.StringKey),
		dividendAccounts: collections.NewMap(sb, types.DividendAccountMapKey, "dividend_account", collections.StringKey, codec.CollValue[hTypes.DividendAccount](cdc)),
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
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+types.ModuleName)
}

// GetAllTopupSequences returns all the topup sequences
func (k *Keeper) GetAllTopupSequences(ctx context.Context) (seq []string, e error) {
	logger := k.Logger(ctx)

	// get the sequences iterator
	iter, err := k.sequences.Iterate(ctx, nil)
	if err != nil {
		e = err
		return nil, e
	}

	// defer closing the iterator
	defer func(iter collections.KeySetIterator[string]) {
		err := iter.Close()
		if err != nil {
			logger.Error("error closing topup sequences iterator", "err", err)
			seq = nil
			e = err
		}
	}(iter)

	// iterate over sequences' keys and return them
	sequences, err := iter.Keys()
	if err != nil {
		logger.Error("error getting topup sequences from the iterator", "err", err)
		e = err
		return nil, err
	}

	return sequences, e
}

// SetTopupSequence sets the topup sequence value in the store for the given key
func (k *Keeper) SetTopupSequence(ctx context.Context, sequence string) error {
	logger := k.Logger(ctx)

	err := k.sequences.Set(ctx, sequence)
	if err != nil {
		logger.Error("error setting topup sequence", "sequence", sequence, "err", err)
		return err
	}

	logger.Debug("topup sequence set", "sequence", sequence)

	return nil
}

// HasTopupSequence checks if the topup sequence exists
func (k *Keeper) HasTopupSequence(ctx context.Context, sequence string) (bool, error) {
	logger := k.Logger(ctx)

	isSequencePresent, err := k.sequences.Has(ctx, sequence)
	if err != nil {
		logger.Error("error checking if topup sequence exists", "sequence", sequence, "err", err)
		return false, err
	}

	logger.Debug("topup sequence exists", "sequence", sequence, "isSequencePresent", isSequencePresent)

	return isSequencePresent, nil
}

// GetAllDividendAccounts returns all the dividend accounts
func (k *Keeper) GetAllDividendAccounts(ctx context.Context) (da []hTypes.DividendAccount, e error) {
	logger := k.Logger(ctx)

	// get the dividend accounts iterator
	iter, err := k.dividendAccounts.Iterate(ctx, nil)
	if err != nil {
		e = err
		return nil, e
	}

	// defer closing the iterator
	defer func(iter collections.Iterator[string, hTypes.DividendAccount]) {
		err := iter.Close()
		if err != nil {
			logger.Error("error closing dividend accounts iterator", "err", err)
			da = nil
			e = err
		}
	}(iter)

	// iterate over dividend accounts' values and return them
	dividendAccounts, err := iter.Values()
	if err != nil {
		logger.Error("error getting dividend accounts from the iterator", "err", err)
		e = err
		return nil, e
	}

	return dividendAccounts, e
}

// SetDividendAccount sets the dividend account in the store for the given dividendAccount
func (k *Keeper) SetDividendAccount(ctx context.Context, dividendAccount hTypes.DividendAccount) error {
	logger := k.Logger(ctx)

	dividendAccount.User = util.FormatAddress(dividendAccount.User)
	err := k.dividendAccounts.Set(ctx, dividendAccount.User, dividendAccount)
	if err != nil {
		logger.Error("error adding dividend account", "dividendAccount", dividendAccount, "err", err)
		return err
	}

	logger.Debug("dividend account added", "dividendAccount", dividendAccount)

	return nil
}

// HasDividendAccount checks if the dividend account exists
func (k *Keeper) HasDividendAccount(ctx context.Context, user string) (bool, error) {
	logger := k.Logger(ctx)

	isDividendAccountPresent, err := k.dividendAccounts.Has(ctx, util.FormatAddress(user))
	if err != nil {
		logger.Error("error checking if dividend account exists", "user", user, "err", err)
		return false, err
	}

	logger.Debug("dividend account exists", "user", user, "isDividendAccountPresent", isDividendAccountPresent)

	return isDividendAccountPresent, nil
}

// GetDividendAccount returns the dividend account for the given user
func (k *Keeper) GetDividendAccount(ctx context.Context, user string) (hTypes.DividendAccount, error) {
	logger := k.Logger(ctx)

	dividendAccount, err := k.dividendAccounts.Get(ctx, util.FormatAddress(user))
	if err != nil {
		logger.Error("error getting dividend account", "user", user, "err", err)
		return hTypes.DividendAccount{}, err
	}

	logger.Debug("dividend account retrieved", "user", user, "dividendAccount", dividendAccount)

	return dividendAccount, nil
}

// AddFeeToDividendAccount adds the fee to the dividend account for the given user
func (k *Keeper) AddFeeToDividendAccount(ctx context.Context, user string, fee *big.Int) error {
	logger := k.Logger(ctx)

	// check if dividendAccount exists
	exist, err := k.HasDividendAccount(ctx, util.FormatAddress(user))
	if err != nil {
		return err
	}

	var dividendAccount hTypes.DividendAccount
	if !exist {
		// create a new dividend account
		logger.Debug("dividend account not found, creating one", "user", user)
		dividendAccount = hTypes.DividendAccount{
			User:      util.FormatAddress(user),
			FeeAmount: big.NewInt(0).String(),
		}
	} else {
		// get the dividend account
		dividendAccount, err = k.GetDividendAccount(ctx, user)
		if err != nil {
			return err
		}
	}

	// update the fee
	oldFee, ok := big.NewInt(0).SetString(dividendAccount.FeeAmount, 10)
	if !ok {
		logger.Error("failed to set the old fee", "feeAmount", dividendAccount.FeeAmount, "account", dividendAccount.User)
		return errors.New("failed to set the old fee for dividend account")
	}
	totalFee := big.NewInt(0).Add(oldFee, fee).String()
	dividendAccount.FeeAmount = totalFee
	logger.Info("fee added to dividend account", "user", user, "oldFee", oldFee, "addedFee", fee, "totalFee", totalFee)

	// set the updated dividend account
	err = k.SetDividendAccount(ctx, dividendAccount)
	if err != nil {
		logger.Error("error adding fee to dividend account", "user", user, "fee", fee, "err", err)
		return err
	}

	return nil
}
