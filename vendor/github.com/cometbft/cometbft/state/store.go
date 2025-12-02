package state

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtos "github.com/cometbft/cometbft/libs/os"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

const (
	// persist validators every valSetCheckpointInterval blocks to avoid
	// LoadValidators taking too much time.
	// https://github.com/tendermint/tendermint/pull/3438
	// 100000 results in ~ 100ms to get 100 validators (see BenchmarkLoadValidators)
	valSetCheckpointInterval = 100000
)

var (
	ErrKeyNotFound        = errors.New("key not found")
	ErrInvalidHeightValue = errors.New("invalid height value")
)

//------------------------------------------------------------------------

func calcValidatorsKey(height int64) []byte {
	return []byte(fmt.Sprintf("validatorsKey:%v", height))
}

func calcConsensusParamsKey(height int64) []byte {
	return []byte(fmt.Sprintf("consensusParamsKey:%v", height))
}

func calcABCIResponsesKey(height int64) []byte {
	return []byte(fmt.Sprintf("abciResponsesKey:%v", height))
}

// ----------------------
var (
	lastABCIResponseKey              = []byte("lastABCIResponseKey")
	lastABCIResponsesRetainHeightKey = []byte("lastABCIResponsesRetainHeight")
	offlineStateSyncHeight           = []byte("offlineStateSyncHeightKey")
)

//go:generate ../scripts/mockery_generate.sh Store

// Store defines the state store interface
//
// It is used to retrieve current state and save and load ABCI responses,
// validators and consensus parameters
type Store interface {
	// LoadFromDBOrGenesisFile loads the most recent state.
	// If the chain is new it will use the genesis file from the provided genesis file path as the current state.
	LoadFromDBOrGenesisFile(filename string) (State, error)
	// LoadFromDBOrGenesisDoc loads the most recent state.
	// If the chain is new it will use the genesis doc as the current state.
	LoadFromDBOrGenesisDoc(doc *types.GenesisDoc) (State, error)
	// Load loads the current state of the blockchain
	Load() (State, error)
	// LoadValidators loads the validator set at a given height
	LoadValidators(height int64) (*types.ValidatorSet, error)
	// LoadFinalizeBlockResponse loads the abciResponse for a given height
	LoadFinalizeBlockResponse(int64) (*abci.ResponseFinalizeBlock, error)
	// LoadLastFinalizeBlockResponse loads the last abciResponse for a given height
	LoadLastFinalizeBlockResponse(int64) (*abci.ResponseFinalizeBlock, error)
	// LoadConsensusParams loads the consensus params for a given height
	LoadConsensusParams(height int64) (types.ConsensusParams, error)
	// Save overwrites the previous state with the updated one
	Save(state State) error
	// SaveFinalizeBlockResponse saves ABCIResponses for a given height
	SaveFinalizeBlockResponse(height int64, res *abci.ResponseFinalizeBlock) error
	// Bootstrap is used for bootstrapping state when not starting from a initial height.
	Bootstrap(state State) error
	// Saves the height at which the store is bootstrapped after out of band statesync
	SetOfflineStateSyncHeight(height int64) error
	// Gets the height at which the store is bootstrapped after out of band statesync
	GetOfflineStateSyncHeight() (int64, error)
	// PruneStates takes the height from which to start pruning and which height stop at
	PruneStates(fromHeight, toHeight, evidenceThresholdHeight int64) (uint64, error)
	// PruneABCIResponses will prune all ABCI responses below the given height.
	PruneABCIResponses(targetRetainHeight int64, forceCompact bool) (int64, int64, error)
	// SaveApplicationRetainHeight persists the application retain height from the application
	SaveApplicationRetainHeight(height int64) error
	// GetApplicationRetainHeight returns the retain height set by the application
	GetApplicationRetainHeight() (int64, error)
	// SaveCompanionBlockRetainHeight saves the retain height set by the data companion
	SaveCompanionBlockRetainHeight(height int64) error
	// GetCompanionBlockRetainHeight returns the retain height set by the data companion
	GetCompanionBlockRetainHeight() (int64, error)
	// SaveABCIResRetainHeight persists the retain height for ABCI results set by the data companion
	SaveABCIResRetainHeight(height int64) error
	// GetABCIResRetainHeight returns the last saved retain height for ABCI results set by the data companion
	GetABCIResRetainHeight() (int64, error)
	// Close closes the connection with the database
	Close() error
}

// dbStore wraps a db (github.com/cometbft/cometbft-db)
type dbStore struct {
	db dbm.DB

	StoreOptions

	StoreStateKeeper
}

type StoreStateKeeper struct {
	ResultsToCompact uint64

	StatesToCompact uint64
}
type StoreOptions struct {
	// DiscardABCIResponses determines whether or not the store
	// retains all ABCIResponses. If DiscardABCIResponses is enabled,
	// the store will maintain only the response object from the latest
	// height.
	DiscardABCIResponses bool

	Compact bool

	CompactionInterval int64
}

var _ Store = (*dbStore)(nil)

func IsEmpty(store dbStore) (bool, error) {
	state, err := store.Load()
	if err != nil {
		return false, err
	}
	return state.IsEmpty(), nil
}

// NewStore creates the dbStore of the state pkg.
func NewStore(db dbm.DB, options StoreOptions) Store {
	return dbStore{db, options, StoreStateKeeper{}}
}

// LoadStateFromDBOrGenesisFile loads the most recent state from the database,
// or creates a new one from the given genesisFilePath.
func (store dbStore) LoadFromDBOrGenesisFile(genesisFilePath string) (State, error) {
	state, err := store.Load()
	if err != nil {
		return State{}, err
	}
	if state.IsEmpty() {
		var err error
		state, err = MakeGenesisStateFromFile(genesisFilePath)
		if err != nil {
			return state, err
		}
	}

	return state, nil
}

// LoadStateFromDBOrGenesisDoc loads the most recent state from the database,
// or creates a new one from the given genesisDoc.
func (store dbStore) LoadFromDBOrGenesisDoc(genesisDoc *types.GenesisDoc) (State, error) {
	state, err := store.Load()
	if err != nil {
		return State{}, err
	}

	if state.IsEmpty() {
		var err error
		state, err = MakeGenesisState(genesisDoc)
		if err != nil {
			return state, err
		}
	}

	return state, nil
}

// LoadState loads the State from the database.
func (store dbStore) Load() (State, error) {
	return store.loadState(stateKey)
}

func (store dbStore) loadState(key []byte) (state State, err error) {
	buf, err := store.db.Get(key)
	if err != nil {
		return state, err
	}
	if len(buf) == 0 {
		return state, nil
	}

	sp := new(cmtstate.State)

	err = proto.Unmarshal(buf, sp)
	if err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmtos.Exit(fmt.Sprintf(`LoadState: Data has been corrupted or its spec has changed:
		%v\n`, err))
	}

	sm, err := FromProto(sp)
	if err != nil {
		return state, err
	}
	return *sm, nil
}

// Save persists the State, the ValidatorsInfo, and the ConsensusParamsInfo to the database.
// This flushes the writes (e.g. calls SetSync).
func (store dbStore) Save(state State) error {
	return store.save(state, stateKey)
}

func (store dbStore) save(state State, key []byte) error {
	batch := store.db.NewBatch()
	defer func(batch dbm.Batch) {
		err := batch.Close()
		if err != nil {
			panic(err)
		}
	}(batch)
	nextHeight := state.LastBlockHeight + 1
	// If first block, save validators for the block.
	if nextHeight == 1 {
		nextHeight = state.InitialHeight
		// This extra logic due to validator set changes being delayed 1 block.
		// It may get overwritten due to InitChain validator updates.
		if err := store.saveValidatorsInfo(nextHeight, nextHeight, state.Validators, batch); err != nil {
			return err
		}
	}
	// Save next validators.
	if err := store.saveValidatorsInfo(nextHeight+1, state.LastHeightValidatorsChanged, state.NextValidators, batch); err != nil {
		return err
	}
	// Save next consensus params.
	if err := store.saveConsensusParamsInfo(nextHeight,
		state.LastHeightConsensusParamsChanged, state.ConsensusParams, batch); err != nil {
		return err
	}
	if err := batch.Set(key, state.Bytes()); err != nil {
		return err
	}
	if err := batch.WriteSync(); err != nil {
		panic(err)
	}
	return nil
}

// BootstrapState saves a new state, used e.g. by state sync when starting from non-zero height.
func (store dbStore) Bootstrap(state State) error {
	batch := store.db.NewBatch()
	defer func(batch dbm.Batch) {
		err := batch.Close()
		if err != nil {
			panic(err)
		}
	}(batch)
	height := state.LastBlockHeight + 1
	if height == 1 {
		height = state.InitialHeight
	}

	if height > 1 && !state.LastValidators.IsNilOrEmpty() {
		if err := store.saveValidatorsInfo(height-1, height-1, state.LastValidators, batch); err != nil {
			return err
		}
	}

	if err := store.saveValidatorsInfo(height, height, state.Validators, batch); err != nil {
		return err
	}

	if err := store.saveValidatorsInfo(height+1, height+1, state.NextValidators, batch); err != nil {
		return err
	}

	if err := store.saveConsensusParamsInfo(height,
		state.LastHeightConsensusParamsChanged, state.ConsensusParams, batch); err != nil {
		return err
	}

	if err := batch.Set(stateKey, state.Bytes()); err != nil {
		return err
	}

	if err := batch.WriteSync(); err != nil {
		panic(err)
	}

	return batch.Close()
}

// PruneStates deletes states between the given heights (including from, excluding to). It is not
// guaranteed to delete all states, since the last checkpointed state and states being pointed to by
// e.g. `LastHeightChanged` must remain. The state at to must also exist.
//
// The from parameter is necessary since we can't do a key scan in a performant way due to the key
// encoding not preserving ordering: https://github.com/tendermint/tendermint/issues/4567
// This will cause some old states to be left behind when doing incremental partial prunes,
// specifically older checkpoints and LastHeightChanged targets.
func (store dbStore) PruneStates(from int64, to int64, evidenceThresholdHeight int64) (uint64, error) {
	if from <= 0 || to <= 0 {
		return 0, fmt.Errorf("from height %v and to height %v must be greater than 0", from, to)
	}
	if from >= to {
		return 0, fmt.Errorf("from height %v must be lower than to height %v", from, to)
	}

	valInfo, err := loadValidatorsInfo(store.db, min(to, evidenceThresholdHeight))
	if err != nil {
		return 0, fmt.Errorf("validators at height %v not found: %w", to, err)
	}
	paramsInfo, err := store.loadConsensusParamsInfo(to)
	if err != nil {
		return 0, fmt.Errorf("consensus params at height %v not found: %w", to, err)
	}

	keepVals := make(map[int64]bool)
	if valInfo.ValidatorSet == nil {
		keepVals[valInfo.LastHeightChanged] = true
		keepVals[lastStoredHeightFor(to, valInfo.LastHeightChanged)] = true // keep last checkpoint too
	}
	keepParams := make(map[int64]bool)
	if paramsInfo.ConsensusParams.Equal(&cmtproto.ConsensusParams{}) {
		keepParams[paramsInfo.LastHeightChanged] = true
	}

	batch := store.db.NewBatch()
	defer batch.Close()
	pruned := uint64(0)

	// We have to delete in reverse order, to avoid deleting previous heights that have validator
	// sets and consensus params that we may need to retrieve.
	for h := to - 1; h >= from; h-- {
		// For heights we keep, we must make sure they have the full validator set or consensus
		// params, otherwise they will panic if they're retrieved directly (instead of
		// indirectly via a LastHeightChanged pointer).
		if keepVals[h] {
			v, err := loadValidatorsInfo(store.db, h)
			if err != nil || v.ValidatorSet == nil {
				vip, err := store.LoadValidators(h)
				if err != nil {
					return pruned, err
				}

				pvi, err := vip.ToProto()
				if err != nil {
					return pruned, err
				}

				v.ValidatorSet = pvi
				v.LastHeightChanged = h

				bz, err := v.Marshal()
				if err != nil {
					return pruned, err
				}
				err = batch.Set(calcValidatorsKey(h), bz)
				if err != nil {
					return pruned, err
				}
			}
		} else if h < evidenceThresholdHeight {
			err = batch.Delete(calcValidatorsKey(h))
			if err != nil {
				return pruned, err
			}
		}
		// else we keep the validator set because we might need
		// it later on for evidence verification

		if keepParams[h] {
			p, err := store.loadConsensusParamsInfo(h)
			if err != nil {
				return pruned, err
			}

			if p.ConsensusParams.Equal(&cmtproto.ConsensusParams{}) {
				params, err := store.LoadConsensusParams(h)
				if err != nil {
					return pruned, err
				}
				p.ConsensusParams = params.ToProto()

				p.LastHeightChanged = h
				bz, err := p.Marshal()
				if err != nil {
					return pruned, err
				}

				err = batch.Set(calcConsensusParamsKey(h), bz)
				if err != nil {
					return pruned, err
				}
			}
		} else {
			err = batch.Delete(calcConsensusParamsKey(h))
			if err != nil {
				return pruned, err
			}
		}

		err = batch.Delete(calcABCIResponsesKey(h))
		if err != nil {
			return pruned, err
		}
		pruned++

		// avoid batches growing too large by flushing to database regularly
		if pruned%1000 == 0 && pruned > 0 {
			err := batch.Write()
			if err != nil {
				return pruned, err
			}
			batch.Close()
			batch = store.db.NewBatch()
			defer batch.Close()
		}
	}

	err = batch.WriteSync()
	if err != nil {
		return pruned, err
	}
	store.StoreStateKeeper.StatesToCompact += pruned

	// We do not want to panic or interrupt consensus on compaction failure
	if store.StoreOptions.Compact {
		store.StoreStateKeeper.StatesToCompact += pruned
		if store.StoreStateKeeper.StatesToCompact >= uint64(store.StoreOptions.CompactionInterval) {
			// When the range is nil,nil, the database will try to compact
			// ALL levels. Another option is to set a predefined range of
			// specific keys.
			err = store.db.Compact(nil, nil)
			if err == nil {
				store.StoreStateKeeper.StatesToCompact = 0
			}
		}
	}

	return pruned, nil
}

// PruneABCIResponses attempts to prune all ABCI responses up to, but not
// including, the given height. On success, returns the number of heights
// pruned and the new retain height.
func (store dbStore) PruneABCIResponses(targetRetainHeight int64, forceCompact bool) (int64, int64, error) {
	if store.DiscardABCIResponses {
		return 0, 0, nil
	}
	lastRetainHeight, err := store.getLastABCIResponsesRetainHeight()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to look up last ABCI responses retain height: %w", err)
	}
	if lastRetainHeight == 0 {
		lastRetainHeight = 1
	}

	batch := store.db.NewBatch()
	defer batch.Close()

	pruned := int64(0)
	batchPruned := int64(0)

	for h := lastRetainHeight; h < targetRetainHeight; h++ {
		if err := batch.Delete(calcABCIResponsesKey(h)); err != nil {
			return pruned, lastRetainHeight + pruned, fmt.Errorf("failed to delete ABCI responses at height %d: %w", h, err)
		}
		batchPruned++
		if batchPruned >= 1000 {
			if err := batch.Write(); err != nil {
				return pruned, lastRetainHeight + pruned, fmt.Errorf("failed to write ABCI responses deletion batch at height %d: %w", h, err)
			}
			batch.Close()

			pruned += batchPruned
			batchPruned = 0
			if err := store.setLastABCIResponsesRetainHeight(h); err != nil {
				return pruned, lastRetainHeight + pruned, fmt.Errorf("failed to set last ABCI responses retain height: %w", err)
			}

			batch = store.db.NewBatch()
			defer batch.Close()
		}
	}
	if err = batch.WriteSync(); err != nil {
		return pruned + batchPruned, targetRetainHeight, err
	}

	// forceCompact was introduced because in main and v1 there is no config to prune ABCI results
	// and they are pruned only when instructed by the data companion (which does not exist here)
	// When we do want to enfore pruning of the results with state pruning then
	// we can also check store.Compact
	if forceCompact || store.StoreOptions.Compact {
		store.StoreStateKeeper.ResultsToCompact += uint64(pruned + batchPruned)
		if store.StoreStateKeeper.ResultsToCompact >= (uint64)(store.StoreOptions.CompactionInterval) {
			err = store.db.Compact(nil, nil)
			if err == nil {
				store.StoreStateKeeper.ResultsToCompact = 0
			}
		}
	}
	return pruned + batchPruned, targetRetainHeight, err
}

//------------------------------------------------------------------------

// TxResultsHash returns the root hash of a Merkle tree of
// ExecTxResulst responses (see ABCIResults.Hash)
//
// See merkle.SimpleHashFromByteSlices
func TxResultsHash(txResults []*abci.ExecTxResult) []byte {
	return types.NewResults(txResults).Hash()
}

// LoadFinalizeBlockResponse loads the DiscardABCIResponses for the given height from the
// database. If the node has D set to true, ErrABCIResponsesNotPersisted
// is persisted. If not found, ErrNoABCIResponsesForHeight is returned.
func (store dbStore) LoadFinalizeBlockResponse(height int64) (*abci.ResponseFinalizeBlock, error) {
	if store.DiscardABCIResponses {
		return nil, ErrFinalizeBlockResponsesNotPersisted
	}

	buf, err := store.db.Get(calcABCIResponsesKey(height))
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, ErrNoABCIResponsesForHeight{height}
	}

	resp := new(abci.ResponseFinalizeBlock)
	err = resp.Unmarshal(buf)
	// Check for an error or if the resp.AppHash is nil if so
	// this means the unmarshalling should be a LegacyABCIResponses
	// Depending on a source message content (serialized as ABCIResponses)
	// there are instances where it can be deserialized as a FinalizeBlockResponse
	// without causing an error. But the values will not be deserialized properly
	// and, it will contain zero values, and one of them is an AppHash == nil
	// This can be verified in the /state/compatibility_test.go file
	if err != nil || resp.AppHash == nil {
		// The data might be of the legacy ABCI response type, so
		// we try to unmarshal that
		legacyResp := new(cmtstate.LegacyABCIResponses)
		if err := legacyResp.Unmarshal(buf); err != nil {
			// only return an error, this method is only invoked through the `/block_results` not for state logic and
			// some tests, so no need to exit cometbft if there's an error, just return it.
			return nil, ErrABCIResponseCorruptedOrSpecChangeForHeight{Height: height, Err: err}
		}
		// The state store contains the old format. Migrate to
		// the new ResponseFinalizeBlock format. Note that the
		// new struct expects the AppHash which we don't have.
		return responseFinalizeBlockFromLegacy(legacyResp), nil
	}

	// TODO: ensure that buf is completely read.

	return resp, nil
}

// LoadLastFinalizeBlockResponse loads the FinalizeBlockResponses from the most recent height.
// The height parameter is used to ensure that the response corresponds to the latest height.
// If not, an error is returned.
//
// This method is used for recovering in the case that we called the Commit ABCI
// method on the application but crashed before persisting the results.
func (store dbStore) LoadLastFinalizeBlockResponse(height int64) (*abci.ResponseFinalizeBlock, error) {
	bz, err := store.db.Get(lastABCIResponseKey)
	if err != nil {
		return nil, err
	}

	if len(bz) == 0 {
		return nil, errors.New("no last ABCI response has been persisted")
	}

	info := new(cmtstate.ABCIResponsesInfo)
	err = info.Unmarshal(bz)
	if err != nil {
		cmtos.Exit(fmt.Sprintf(`LoadLastFinalizeBlockResponse: Data has been corrupted or its spec has
			changed: %v\n`, err))
	}

	// Here we validate the result by comparing its height to the expected height.
	if height != info.GetHeight() {
		return nil, fmt.Errorf("expected height %d but last stored abci responses was at height %d", height, info.GetHeight())
	}

	// It is possible if this is called directly after an upgrade that
	// ResponseFinalizeBlock is nil. In which case we use the legacy
	// ABCI responses
	if info.ResponseFinalizeBlock == nil {
		// sanity check
		if info.LegacyAbciResponses == nil {
			panic("state store contains last abci response but it is empty")
		}
		return responseFinalizeBlockFromLegacy(info.LegacyAbciResponses), nil
	}

	return info.ResponseFinalizeBlock, nil
}

// SaveFinalizeBlockResponse persists the ResponseFinalizeBlock to the database.
// This is useful in case we crash after app.Commit and before s.Save().
// Responses are indexed by height so they can also be loaded later to produce
// Merkle proofs.
//
// CONTRACT: height must be monotonically increasing every time this is called.
func (store dbStore) SaveFinalizeBlockResponse(height int64, resp *abci.ResponseFinalizeBlock) error {
	var dtxs []*abci.ExecTxResult
	// strip nil values,
	for _, tx := range resp.TxResults {
		if tx != nil {
			dtxs = append(dtxs, tx)
		}
	}
	resp.TxResults = dtxs

	// If the flag is false then we save the ABCIResponse. This can be used for the /BlockResults
	// query or to reindex an event using the command line.
	if !store.DiscardABCIResponses {
		bz, err := resp.Marshal()
		if err != nil {
			return err
		}
		if err := store.db.Set(calcABCIResponsesKey(height), bz); err != nil {
			return err
		}
	}

	// We always save the last ABCI response for crash recovery.
	// This overwrites the previous saved ABCI Response.
	response := &cmtstate.ABCIResponsesInfo{
		ResponseFinalizeBlock: resp,
		Height:                height,
	}
	bz, err := response.Marshal()
	if err != nil {
		return err
	}

	return store.db.SetSync(lastABCIResponseKey, bz)
}

func (store dbStore) getValue(key []byte) ([]byte, error) {
	bz, err := store.db.Get(key)
	if err != nil {
		return nil, err
	}

	if len(bz) == 0 {
		return nil, ErrKeyNotFound
	}
	return bz, nil
}

// SaveApplicationRetainHeight saves the desired applicationRetainHeight
func (store dbStore) SaveApplicationRetainHeight(height int64) error {
	return store.db.SetSync(AppRetainHeightKey, int64ToBytes(height))
}

// GetApplicationRetainHeight retrieves the application  retain height
func (store dbStore) GetApplicationRetainHeight() (int64, error) {
	buf, err := store.getValue(AppRetainHeightKey)
	if err != nil {
		return 0, err
	}
	height := int64FromBytes(buf)

	if height < 0 {
		return 0, ErrInvalidHeightValue
	}

	return height, nil
}

// DataCompanionRetainHeight
func (store dbStore) SaveCompanionBlockRetainHeight(height int64) error {
	return store.db.SetSync(CompanionBlockRetainHeightKey, int64ToBytes(height))
}

func (store dbStore) GetCompanionBlockRetainHeight() (int64, error) {
	buf, err := store.getValue(CompanionBlockRetainHeightKey)
	if err != nil {
		return 0, err
	}
	height := int64FromBytes(buf)

	if height < 0 {
		return 0, ErrInvalidHeightValue
	}

	return height, nil
}

// DataCompanionRetainHeight
func (store dbStore) SaveABCIResRetainHeight(height int64) error {
	return store.db.SetSync(ABCIResultsRetainHeightKey, int64ToBytes(height))
}

func (store dbStore) GetABCIResRetainHeight() (int64, error) {
	buf, err := store.getValue(ABCIResultsRetainHeightKey)
	if err != nil {
		return 0, err
	}
	height := int64FromBytes(buf)

	if height < 0 {
		return 0, ErrInvalidHeightValue
	}

	return height, nil
}

func (store dbStore) getLastABCIResponsesRetainHeight() (int64, error) {
	bz, err := store.getValue(lastABCIResponsesRetainHeightKey)
	if errors.Is(err, ErrKeyNotFound) {
		return 0, nil
	}
	height := int64FromBytes(bz)
	if height < 0 {
		return 0, ErrInvalidHeightValue
	}
	return height, nil
}

func (store dbStore) setLastABCIResponsesRetainHeight(height int64) error {
	return store.db.SetSync(lastABCIResponsesRetainHeightKey, int64ToBytes(height))
}

//-----------------------------------------------------------------------------

// LoadValidators loads the ValidatorSet for a given height.
// Returns ErrNoValSetForHeight if the validator set can't be found for this height.
func (store dbStore) LoadValidators(height int64) (*types.ValidatorSet, error) {
	valInfo, err := loadValidatorsInfo(store.db, height)
	if err != nil {
		return nil, ErrNoValSetForHeight{height}
	}
	if valInfo.ValidatorSet == nil {
		lastStoredHeight := lastStoredHeightFor(height, valInfo.LastHeightChanged)
		valInfo2, err := loadValidatorsInfo(store.db, lastStoredHeight)
		if err != nil || valInfo2.ValidatorSet == nil {
			return nil,
				fmt.Errorf("couldn't find validators at height %d (height %d was originally requested): %w",
					lastStoredHeight,
					height,
					err,
				)
		}

		vs, err := types.ValidatorSetFromProto(valInfo2.ValidatorSet)
		if err != nil {
			return nil, err
		}

		vs.IncrementProposerPriority(cmtmath.SafeConvertInt32(height - lastStoredHeight)) // mutate
		vi2, err := vs.ToProto()
		if err != nil {
			return nil, err
		}

		valInfo2.ValidatorSet = vi2
		valInfo = valInfo2
	}

	vip, err := types.ValidatorSetFromProto(valInfo.ValidatorSet)
	if err != nil {
		return nil, err
	}

	return vip, nil
}

func lastStoredHeightFor(height, lastHeightChanged int64) int64 {
	checkpointHeight := height - height%valSetCheckpointInterval
	return cmtmath.MaxInt64(checkpointHeight, lastHeightChanged)
}

// CONTRACT: Returned ValidatorsInfo can be mutated.
func loadValidatorsInfo(db dbm.DB, height int64) (*cmtstate.ValidatorsInfo, error) {
	buf, err := db.Get(calcValidatorsKey(height))
	if err != nil {
		return nil, err
	}

	if len(buf) == 0 {
		return nil, errors.New("value retrieved from db is empty")
	}

	v := new(cmtstate.ValidatorsInfo)
	err = v.Unmarshal(buf)
	if err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmtos.Exit(fmt.Sprintf(`LoadValidators: Data has been corrupted or its spec has changed:
        %v\n`, err))
	}
	// TODO: ensure that buf is completely read.

	return v, nil
}

// saveValidatorsInfo persists the validator set.
//
// `height` is the effective height for which the validator is responsible for
// signing. It should be called from s.Save(), right before the state itself is
// persisted.
func (store dbStore) saveValidatorsInfo(height, lastHeightChanged int64, valSet *types.ValidatorSet, batch dbm.Batch) error {
	if lastHeightChanged > height {
		return errors.New("lastHeightChanged cannot be greater than ValidatorsInfo height")
	}
	valInfo := &cmtstate.ValidatorsInfo{
		LastHeightChanged: lastHeightChanged,
	}
	// Only persist validator set if it was updated or checkpoint height (see
	// valSetCheckpointInterval) is reached.
	if height == lastHeightChanged || height%valSetCheckpointInterval == 0 {
		pv, err := valSet.ToProto()
		if err != nil {
			return err
		}
		valInfo.ValidatorSet = pv
	}

	bz, err := valInfo.Marshal()
	if err != nil {
		return err
	}

	err = batch.Set(calcValidatorsKey(height), bz)
	if err != nil {
		return err
	}

	return nil
}

//-----------------------------------------------------------------------------

// ConsensusParamsInfo represents the latest consensus params, or the last height it changed

// LoadConsensusParams loads the ConsensusParams for a given height.
func (store dbStore) LoadConsensusParams(height int64) (types.ConsensusParams, error) {
	var (
		empty   = types.ConsensusParams{}
		emptypb = cmtproto.ConsensusParams{}
	)
	paramsInfo, err := store.loadConsensusParamsInfo(height)
	if err != nil {
		return empty, fmt.Errorf("could not find consensus params for height #%d: %w", height, err)
	}

	if paramsInfo.ConsensusParams.Equal(&emptypb) {
		paramsInfo2, err := store.loadConsensusParamsInfo(paramsInfo.LastHeightChanged)
		if err != nil {
			return empty, fmt.Errorf(
				"couldn't find consensus params at height %d as last changed from height %d: %w",
				paramsInfo.LastHeightChanged,
				height,
				err,
			)
		}

		paramsInfo = paramsInfo2
	}

	return types.ConsensusParamsFromProto(paramsInfo.ConsensusParams), nil
}

func (store dbStore) loadConsensusParamsInfo(height int64) (*cmtstate.ConsensusParamsInfo, error) {
	buf, err := store.db.Get(calcConsensusParamsKey(height))
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, errors.New("value retrieved from db is empty")
	}

	paramsInfo := new(cmtstate.ConsensusParamsInfo)
	if err = paramsInfo.Unmarshal(buf); err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmtos.Exit(fmt.Sprintf(`LoadConsensusParams: Data has been corrupted or its spec has changed:
                %v\n`, err))
	}
	// TODO: ensure that buf is completely read.

	return paramsInfo, nil
}

// saveConsensusParamsInfo persists the consensus params for the next block to disk.
// It should be called from s.Save(), right before the state itself is persisted.
// If the consensus params did not change after processing the latest block,
// only the last height for which they changed is persisted.
func (store dbStore) saveConsensusParamsInfo(nextHeight, changeHeight int64, params types.ConsensusParams, batch dbm.Batch) error {
	paramsInfo := &cmtstate.ConsensusParamsInfo{
		LastHeightChanged: changeHeight,
	}

	if changeHeight == nextHeight {
		paramsInfo.ConsensusParams = params.ToProto()
	}
	bz, err := paramsInfo.Marshal()
	if err != nil {
		return err
	}

	err = batch.Set(calcConsensusParamsKey(nextHeight), bz)
	if err != nil {
		return err
	}

	return nil
}

func (store dbStore) SetOfflineStateSyncHeight(height int64) error {
	err := store.db.SetSync(offlineStateSyncHeight, int64ToBytes(height))
	if err != nil {
		return err
	}
	return nil

}

// Gets the height at which the store is bootstrapped after out of band statesync
func (store dbStore) GetOfflineStateSyncHeight() (int64, error) {

	buf, err := store.db.Get(offlineStateSyncHeight)
	if err != nil {
		return 0, err
	}

	if len(buf) == 0 {
		return 0, errors.New("value empty")
	}

	height := int64FromBytes(buf)
	if height < 0 {
		return 0, errors.New("invalid value for height: height cannot be negative")
	}
	return height, nil
}

func (store dbStore) Close() error {
	return store.db.Close()
}

func min(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// responseFinalizeBlockFromLegacy is a convenience function that takes the old abci responses and morphs
// it to the finalize block response. Note that the app hash is missing
func responseFinalizeBlockFromLegacy(legacyResp *cmtstate.LegacyABCIResponses) *abci.ResponseFinalizeBlock {
	var response abci.ResponseFinalizeBlock
	events := make([]abci.Event, 0)

	if legacyResp.DeliverTxs != nil {
		response.TxResults = legacyResp.DeliverTxs
	}

	// Check for begin block and end block and only append events or assign values if they are not nil
	if legacyResp.BeginBlock != nil {
		if legacyResp.BeginBlock.Events != nil {
			// Add BeginBlock attribute to BeginBlock events
			for idx := range legacyResp.BeginBlock.Events {
				legacyResp.BeginBlock.Events[idx].Attributes = append(legacyResp.BeginBlock.Events[idx].Attributes, abci.EventAttribute{
					Key:   "mode",
					Value: "BeginBlock",
					Index: false,
				})
			}
			events = append(events, legacyResp.BeginBlock.Events...)
		}
	}
	if legacyResp.EndBlock != nil {
		if legacyResp.EndBlock.ValidatorUpdates != nil {
			response.ValidatorUpdates = legacyResp.EndBlock.ValidatorUpdates
		}
		if legacyResp.EndBlock.ConsensusParamUpdates != nil {
			response.ConsensusParamUpdates = legacyResp.EndBlock.ConsensusParamUpdates
		}
		if legacyResp.EndBlock.Events != nil {
			// Add EndBlock attribute to BeginBlock events
			for idx := range legacyResp.EndBlock.Events {
				legacyResp.EndBlock.Events[idx].Attributes = append(legacyResp.EndBlock.Events[idx].Attributes, abci.EventAttribute{
					Key:   "mode",
					Value: "EndBlock",
					Index: false,
				})
			}
			events = append(events, legacyResp.EndBlock.Events...)
		}
	}

	response.Events = events

	// NOTE: AppHash is missing in the response but will
	// be caught and filled in consensus/replay.go
	return &response
}

func int64FromBytes(bz []byte) int64 {
	v, _ := binary.Varint(bz)
	return v
}

func int64ToBytes(i int64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutVarint(buf, i)
	return buf[:n]
}
