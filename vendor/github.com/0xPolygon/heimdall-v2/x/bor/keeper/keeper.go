package keeper

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

const (
	blockAuthorsCollisionCheck   = 10
	blockProducerMaxSpanLookback = 50
)

// Keeper stores all the bor related data
type Keeper struct {
	cdc            codec.BinaryCodec
	storeService   store.KVStoreService
	authority      string
	ck             types.ChainManagerKeeper
	sk             types.StakeKeeper
	mk             types.MilestoneKeeper
	contractCaller helper.IContractCaller

	Schema               collections.Schema
	spans                collections.Map[uint64, types.Span]
	latestSpan           collections.Item[uint64]
	seedLastProducer     collections.Map[uint64, []byte]
	Params               collections.Item[types.Params]
	ProducerVotes        collections.Map[uint64, types.ProducerVotes]
	PerformanceScore     collections.Map[uint64, uint64]
	LatestActiveProducer collections.KeySet[uint64]
	LatestFailedProducer collections.KeySet[uint64]
	LastSpanBlock        collections.Item[uint64]
}

// NewKeeper creates a new instance of the bor Keeper
func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	authority string,
	chainKeeper types.ChainManagerKeeper,
	stakingKeeper types.StakeKeeper,
	milestoneKeeper types.MilestoneKeeper,
	caller helper.IContractCaller,
) Keeper {
	bz, err := address.NewHexCodec().StringToBytes(authority)
	if err != nil {
		panic(fmt.Errorf("invalid bor authority address: %w", err))
	}

	// ensure only gov has the authority to update the params
	if !bytes.Equal(bz, authtypes.NewModuleAddress(govtypes.ModuleName)) {
		panic(fmt.Errorf("invalid bor authority address: %s", authority))
	}

	sb := collections.NewSchemaBuilder(storeService)
	k := Keeper{
		cdc:                  cdc,
		storeService:         storeService,
		authority:            authority,
		ck:                   chainKeeper,
		sk:                   stakingKeeper,
		mk:                   milestoneKeeper,
		contractCaller:       caller,
		spans:                collections.NewMap(sb, types.SpanPrefixKey, "span", collections.Uint64Key, codec.CollValue[types.Span](cdc)),
		latestSpan:           collections.NewItem(sb, types.LastSpanIDKey, "lastSpanId", collections.Uint64Value),
		seedLastProducer:     collections.NewMap(sb, types.SeedLastBlockProducerKey, "seedLastProducer", collections.Uint64Key, collections.BytesValue),
		Params:               collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		ProducerVotes:        collections.NewMap(sb, types.ProducerVotesKey, "producerVotes", collections.Uint64Key, codec.CollValue[types.ProducerVotes](cdc)),
		PerformanceScore:     collections.NewMap(sb, types.PerformanceScoreKey, "performanceScore", collections.Uint64Key, collections.Uint64Value),
		LatestActiveProducer: collections.NewKeySet(sb, types.LatestActiveProducerKey, "latestActiveProducer", collections.Uint64Key),
		LatestFailedProducer: collections.NewKeySet(sb, types.LatestFailedProducerKey, "latestFailedProducer", collections.Uint64Key),
		LastSpanBlock:        collections.NewItem(sb, types.LastSpanBlockKey, "lastSpanBlock", collections.Uint64Value),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}

	k.Schema = schema
	return k
}

// Logger returns a module-specific logger
func (k Keeper) Logger(ctx context.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+types.ModuleName)
}

// GetAuthority returns x/bor module's authority
func (k Keeper) GetAuthority() string {
	return k.authority
}

func (k *Keeper) SetContractCaller(contractCaller helper.IContractCaller) {
	k.contractCaller = contractCaller
}

// AddNewSpan adds new span for bor to store and updates last span
func (k *Keeper) AddNewSpan(ctx context.Context, span *types.Span) error {
	logger := k.Logger(ctx)
	if err := k.AddNewRawSpan(ctx, span); err != nil {
		logger.Error("error setting span", "error", err, "span", span)
		return err
	}

	return k.UpdateLastSpan(ctx, span.Id)
}

// AddNewRawSpan adds the new span for bor to store
func (k *Keeper) AddNewRawSpan(ctx context.Context, span *types.Span) error {
	return k.spans.Set(ctx, span.Id, *span)
}

// GetSpan fetches span indexed by id from store
func (k *Keeper) GetSpan(ctx context.Context, id uint64) (types.Span, error) {
	ok, err := k.spans.Has(ctx, id)
	if err != nil {
		return types.Span{}, err
	}

	// If we are starting from zero, there will be no span key present
	if !ok {
		return types.Span{}, fmt.Errorf("span not found for id: %v", id)
	}

	span, err := k.spans.Get(ctx, id)
	if err != nil {
		return types.Span{}, err
	}
	return span, nil
}

func (k *Keeper) HasSpan(ctx context.Context, id uint64) (bool, error) {
	return k.spans.Has(ctx, id)
}

// GetAllSpans fetches all spans indexed by id from store
func (k *Keeper) GetAllSpans(ctx context.Context) ([]*types.Span, error) {
	logger := k.Logger(ctx)

	// get span iterator
	iter, err := k.spans.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func(iter collections.Iterator[uint64, types.Span]) {
		err := iter.Close()
		if err != nil {
			logger.Error("error closing span iterator", "err", err)
			return
		}
	}(iter)

	res, err := iter.Values()
	if err != nil {
		logger.Error("error getting spans from iterator", "err", err)
		return nil, err
	}

	spans := make([]*types.Span, 0, len(res))
	for _, span := range res {
		spans = append(spans, &span)
	}
	return spans, err
}

// GetLastSpan fetches the last span from the store
func (k *Keeper) GetLastSpan(ctx context.Context) (types.Span, error) {
	ok, err := k.latestSpan.Has(ctx)
	if err != nil {
		return types.Span{}, err
	}

	if !ok {
		return types.Span{}, fmt.Errorf("last span not found")
	}

	// get last span id
	lastSpanId, err := k.latestSpan.Get(ctx)
	if err != nil {
		return types.Span{}, err
	}

	return k.GetSpan(ctx, lastSpanId)
}

func (k *Keeper) GetParams(ctx context.Context) (types.Params, error) {
	return k.Params.Get(ctx)
}

// FreezeSet freezes validator set for next span
func (k *Keeper) FreezeSet(ctx sdk.Context, id uint64, startBlock uint64, endBlock uint64, borChainID string, seed common.Hash) error {
	logger := k.Logger(ctx)

	var lastSpan types.Span
	var lastSpanId uint64
	if id < 2 {
		lastSpanId = id - 1
	} else {
		lastSpanId = id - 2
	}
	lastSpan, err := k.GetSpan(ctx, lastSpanId)
	if err != nil {
		return err
	}

	prevVals := make([]staketypes.Validator, 0, len(lastSpan.ValidatorSet.Validators))
	for _, val := range lastSpan.ValidatorSet.Validators {
		prevVals = append(prevVals, *val)
	}

	// select next producers
	newProducers, err := k.SelectNextProducers(ctx, seed, prevVals)
	if err != nil {
		return err
	}

	valSet, err := k.sk.GetValidatorSet(ctx)
	if err != nil {
		return err
	}

	// generate new span
	newSpan := &types.Span{
		Id:                id,
		StartBlock:        startBlock,
		EndBlock:          endBlock,
		ValidatorSet:      valSet,
		SelectedProducers: newProducers,
		BorChainId:        borChainID,
	}

	logger.Info("Freezing new span", "id", id, "span", newSpan)

	return k.AddNewSpan(ctx, newSpan)
}

// SelectNextProducers selects producers for the next span
func (k *Keeper) SelectNextProducers(ctx context.Context, seed common.Hash, prevVals []staketypes.Validator) ([]staketypes.Validator, error) {
	// spanEligibleVals are current validators who are not getting deactivated in between the next span
	spanEligibleVals := k.sk.GetSpanEligibleValidators(ctx)
	params, err := k.FetchParams(ctx)
	if err != nil {
		return nil, err
	}
	producerCount := params.ProducerCount

	// if producers to be selected are more than current validators, no need to select/shuffle
	if len(spanEligibleVals) <= int(producerCount) {
		return spanEligibleVals, nil
	}

	if len(prevVals) > 0 {
		// rollback voting powers for the selection algorithm
		spanEligibleVals = RollbackVotingPowers(spanEligibleVals, prevVals)
	}

	// select the next producers using seed as block header hash
	newProducersIds := selectNextProducers(seed, spanEligibleVals, producerCount)

	vals := make([]staketypes.Validator, 0, len(newProducersIds))

	IDToPower := make(map[uint64]uint64)
	for _, ID := range newProducersIds {
		IDToPower[ID] = IDToPower[ID] + 1
	}

	for key, value := range IDToPower {
		val, err := k.sk.GetValidatorFromValID(ctx, key)
		if err != nil {
			return nil, err
		}

		val.VotingPower = int64(value)
		vals = append(vals, val)
	}

	// sort by address
	vals = types.SortValidatorByAddress(vals)

	return vals, nil
}

// UpdateLastSpan updates the last span
func (k *Keeper) UpdateLastSpan(ctx context.Context, id uint64) error {
	return k.latestSpan.Set(ctx, id)
}

func (k *Keeper) SetLastSpanBlock(ctx context.Context, block uint64) error {
	err := k.LastSpanBlock.Set(ctx, block)
	if err != nil {
		k.Logger(ctx).Error("error while setting last span block in store", "err", err)
		return err
	}

	return nil
}

func (k *Keeper) GetLastSpanBlock(ctx context.Context) (uint64, error) {
	doExist, err := k.LastSpanBlock.Has(ctx)
	if err != nil {
		k.Logger(ctx).Error("error while checking the existence of last span block in store", "err", err)
		return 0, err
	}

	if !doExist {
		return 0, nil
	}

	block, err := k.LastSpanBlock.Get(ctx)
	if err != nil {
		k.Logger(ctx).Error("error while getting last span block from store", "err", err)
		return 0, err
	}

	return block, nil
}

// FetchNextSpanSeed gets the eth block hash which serves as seed for random selection of the producer set
// for the next span
func (k *Keeper) FetchNextSpanSeed(ctx context.Context, id uint64) (common.Hash, common.Address, error) {
	logger := k.Logger(ctx)

	var seedSpanID uint64
	if id < 2 {
		seedSpanID = id - 1
	} else {
		seedSpanID = id - 2
	}

	seedSpan, err := k.GetSpan(ctx, seedSpanID)
	if err != nil {
		logger.Error("error fetching span while calculating next span seed", "error", err)
		return common.Hash{}, common.Address{}, err
	}

	borBlock, author, err := k.getBorBlockForSpanSeed(ctx, &seedSpan, id)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}

	blockHeader, err := k.contractCaller.GetBorChainBlock(ctx, big.NewInt(int64(borBlock)))
	if err != nil {
		k.Logger(ctx).Error("error fetching block header from bor chain while calculating next span seed", "error", err, "block", borBlock)
		return common.Hash{}, common.Address{}, err
	}

	if author == nil {
		k.Logger(ctx).Error("seed author is nil")
		return blockHeader.Hash(), common.Address{}, fmt.Errorf("seed author is nil")
	}

	logger.Debug("fetched block for seed", "block", borBlock, "author", author, "span id", id)

	return blockHeader.Hash(), *author, nil
}

// StoreSeedProducer stores producer of the block used for seed for the given span id
func (k *Keeper) StoreSeedProducer(ctx context.Context, id uint64, producer *common.Address) error {
	ok, err := k.seedLastProducer.Has(ctx, id)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("seed producer already set")
	}

	err = k.seedLastProducer.Set(ctx, id, producer.Bytes())
	if err != nil {
		return err
	}

	return nil
}

// GetSeedProducer gets producer of the block used for seed for the given span id
func (k *Keeper) GetSeedProducer(ctx context.Context, id uint64) (*common.Address, error) {
	ok, err := k.seedLastProducer.Has(ctx, id)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil //nolint: nilnil
	}

	// get last seed producer
	authorBytes, err := k.seedLastProducer.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if authorBytes == nil {
		return nil, nil //nolint: nilnil
	}

	author := common.BytesToAddress(authorBytes)

	return &author, nil
}

// SetParams sets the bor module's parameters.
func (k *Keeper) SetParams(ctx context.Context, params types.Params) error {
	return k.Params.Set(ctx, params)
}

// FetchParams gets the bor module's parameters.
func (k *Keeper) FetchParams(ctx context.Context) (types.Params, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return types.Params{}, err
	}
	return params, nil
}

func (k *Keeper) GetProducerVotes(ctx context.Context, id uint64) (types.ProducerVotes, error) {
	hasVote, err := k.ProducerVotes.Has(ctx, id)
	if err != nil {
		return types.ProducerVotes{}, err
	}
	if !hasVote {
		return types.ProducerVotes{}, nil
	}

	votes, err := k.ProducerVotes.Get(ctx, id)
	if err != nil {
		return types.ProducerVotes{}, err
	}
	return votes, nil
}

func (k *Keeper) SetProducerVotes(ctx context.Context, id uint64, votes types.ProducerVotes) error {
	return k.ProducerVotes.Set(ctx, id, votes)
}

func (k *Keeper) RemoveProducerVotes(ctx context.Context, id uint64) error {
	return k.ProducerVotes.Remove(ctx, id)
}

func (k *Keeper) ClearProducerVotes(ctx context.Context) error {
	return k.ProducerVotes.Clear(ctx, nil)
}

// getBorBlockForSpanSeed returns the bor block number and its producer whose hash is used as seed for the next span
func (k *Keeper) getBorBlockForSpanSeed(ctx context.Context, seedSpan *types.Span, proposedSpanID uint64) (uint64, *common.Address, error) {
	logger := k.Logger(ctx)

	var (
		borBlock uint64
		author   *common.Address
		err      error
	)

	logger.Debug("getting bor block for span seed", "span id", seedSpan.GetId(), "proposed span id", proposedSpanID)

	if proposedSpanID == 1 {
		borBlock = 1
		author, err = k.contractCaller.GetBorChainBlockAuthor(big.NewInt(int64(borBlock)))
		if err != nil {
			logger.Error("error fetching first block for span seed", "error", err, "block", borBlock)
			return 0, nil, err
		}

		logger.Debug("returning first block author", "author", author, "block", borBlock)

		return borBlock, author, nil
	}

	uniqueAuthors := make(map[string]struct{})
	spanID := proposedSpanID - 1

	lastAuthor, err := k.GetSeedProducer(ctx, spanID)
	if err != nil {
		logger.Error("Error fetching last seed producer", "error", err, "span id", spanID)
		return 0, nil, err
	}

	// get seed block authors from last "blockProducerMaxSpanLookback" spans
	for i := 0; len(uniqueAuthors) < blockAuthorsCollisionCheck && i < blockProducerMaxSpanLookback; i++ {
		if spanID == 0 {
			break
		}

		author, err = k.GetSeedProducer(ctx, spanID)
		if err != nil {
			logger.Error("Error fetching span seed producer", "error", err, "span id", spanID)
			return 0, nil, err
		}

		if author == nil {
			logger.Info("GetSeedProducer returned empty value", "span id", spanID)
			break
		}

		uniqueAuthors[author.Hex()] = struct{}{}
		spanID--
	}

	logger.Debug("last authors", "authors", fmt.Sprintf("%+v", uniqueAuthors), "span id", seedSpan.GetId())

	firstDiffFromLast := uint64(0)

	// try to find a seed block with an author not in the last "blockAuthorsCollisionCheck" spans
	borParams, err := k.FetchParams(ctx)
	if err != nil {
		logger.Error("Error fetching bor params while getting BorBlockForSpanSeed")
		return 0, nil, err
	}

	for borBlock = seedSpan.EndBlock; borBlock >= seedSpan.StartBlock; borBlock -= borParams.SprintDuration {
		author, err = k.contractCaller.GetBorChainBlockAuthor(big.NewInt(int64(borBlock)))
		if err != nil {
			logger.Error("Error fetching block author from bor chain while calculating next span seed", "error", err, "block", borBlock)
			return 0, nil, err
		}

		if _, exists := uniqueAuthors[author.Hex()]; !exists || len(seedSpan.ValidatorSet.Validators) == 1 {
			logger.Debug("got author", "author", author, "block", borBlock)
			return borBlock, author, nil
		}

		if firstDiffFromLast == 0 && lastAuthor != nil && author.Hex() != lastAuthor.Hex() {
			firstDiffFromLast = borBlock
		}
	}

	// if no unique author found, return the first different block author
	borBlock = firstDiffFromLast
	if borBlock == 0 {
		borBlock = seedSpan.EndBlock
	}

	author, err = k.contractCaller.GetBorChainBlockAuthor(big.NewInt(int64(borBlock)))
	if err != nil {
		logger.Error("Error fetching end block author from bor chain while calculating next span seed", "error", err, "block", borBlock)
		return 0, nil, err
	}

	logger.Debug("returning first different block author", "author", author, "block", borBlock)

	return borBlock, author, nil
}

// RollbackVotingPowers rolls back voting powers of validators from a previous snapshot of validators
func RollbackVotingPowers(valsNew, valsOld []staketypes.Validator) []staketypes.Validator {
	idToVP := make(map[uint64]int64)
	for _, val := range valsOld {
		idToVP[val.ValId] = val.VotingPower
	}

	for i := range valsNew {
		if _, ok := idToVP[valsNew[i].ValId]; ok {
			valsNew[i].VotingPower = idToVP[valsNew[i].ValId]
		} else {
			valsNew[i].VotingPower = 0
		}
	}

	return valsNew
}

// CanVoteProducers checks if the current span is in VEBLOP phase and returns an error if not.
func (k Keeper) CanVoteProducers(ctx context.Context) error {
	latestSpan, err := k.GetLastSpan(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest span for VEBLOP validation: %w", err)
	}

	if !helper.IsVeblop(latestSpan.EndBlock + 1) {
		return fmt.Errorf("MsgVoteProducers not allowed: span is not in VEBLOP phase (span start block: %d, span end block: %d, veblop height: %d)",
			latestSpan.StartBlock,
			latestSpan.EndBlock,
			helper.GetVeblopHeight())
	}

	return nil
}

func (k *Keeper) SpanByBlockNumber(ctx context.Context, blockNumber uint64) (types.Span, error) {
	lastSpan, err := k.GetLastSpan(ctx)
	if err != nil {
		return types.Span{}, err
	}

	for id := lastSpan.Id; ; id-- {
		span, err := k.GetSpan(ctx, id)
		if err != nil {
			// Not finding a span that should exist is a critical error.
			return types.Span{}, fmt.Errorf("failed to get span with id %d: %w", id, err)
		}

		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			return span, nil
		}

		if id == 0 { // prevent uint64 underflow
			break
		}
	}

	return types.Span{}, fmt.Errorf("span not found for block %d", blockNumber)
}

func (k *Keeper) GetProducersByBlockNumber(ctx context.Context, blockNumber uint64) ([]common.Address, error) {
	span, err := k.SpanByBlockNumber(ctx, blockNumber)
	if err != nil {
		return nil, err
	}

	producers := make([]common.Address, 0, len(span.SelectedProducers))
	for _, producer := range span.SelectedProducers {
		producers = append(producers, common.HexToAddress(producer.Signer))
	}

	return producers, nil
}
