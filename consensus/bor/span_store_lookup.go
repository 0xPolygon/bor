package bor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/log"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// spanWarmupRetry is how long startRangeWarmup waits before retrying a walk that failed.
var spanWarmupRetry = 5 * time.Second

var errSpanNotFound = errors.New("span not found")

type spanRange struct {
	start, end uint64
}

// preRioSpanIdContaining finds the span of a block before Rio by binary search over span ids.
// Heimdall only accepts a pre-Rio span that starts right after the previous one, and every
// span created from Rio on starts at or after the Rio block. So among all span ids, those
// starting at or below a pre-Rio block form a prefix, and the last id of that prefix is the
// only span containing the block, which is what the full walk would return.
func (s *SpanStore) preRioSpanIdContaining(ctx context.Context, blockNumber uint64, latestId uint64) (uint64, error) {
	// Span 0 starts at block 0, so lo always starts at or below blockNumber.
	lo, hi := uint64(0), latestId
	for lo < hi {
		// Upper midpoint, written so it can't wrap when hi is the largest id.
		mid := hi - (hi-lo)/2
		r, err := s.spanRangeById(ctx, mid)
		if err != nil {
			return 0, err
		}
		if r.start <= blockNumber {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	r, err := s.spanRangeById(ctx, lo)
	if err != nil {
		return 0, err
	}
	if blockNumber < r.start || blockNumber > r.end {
		return 0, fmt.Errorf("%w for block %d", errSpanNotFound, blockNumber)
	}
	return lo, nil
}

// spanIdContainingRefreshed resolves blockNumber against latestSpan. When no span contains a
// block past the latest span's end, the cached latest span may lag Heimdall's, so it is refreshed
// once and the lookup runs again up to the new latest id.
func (s *SpanStore) spanIdContainingRefreshed(ctx context.Context, blockNumber uint64, latestSpan *borTypes.Span) (uint64, error) {
	id, err := s.spanIdContaining(ctx, blockNumber, latestSpan)
	if !errors.Is(err, errSpanNotFound) || latestSpan == nil || blockNumber <= latestSpan.EndBlock {
		return id, err
	}

	if updateErr := s.updateLatestSpan(ctx); updateErr != nil {
		return 0, updateErr
	}
	fresh := s.latestSpanCache.Load()
	if fresh == nil || fresh.Id <= latestSpan.Id {
		return 0, err
	}
	return s.spanIdContaining(ctx, blockNumber, fresh)
}

func (s *SpanStore) spanIdContaining(ctx context.Context, blockNumber uint64, latestSpan *borTypes.Span) (uint64, error) {
	// Without a heimdall client only the mock span 0 exists.
	var latestId uint64
	if latestSpan != nil {
		latestId = latestSpan.Id
	}
	if s.beforeRio(blockNumber) {
		return s.preRioSpanIdContaining(ctx, blockNumber, latestId)
	}
	return s.highestSpanIdContaining(ctx, blockNumber, latestId)
}

// highestSpanIdContaining walks span ids down from latestId and returns the first one whose range
// contains blockNumber. Ranges are served from spanRanges and only missing ids are fetched.
func (s *SpanStore) highestSpanIdContaining(ctx context.Context, blockNumber uint64, latestId uint64) (uint64, error) {
	id := latestId
	for {
		next, found, exhausted := s.scanSpanRanges(id, blockNumber)
		if found {
			return next, nil
		}
		if exhausted {
			return 0, fmt.Errorf("%w for block %d", errSpanNotFound, blockNumber)
		}

		if _, err := s.spanRangeById(ctx, next); err != nil {
			return 0, err
		}
		id = next
	}
}

// scanSpanRanges walks the cached ranges down from id. It stops at the first id that contains
// blockNumber (found), at the first id whose range isn't cached yet, or once no lower id can
// contain blockNumber (exhausted).
func (s *SpanStore) scanSpanRanges(from uint64, blockNumber uint64) (id uint64, found bool, exhausted bool) {
	s.spanRangesMu.Lock()
	defer s.spanRangesMu.Unlock()

	for id = from; ; id-- {
		r, ok := s.spanRanges[id]
		if !ok {
			return id, false, false
		}
		if found, exhausted = s.rangeVerdict(id, r, blockNumber); found || exhausted {
			return id, found, exhausted
		}
	}
}

// rangeVerdict reports whether span id, with range r, contains blockNumber (found), or whether
// no id at or below it can (exhausted). Callers hold spanRangesMu.
func (s *SpanStore) rangeVerdict(id uint64, r spanRange, blockNumber uint64) (found bool, exhausted bool) {
	if r.start <= blockNumber && blockNumber <= r.end {
		return true, false
	}
	// Pre-Rio spans are contiguous and hold the lowest ids, so below one that ends before
	// blockNumber no span can contain it.
	if r.end < blockNumber && s.beforeRio(r.start) {
		return false, true
	}
	return false, id == 0
}

// spanRangeById returns the block range of span id, fetching the span if its range isn't cached.
// Concurrent lookups share a single fetch per id, so parallel walks over the same cold ids cost
// one heimdall request each. The fetch is keyed by cache generation, so a lookup started after
// PurgeCache never joins one started before it.
func (s *SpanStore) spanRangeById(ctx context.Context, id uint64) (spanRange, error) {
	gen := s.rangeGeneration()
	if r, ok := s.cachedSpanRange(id); ok {
		return r, nil
	}

	key := strconv.FormatUint(gen, 10) + "/" + strconv.FormatUint(id, 10)
	ch := s.rangeFetches.DoChan(key, func() (any, error) {
		return s.fetchSpanRange(gen, id)
	})

	select {
	case <-ctx.Done():
		return spanRange{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return spanRange{}, res.Err
		}
		return res.Val.(spanRange), nil
	}
}

// fetchSpanRange is the fetch shared by every lookup waiting on span id in generation gen. It
// runs detached from them so one caller's deadline can't fail the others; it ends instead on
// Close. It has no deadline of its own because a lookup on the import path has none either, and
// failing it would reject valid headers while Heimdall is unreachable. singleflight runs it on its own goroutine, where a panic couldn't
// be recovered by any caller, so it is returned as an error.
func (s *SpanStore) fetchSpanRange(gen uint64, id uint64) (val any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("fetching span %d: %v", id, p)
		}
	}()

	if r, ok := s.cachedSpanRange(id); ok {
		return r, nil
	}
	span, err := s.rangeSpanAt(s.lifetime, gen, id)
	if err != nil {
		return nil, err
	}
	return s.recordSpanRange(gen, id, span), nil
}

// rangeSpanAt fetches span id only for its block range. A walk may have to step over a span that
// Heimdall serves without producers yet, so that is only checked on the span a lookup returns;
// such a span is kept out of the span cache.
func (s *SpanStore) rangeSpanAt(ctx context.Context, gen uint64, id uint64) (*borTypes.Span, error) {
	if cached := s.cachedSpan(id); cached != nil {
		return cached, nil
	}
	if s.heimdallClient == nil {
		return s.spanByIdAt(ctx, gen, id)
	}
	span, err := s.fetchSpanRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(span.SelectedProducers) > 0 {
		s.cacheSpan(gen, id, span)
	}
	return span, nil
}

func (s *SpanStore) beforeRio(block uint64) bool {
	return s.rioBlock != nil && s.rioBlock.Cmp(new(big.Int).SetUint64(block)) > 0
}

func (s *SpanStore) rangeGeneration() uint64 {
	s.spanRangesMu.Lock()
	defer s.spanRangesMu.Unlock()
	return s.rangeGen
}

func (s *SpanStore) cachedSpanRange(id uint64) (spanRange, bool) {
	s.spanRangesMu.Lock()
	defer s.spanRangesMu.Unlock()

	r, ok := s.spanRanges[id]
	return r, ok
}

// cacheSpan adds span to the span cache unless PurgeCache replaced it after generation gen.
func (s *SpanStore) cacheSpan(gen uint64, id uint64, span *borTypes.Span) {
	s.spanRangesMu.Lock()
	defer s.spanRangesMu.Unlock()

	if gen == s.rangeGen {
		s.store.Add(id, span)
	}
}

// cachedSpan reads span id from the span cache. PurgeCache swaps the cache under spanRangesMu, so
// the pointer is read under it too.
func (s *SpanStore) cachedSpan(id uint64) *borTypes.Span {
	s.spanRangesMu.Lock()
	store := s.store
	s.spanRangesMu.Unlock()

	value, ok := store.Get(id)
	if !ok {
		return nil
	}
	span, _ := value.(*borTypes.Span)
	return span
}

// recordSpanRange indexes the range of span id unless PurgeCache ran after generation gen.
func (s *SpanStore) recordSpanRange(gen uint64, id uint64, span *borTypes.Span) spanRange {
	r := spanRange{start: span.StartBlock, end: span.EndBlock}

	s.spanRangesMu.Lock()
	defer s.spanRangesMu.Unlock()

	if gen != s.rangeGen {
		return r
	}
	if s.spanRanges == nil {
		s.spanRanges = make(map[uint64]spanRange)
	}
	s.spanRanges[id] = r
	return r
}

// startRangeWarmup indexes, in the background, the range of every span from Rio on. Rotation and
// planned-downtime spans can end before spans created earlier, so a lookup for a block no span
// contains must check every post-Rio span; with the ranges indexed it does so without a heimdall
// call. It retries until the walk completes, Close or PurgeCache.
func (s *SpanStore) startRangeWarmup() {
	if s.heimdallClient == nil || s.rioBlock == nil {
		return
	}
	ctx, cancel := context.WithCancel(s.lifetime)
	s.warmCancel = cancel
	s.warmWg.Add(1)
	go func() {
		defer s.warmWg.Done()
		for !s.warmSpanRanges(ctx) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(spanWarmupRetry):
			}
		}
	}()
}

// warmSpanRanges walks span ids down from Heimdall's latest to the first pre-Rio span, indexing
// each range, and reports whether the walk completed.
func (s *SpanStore) warmSpanRanges(ctx context.Context) bool {
	latest, err := s.getLatestSpan(ctx)
	if err != nil {
		return false
	}
	// No span ends at the last block number, so the walk covers every id down to the first
	// pre-Rio span, or to span 0.
	_, err = s.highestSpanIdContaining(ctx, math.MaxUint64, latest.Id)
	if !errors.Is(err, errSpanNotFound) {
		log.Debug("Span range warmup incomplete", "err", err)
		return false
	}
	return true
}

// stopRangeWarmup cancels the background walk and blocks until it exits.
func (s *SpanStore) stopRangeWarmup() {
	if s.warmCancel == nil {
		return
	}
	s.warmCancel()
	s.warmWg.Wait()
	s.warmCancel = nil
}
