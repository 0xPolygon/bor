package bor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// maxSpanFetchLimit denotes maximum number of future spans to fetch. During snap sync,
// we verify very large batch of headers. The maximum range is not known as of now and
// hence we set a very high limit. It can be reduced later.
// const maxSpanFetchLimit = 10_000

// SpanStore acts as a simple middleware to cache span data populated from heimdall. It is used
// in multiple places of bor consensus for verification.
type SpanStore struct {
	store *lru.ARCCache

	latestSpanCache atomic.Pointer[borTypes.Span]
	latestSpanAt    atomic.Int64 // unix nanos of the last latest-span fetch that succeeded

	heimdallClient IHeimdallClient
	spanner        Spanner

	chainId           string
	lastUsedSpan      atomic.Pointer[borTypes.Span]
	latestKnownSpanId atomic.Uint64
	heimdallStatus    atomic.Pointer[ctypes.SyncInfo]

	// cancel function to stop the background routine
	cancel context.CancelFunc
	// pollWg tracks the background polling goroutine so PurgeCache and Close
	// can wait for it to exit before touching shared state.
	pollWg sync.WaitGroup
}

func NewSpanStore(heimdallClient IHeimdallClient, spanner Spanner, chainId string) *SpanStore {
	cache, _ := lru.NewARC(10)
	store := &SpanStore{
		store:           cache,
		heimdallClient:  heimdallClient,
		spanner:         spanner,
		chainId:         chainId,
		latestSpanCache: atomic.Pointer[borTypes.Span]{},
		lastUsedSpan:    atomic.Pointer[borTypes.Span]{},
	}
	store.startPollLoop()
	return store
}

// startPollLoop spawns the background goroutine that periodically refreshes
// latestSpanCache and heimdallStatus. No-op when heimdallClient is nil.
// latestSpanFreshness is how old the latest span may be for a lookup to rule out a
// newer span. The poll loop refreshes it every 200ms; older means Heimdall stopped answering.
const latestSpanFreshness = 2 * time.Second

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

	s.latestSpanCache.Store(&borTypes.Span{
		Id:                latestSpan.Id,
		StartBlock:        latestSpan.StartBlock,
		EndBlock:          latestSpan.EndBlock,
		SelectedProducers: span.ConvertBorValidatorsToHeimdallValidators(selectedProducers),
		ValidatorSet:      span.ConvertBorValSetToHeimdallValSet(valset.NewValidatorSet(validators)),
		BorChainId:        s.chainId,
	})
	s.latestSpanAt.Store(time.Now().UnixNano())
	return nil
}

// spanById returns a span given its id. It fetches span from heimdall if not found in cache.
func (s *SpanStore) spanById(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	var currentSpan *borTypes.Span
	if value, ok := s.store.Get(spanId); ok {
		currentSpan, _ = value.(*borTypes.Span)
	}

	if currentSpan != nil {
		return currentSpan, nil
	}

	var err error
	if s.heimdallClient == nil {
		if spanId == 0 {
			currentSpan, err = getMockSpan0(ctx, s.spanner, s.chainId)
			if err != nil {
				log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("unable to create test span without heimdall client for id %d", spanId)
		}
	} else {
		currentSpan, err = s.heimdallClient.GetSpan(ctx, spanId)
		if err != nil {
			log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
			return nil, err
		}

		if len(currentSpan.SelectedProducers) == 0 {
			log.Warn("Span from Heimdall has empty SelectedProducers", "spanId", spanId, "selectedProducers", currentSpan.SelectedProducers, "validators", currentSpan.ValidatorSet.Validators, "startBlock", currentSpan.StartBlock, "endBlock", currentSpan.EndBlock)
			return nil, fmt.Errorf("span %d has empty SelectedProducers, possibly incomplete", spanId)
		}
	}

	if currentSpan == nil {
		return nil, fmt.Errorf("span not found for id %d", spanId)
	}

	s.store.Add(spanId, currentSpan)
	if currentSpan.Id > s.latestKnownSpanId.Load() {
		s.latestKnownSpanId.Store(currentSpan.Id)
	}

	return currentSpan, nil
}

// spanByBlockNumber returns a span given a block number. It fetches span from heimdall if not found in cache. It
// assumes that a span has been committed before (i.e. is current or past span) and returns an error if
// asked for a future span. This is safe to assume as we don't have a way to find out span id for a future block
// unless we hardcode the span length (which we don't want to).
func (s *SpanStore) spanByBlockNumber(ctx context.Context, blockNumber uint64) (*borTypes.Span, error) {
	span, _, err := s.settledSpanByBlockNumber(ctx, blockNumber)
	return span, err
}

// settledSpanByBlockNumber is spanByBlockNumber, also reporting whether the search for a
// newer span holding the block settled (see getFutureSpan). If not, the span may be an
// older one standing in for a newer span Heimdall failed to return.
func (s *SpanStore) settledSpanByBlockNumber(ctx context.Context, blockNumber uint64) (res *borTypes.Span, settled bool, err error) {
	s.waitUntilHeimdallIsSynced(ctx)

	// As we don't persist latest known span to db, we loose the value on restarts. This leads to multiple heimdall calls
	// which can be avoided. Hence we estimate the span id from block number which updates the latest known span id. Note
	// that we still check if the block number lies in the range of span before returning it.
	estimatedSpanId := s.estimateSpanId(blockNumber)
	defer func() {
		if res != nil && len(res.SelectedProducers) > 0 && err == nil {
			s.lastUsedSpan.Store(res)
		}
	}()

	res, beyond, err := s.knownSpan(ctx, blockNumber, estimatedSpanId)
	if err != nil {
		return nil, false, err
	}
	if beyond {
		// Block is in the future, search future spans
		return s.getFutureSpan(ctx, estimatedSpanId+1, blockNumber)
	}

	// If we found a candidate in known spans, we still need to check if there are newer spans in future
	// that also contain this block number due to overlapping spans
	if res != nil {
		futureSpan, settled, err := s.getFutureSpan(ctx, estimatedSpanId+1, blockNumber)
		if err == nil && futureSpan != nil {
			// Found a future span that also contains the block, return the newer one
			return futureSpan, settled, nil
		}
		// No future span found or error occurred, return the candidate from known spans
		return res, settled, nil
	}

	return nil, false, fmt.Errorf("span not found for block %d", blockNumber)
}

// knownSpan searches backwards from the highest known span ID for the latest
// span containing the block, so the first match has the largest ID among known
// spans. beyond reports a block past the end of the highest known span.
func (s *SpanStore) knownSpan(ctx context.Context, blockNumber uint64, highestId uint64) (span *borTypes.Span, beyond bool, err error) {
	for id := int(highestId); id >= 0; id-- {
		span, err := s.spanById(ctx, uint64(id))
		if err != nil {
			return nil, false, err
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			return span, false, nil
		}
		if id == int(highestId) && blockNumber > span.EndBlock {
			return nil, true, nil
		}
	}
	return nil, false, nil
}

// getFutureSpan fetches span for future block number. It is mostly needed during snap sync.
// It also reports whether the search settled: it reached a latest span fetched within
// latestSpanFreshness with no failed lookup on the way.
func (s *SpanStore) getFutureSpan(ctx context.Context, id uint64, blockNumber uint64) (*borTypes.Span, bool, error) {
	latestSpan, err := s.getLatestSpan(ctx)
	if err != nil || latestSpan == nil {
		return nil, false, err
	}

	fresh := time.Since(time.Unix(0, s.latestSpanAt.Load())) < latestSpanFreshness

	var candidateSpan *borTypes.Span
	skippedSpans := 0
	for ; id <= latestSpan.Id; id++ {
		span, err := s.spanById(ctx, id)
		if err != nil {
			if candidateSpan == nil {
				return nil, false, err
			}
			return candidateSpan, false, nil
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			candidateSpan = span
			skippedSpans = 0
		}
		// Stopping early leaves it unsettled: a newer span can start before older ones.
		if blockNumber < span.StartBlock {
			skippedSpans++
			if skippedSpans > 1 {
				return spanOrNotFound(candidateSpan, false, blockNumber)
			}
		}
	}

	return spanOrNotFound(candidateSpan, fresh, blockNumber)
}

func spanOrNotFound(span *borTypes.Span, settled bool, blockNumber uint64) (*borTypes.Span, bool, error) {
	if span == nil {
		return nil, settled, fmt.Errorf("span not found for block %d", blockNumber)
	}
	return span, settled, nil
}

// estimateSpanId returns the corresponding span id for the given block number in a deterministic way.
func (s *SpanStore) estimateSpanId(blockNumber uint64) uint64 {
	if blockNumber > zerothSpanEnd && blockNumber > 0 {
		if s.lastUsedSpan.Load() != nil {
			lastUsedSpan := s.lastUsedSpan.Load()
			startBlock := lastUsedSpan.StartBlock
			endBlock := lastUsedSpan.EndBlock
			if blockNumber > endBlock {
				return lastUsedSpan.Id + 1 + (blockNumber-endBlock-1)/defaultSpanLength
			} else if blockNumber < startBlock {
				// Calculate how many spans to go back. (startBlock - blockNumber + defaultSpanLength - 1) / defaultSpanLength is ceil((startBlock - blockNumber)/defaultSpanLength)
				spansToDecrement := 1 + (startBlock-blockNumber-1)/defaultSpanLength
				if lastUsedSpan.Id >= spansToDecrement { // Prevent underflow for uint64
					return lastUsedSpan.Id - spansToDecrement
				} else {
					return 1 + (blockNumber-zerothSpanEnd-1)/defaultSpanLength
				}
			} else {
				return lastUsedSpan.Id
			}
		}
		return 1 + (blockNumber-zerothSpanEnd-1)/defaultSpanLength
	}

	return 0
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

	// Create a new cache to replace the old one
	newCache, _ := lru.NewARC(10)
	s.store = newCache
	// Clear the latest span cache
	s.latestSpanCache.Store(nil)
	// Clear the last used span
	s.lastUsedSpan.Store(nil)
	// Reset the latest known span ID
	s.latestKnownSpanId.Store(0)
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

// Close cancels the background routine and cleans up resources
func (s *SpanStore) Close() {
	s.stopPollLoop()
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
