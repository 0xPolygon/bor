package bor

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"golang.org/x/sync/singleflight"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// SpanStore acts as a simple middleware to cache span data populated from heimdall. It is used
// in multiple places of bor consensus for verification.
type SpanStore struct {
	store *lru.ARCCache

	latestSpanCache atomic.Pointer[borTypes.Span]

	heimdallClient IHeimdallClient
	spanner        Spanner

	chainId        string
	rioBlock       *big.Int
	heimdallStatus atomic.Pointer[ctypes.SyncInfo]

	// spanRanges holds the block range of every span fetched so far. Heimdall never rewrites a stored
	// span, so a range stays valid for the life of the store; keeping only the ranges lets lookups
	// for old blocks walk thousands of span ids without refetching them. spanRangesMu also guards
	// rangeGen and the swap of store, so a fetch started before PurgeCache can't write into the
	// caches that replace the purged ones.
	spanRangesMu sync.Mutex
	spanRanges   map[uint64]spanRange
	rangeGen     uint64
	rangeFetches singleflight.Group

	// lifetime bounds the span fetches shared by concurrent lookups; Close cancels it.
	lifetime      context.Context
	closeLifetime context.CancelFunc

	// warmCancel and warmWg stop and track the background walk started by startRangeWarmup.
	warmCancel context.CancelFunc
	warmWg     sync.WaitGroup

	// cancel function to stop the background routine
	cancel context.CancelFunc
	// pollWg tracks the background polling goroutine so PurgeCache and Close
	// can wait for it to exit before touching shared state.
	pollWg sync.WaitGroup
}

func NewSpanStore(heimdallClient IHeimdallClient, spanner Spanner, chainId string, rioBlock *big.Int) *SpanStore {
	cache, _ := lru.NewARC(10)
	store := &SpanStore{
		store:           cache,
		heimdallClient:  heimdallClient,
		spanner:         spanner,
		chainId:         chainId,
		rioBlock:        rioBlock,
		latestSpanCache: atomic.Pointer[borTypes.Span]{},
	}
	store.lifetime, store.closeLifetime = context.WithCancel(context.Background())
	store.startPollLoop()
	return store
}

// startPollLoop spawns the background goroutine that periodically refreshes
// latestSpanCache and heimdallStatus. No-op when heimdallClient is nil.
func (s *SpanStore) startPollLoop() {
	if s.heimdallClient == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.pollWg.Add(1)
	go s.runPollLoop(ctx)
}

func (s *SpanStore) runPollLoop(ctx context.Context) {
	defer s.pollWg.Done()
	const errorLogInterval = 10 * time.Second
	var lastSpanErr, lastHeimdallErr time.Time

	for {
		if err := s.updateLatestSpan(ctx); err != nil {
			logErrRateLimited(&lastSpanErr, errorLogInterval, "Failed to update latest span", err)
		}
		if err := s.updateHeimdallStatus(ctx); err != nil {
			logErrRateLimited(&lastHeimdallErr, errorLogInterval, "Failed to update heimdall status", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// logErrRateLimited logs err at most once per interval, updating lastLog when it does.
func logErrRateLimited(lastLog *time.Time, interval time.Duration, msg string, err error) {
	if time.Since(*lastLog) >= interval {
		log.Error(msg, "err", err)
		*lastLog = time.Now()
	}
}

// stopPollLoop cancels the background goroutine and blocks until it exits.
// Safe to call when the loop is not running.
func (s *SpanStore) stopPollLoop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.pollWg.Wait()
	s.cancel = nil
}

func (s *SpanStore) getLatestSpan(ctx context.Context) (*borTypes.Span, error) {
	if s.latestSpanCache.Load() != nil {
		return s.latestSpanCache.Load(), nil
	}

	err := s.updateLatestSpan(ctx)
	if err != nil {
		return nil, err
	}
	return s.latestSpanCache.Load(), nil
}

func (s *SpanStore) updateHeimdallStatus(ctx context.Context) (err error) {
	var syncInfo *ctypes.SyncInfo
	if s.heimdallClient == nil {
		syncInfo = &ctypes.SyncInfo{CatchingUp: false}
	} else {
		syncInfo, err = s.heimdallClient.FetchStatus(ctx)
		if err != nil {
			s.heimdallStatus.Store(nil)
			return err
		}
	}
	s.heimdallStatus.Store(syncInfo)
	return nil
}

func (s *SpanStore) waitUntilHeimdallIsSynced(ctx context.Context) {
	// If there's no heimdall client, don't wait
	if s.heimdallClient == nil {
		return
	}

	timeout := 200 * time.Millisecond
	logInterval := 10 * time.Second
	var lastLogTime time.Time

	for {
		syncInfo := s.heimdallStatus.Load()
		if syncInfo == nil || syncInfo.CatchingUp {
			if time.Since(lastLogTime) >= logInterval {
				log.Warn("Heimdall isn't synced, waiting for update", "syncInfo", syncInfo)
				lastLogTime = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(timeout):
				s.updateHeimdallStatus(ctx)
				continue
			}
		}

		return
	}
}

func (s *SpanStore) updateLatestSpan(ctx context.Context) error {
	if s.heimdallClient == nil {
		return nil
	}

	latestSpan, err := s.heimdallClient.GetLatestSpan(ctx)
	if err != nil {
		return err
	}

	validators := make([]*valset.Validator, len(latestSpan.ValidatorSet.Validators))
	for i, v := range latestSpan.ValidatorSet.Validators {
		validators[i] = &valset.Validator{
			ID:               v.ValId,
			Address:          common.HexToAddress(v.Signer),
			VotingPower:      v.VotingPower,
			ProposerPriority: v.ProposerPriority,
		}
	}

	selectedProducers := make([]*valset.Validator, len(latestSpan.SelectedProducers))
	for i, v := range latestSpan.SelectedProducers {
		selectedProducers[i] = &valset.Validator{
			ID:               v.ValId,
			Address:          common.HexToAddress(v.Signer),
			VotingPower:      v.VotingPower,
			ProposerPriority: v.ProposerPriority,
		}
	}

	s.storeLatestSpan(&borTypes.Span{
		Id:                latestSpan.Id,
		StartBlock:        latestSpan.StartBlock,
		EndBlock:          latestSpan.EndBlock,
		SelectedProducers: span.ConvertBorValidatorsToHeimdallValidators(selectedProducers),
		ValidatorSet:      span.ConvertBorValSetToHeimdallValSet(valset.NewValidatorSet(validators)),
		BorChainId:        s.chainId,
	})
	return nil
}

// storeLatestSpan keeps latestSpanCache from going back to a lower span id. Heimdall never
// removes a span, so a lower latest id only comes from a lagging endpoint (e.g. a failover
// secondary one span behind), and walking from it would resolve blocks of the newer span to an
// older one. PurgeCache resets the cache to nil, so any id is accepted after it.
func (s *SpanStore) storeLatestSpan(latest *borTypes.Span) {
	for {
		cur := s.latestSpanCache.Load()
		if cur != nil && cur.Id > latest.Id {
			log.Debug("Ignoring stale latest span", "cached", cur.Id, "received", latest.Id)
			return
		}
		if s.latestSpanCache.CompareAndSwap(cur, latest) {
			return
		}
	}
}

// spanById returns a span given its id. It fetches span from heimdall if not found in cache.
func (s *SpanStore) spanById(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	return s.spanByIdAt(ctx, s.rangeGeneration(), spanId)
}

// spanByIdAt is spanById for a lookup started in cache generation gen: the fetched span is only
// cached if no PurgeCache happened since.
func (s *SpanStore) spanByIdAt(ctx context.Context, gen uint64, spanId uint64) (*borTypes.Span, error) {
	if cached := s.cachedSpan(spanId); cached != nil {
		return cached, nil
	}

	var currentSpan *borTypes.Span
	var err error
	if s.heimdallClient == nil {
		currentSpan, err = s.mockSpan(ctx, spanId)
	} else {
		currentSpan, err = s.fetchSpan(ctx, spanId)
	}
	if err != nil {
		return nil, err
	}

	s.cacheSpan(gen, spanId, currentSpan)

	return currentSpan, nil
}

// mockSpan returns span 0 built from the genesis validators, the only span a store without a
// heimdall client has.
func (s *SpanStore) mockSpan(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	if spanId != 0 {
		return nil, fmt.Errorf("unable to create test span without heimdall client for id %d", spanId)
	}
	currentSpan, err := getMockSpan0(ctx, s.spanner, s.chainId)
	if err != nil {
		log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
		return nil, err
	}
	return currentSpan, nil
}

func (s *SpanStore) fetchSpan(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	currentSpan, err := s.fetchSpanRecord(ctx, spanId)
	if err != nil {
		return nil, err
	}
	if len(currentSpan.SelectedProducers) == 0 {
		log.Warn("Span from Heimdall has empty SelectedProducers", "spanId", spanId, "selectedProducers", currentSpan.SelectedProducers, "validators", currentSpan.ValidatorSet.Validators, "startBlock", currentSpan.StartBlock, "endBlock", currentSpan.EndBlock)
		return nil, fmt.Errorf("span %d has empty SelectedProducers, possibly incomplete", spanId)
	}
	return currentSpan, nil
}

// fetchSpanRecord fetches span id from heimdall without checking that it is complete.
func (s *SpanStore) fetchSpanRecord(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	currentSpan, err := s.heimdallClient.GetSpan(ctx, spanId)
	if err != nil {
		log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
		return nil, err
	}
	if currentSpan == nil {
		return nil, fmt.Errorf("span not found for id %d", spanId)
	}
	return currentSpan, nil
}

// spanByBlockNumber returns the span that governs blockNumber: the highest-id span containing it,
// among the spans up to Heimdall's latest. This is the rule Heimdall applies (x/bor
// SpanByBlockNumber). Rotation and planned-downtime spans can start before spans created earlier,
// so no span below the latest id can be ruled out by its position; every id down to the first
// match is checked. Blocks before Rio are the exception, see preRioSpanIdContaining.
func (s *SpanStore) spanByBlockNumber(ctx context.Context, blockNumber uint64) (*borTypes.Span, error) {
	s.waitUntilHeimdallIsSynced(ctx)

	latestSpan, err := s.getLatestSpan(ctx)
	if err != nil {
		return nil, err
	}

	id, err := s.spanIdContainingRefreshed(ctx, blockNumber, latestSpan)
	if err != nil {
		return nil, err
	}
	return s.spanById(ctx, id)
}

// setHeimdallClient sets the underlying heimdall client to be used. It is useful in
// tests where mock heimdall client is set after creation of bor instance explicitly.
func (s *SpanStore) setHeimdallClient(client IHeimdallClient) {
	s.heimdallClient = client
}

// PurgeCache clears all cached spans and resets state. This is useful in tests
// when the mock heimdall client is changed and old cached data needs to be invalidated.
//
// The background poll loop is stopped before the reset so it can't race in and
// re-populate latestSpanCache after the clear. PurgeCache leaves the poll loop
// stopped — on-demand reads via getLatestSpan/spanById still work because they
// fall back to updateLatestSpan inline when the cache is empty.
func (s *SpanStore) PurgeCache() {
	s.stopPollLoop()
	s.stopRangeWarmup()

	// Create a new cache to replace the old one. Bumping the generation keeps fetches still in
	// flight from the purged state out of the new caches, and out of lookups started from now on.
	newCache, _ := lru.NewARC(10)
	s.spanRangesMu.Lock()
	s.store = newCache
	s.spanRanges = nil
	s.rangeGen++
	s.spanRangesMu.Unlock()
	// Clear the latest span cache
	s.latestSpanCache.Store(nil)
	// Clear cached heimdall status. With the poll loop stopped, a stale
	// CatchingUp:false would otherwise let waitUntilHeimdallIsSynced skip
	// refreshing status against a newly-swapped heimdall client.
	s.heimdallStatus.Store(nil)
}

// getMockSpan0 constructs a mock span 0 by fetching validator set from genesis state. This should
// only be used in tests where heimdall client is not available.
func getMockSpan0(ctx context.Context, spanner Spanner, chainId string) (*borTypes.Span, error) {
	if spanner == nil {
		return nil, fmt.Errorf("spanner not available to fetch validator set")
	}

	// Fetch validators from genesis state
	vals, err := spanner.GetCurrentValidatorsByBlockNrOrHash(ctx, rpc.BlockNumberOrHashWithNumber(0), 0)
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("no validators found for genesis, cannot create mock span 0")
	}
	validatorSet := valset.ValidatorSet{
		Validators: vals,
		Proposer:   vals[0],
	}

	return &borTypes.Span{
		Id:                0,
		StartBlock:        0,
		EndBlock:          255,
		ValidatorSet:      span.ConvertBorValSetToHeimdallValSet(&validatorSet),
		SelectedProducers: span.ConvertBorValidatorsToHeimdallValidators(vals),
		BorChainId:        chainId,
	}, nil
}

// Close cancels the background routine and the span fetches still in flight.
func (s *SpanStore) Close() {
	s.stopPollLoop()
	s.stopRangeWarmup()
	s.closeLifetime()
}

// Wait for a new span whose selected producers are different from the current header author
func (s *SpanStore) waitForNewSpan(targetBlockNumber uint64, currentHeaderAuthor common.Address, timeout time.Duration) (bool, error) {
	delay := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	currentSpan, err := s.spanByBlockNumber(ctx, targetBlockNumber)
	if err != nil {
		return false, err
	}

	for {
		if currentSpan.StartBlock <= targetBlockNumber && currentSpan.EndBlock >= targetBlockNumber {
			if len(currentSpan.SelectedProducers) > 0 && common.HexToAddress(currentSpan.SelectedProducers[0].Signer) != currentHeaderAuthor {
				return true, nil
			}
		}

		select {
		case <-ctx.Done():
			return false, nil
		case <-time.After(delay):
			// Only update span after delay if we need to keep waiting
			currentSpan, err = s.spanByBlockNumber(ctx, targetBlockNumber)
			if err != nil {
				return false, err
			}
		}
	}
}
