package bor

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/stretchr/testify/require"
)

// gatedClient holds every GetSpan call until release is closed or the call's ctx ends, and
// reports how each call ended on exited.
type gatedClient struct {
	*dynamicHeimdallClient
	entered chan struct{}
	release chan struct{}
	exited  chan error
}

func newGatedClient(ranges []testSpanRange) *gatedClient {
	return &gatedClient{
		dynamicHeimdallClient: newLookupClient(ranges),
		entered:               make(chan struct{}, 16),
		release:               make(chan struct{}),
		exited:                make(chan error, 16),
	}
}

func (g *gatedClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	g.entered <- struct{}{}
	select {
	case <-ctx.Done():
		g.exited <- ctx.Err()
		return nil, ctx.Err()
	case <-g.release:
	}
	g.exited <- nil
	return g.dynamicHeimdallClient.GetSpan(ctx, spanID)
}

// abandonFetch starts a shared fetch of span id and returns once its only caller gave up on it.
func abandonFetch(t *testing.T, store *SpanStore, client *gatedClient, id uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := store.spanRangeById(ctx, id)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	<-client.entered
}

func TestSpanStore_SpanByBlockNumber_RefreshesLaggingLatestSpan(t *testing.T) {
	base := []testSpanRange{{0, 255}, {256, 511}}
	next := append(append([]testSpanRange{}, base...), testSpanRange{512, 767})
	errLatest := errors.New("latest span unavailable")

	testcases := []struct {
		name      string
		heimdall  []testSpanRange
		latestErr error
		wantId    uint64
		wantErr   error
	}{
		{name: "heimdall has the next span", heimdall: next, wantId: 2},
		{name: "heimdall has no later span", heimdall: base, wantErr: errSpanNotFound},
		{name: "refreshing the latest span fails", heimdall: next, latestErr: errLatest, wantErr: errLatest},
	}

	rios := []struct {
		name string
		rio  *big.Int
	}{
		{name: "after rio", rio: nil},
		{name: "before rio", rio: big.NewInt(1000)},
	}

	for _, r := range rios {
		for _, tc := range testcases {
			t.Run(r.name+"/"+tc.name, func(t *testing.T) {
				client := newCountingClient(base, 0)
				store := NewSpanStore(client, nil, "1337", r.rio)
				defer store.Close()
				store.stopPollLoop()
				require.NoError(t, store.updateLatestSpan(t.Context()))
				require.NoError(t, store.updateHeimdallStatus(t.Context()))

				heimdall := newLookupClient(tc.heimdall)
				client.setSpans(heimdall.spans, heimdall.latestSpan)
				client.latestErr = tc.latestErr

				span, err := store.spanByBlockNumber(t.Context(), 600)
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.wantId, span.Id)
			})
		}
	}
}

func TestSpanStore_RangeWarmup_ServesUnmatchedLookupsFromIndex(t *testing.T) {
	preRio := contiguousRanges(20, 64)
	rio := preRio[len(preRio)-1].end + 1
	// The latest span, a rotation, ends before the look-ahead span 23 created before it.
	ranges := append(preRio, testSpanRange{rio, rio + 63}, testSpanRange{rio + 64, rio + 127},
		testSpanRange{rio + 128, rio + 191}, testSpanRange{rio + 100, rio + 150})

	testcases := []struct {
		name          string
		block         uint64
		failOnce      uint64
		latestFailing bool
		wantId        uint64
		wantErr       error
	}{
		{name: "older span ending after the latest one", block: rio + 180, wantId: 23},
		{name: "no span contains the block", block: rio + 192, wantErr: errSpanNotFound},
		{name: "walk retried after a failed fetch", block: rio + 192, failOnce: 22, wantErr: errSpanNotFound},
		{name: "walk retried while the latest span is unavailable", block: rio + 192, latestFailing: true, wantErr: errSpanNotFound},
	}

	defer func(retry time.Duration) { spanWarmupRetry = retry }(spanWarmupRetry)
	spanWarmupRetry = 10 * time.Millisecond

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newCountingClient(ranges, 0)
			if tc.failOnce != 0 {
				client.fail = map[uint64]bool{tc.failOnce: true}
			}
			if tc.latestFailing {
				client.latestErr = errors.New("latest span unavailable")
			}
			store := NewSpanStore(client, nil, "1337", new(big.Int).SetUint64(rio))
			defer store.Close()
			require.NoError(t, store.updateHeimdallStatus(t.Context()))

			store.startRangeWarmup()
			if tc.failOnce != 0 {
				require.Eventually(t, func() bool { return client.callsFor(tc.failOnce) >= 1 }, 5*time.Second, time.Millisecond)
			}
			if tc.latestFailing {
				time.Sleep(5 * spanWarmupRetry)
				require.Zero(t, client.totalCalls())
			}
			client.mu.Lock()
			client.fail, client.latestErr = nil, nil
			client.mu.Unlock()
			store.warmWg.Wait()
			for id := uint64(20); id < uint64(len(ranges)); id++ {
				_, ok := store.cachedSpanRange(id)
				require.True(t, ok, "span %d not indexed", id)
			}

			before := client.totalCalls()
			span, err := store.spanByBlockNumber(t.Context(), tc.block)
			require.Equal(t, before, client.totalCalls(), "lookup fetched spans after the warmup")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantId, span.Id)
		})
	}
}

func TestSpanStore_RangeWarmup_StopsWithStore(t *testing.T) {
	ranges := []testSpanRange{{0, 255}, {256, 511}}
	testcases := []struct {
		name string
		stop func(*SpanStore)
	}{
		{name: "close", stop: (*SpanStore).Close},
		{name: "purge", stop: (*SpanStore).PurgeCache},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newGatedClient(ranges)
			store := NewSpanStore(client, nil, "1337", big.NewInt(0))
			defer store.Close()

			store.startRangeWarmup()
			<-client.entered

			stopped := make(chan struct{})
			go func() {
				tc.stop(store)
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("warmup still running")
			}
			require.Nil(t, store.warmCancel)
		})
	}
}

func TestSpanStore_SpanRangeById_AbandonedFetchEnds(t *testing.T) {
	testcases := []struct {
		name string
		end  func(*SpanStore)
	}{
		{name: "store closed", end: (*SpanStore).Close},
		{name: "cache purged then store closed", end: func(s *SpanStore) { s.PurgeCache(); s.Close() }},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newGatedClient(contiguousRanges(2, 64))
			store := NewSpanStore(client, nil, "1337", nil)
			defer store.Close()

			abandonFetch(t, store, client, 1)
			tc.end(store)

			select {
			case err := <-client.exited:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("abandoned span fetch still running")
			}
		})
	}
}

func TestSpanStore_PurgeCache_IgnoresFetchesStartedBefore(t *testing.T) {
	testcases := []struct {
		name         string
		releaseFirst bool
	}{
		{name: "old fetch completes before the next lookup", releaseFirst: true},
		{name: "next lookup while the old fetch is in flight"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			old := newGatedClient([]testSpanRange{{0, 255}, {256, 511}})
			store := NewSpanStore(old, nil, "1337", nil)
			defer store.Close()

			abandonFetch(t, store, old, 1)
			store.PurgeCache()
			store.setHeimdallClient(newLookupClient([]testSpanRange{{0, 255}, {256, 600}}))

			// Lets the old fetch finish and gives it time to write back what it fetched.
			finishOld := func() {
				close(old.release)
				require.NoError(t, <-old.exited)
				time.Sleep(100 * time.Millisecond)
			}
			if tc.releaseFirst {
				finishOld()
			}

			type result struct {
				span *types.Span
				err  error
			}
			done := make(chan result, 1)
			go func() {
				span, err := store.spanByBlockNumber(context.Background(), 550)
				done <- result{span, err}
			}()
			if !tc.releaseFirst {
				time.Sleep(50 * time.Millisecond)
				finishOld()
			}

			select {
			case res := <-done:
				require.NoError(t, res.err)
				require.Equal(t, uint64(600), res.span.EndBlock)
			case <-time.After(10 * time.Second):
				t.Fatal("lookup after PurgeCache did not return")
			}

			r, ok := store.cachedSpanRange(1)
			require.True(t, ok)
			require.Equal(t, uint64(600), r.end)
			cached, ok := store.store.Get(uint64(1))
			require.True(t, ok)
			require.Equal(t, uint64(600), cached.(*types.Span).EndBlock)
		})
	}
}

// faultyClient answers GetSpan with span, or panics.
type faultyClient struct {
	*dynamicHeimdallClient
	span   *types.Span
	panics bool
}

func (f *faultyClient) GetSpan(context.Context, uint64) (*types.Span, error) {
	if f.panics {
		panic("unexpected response")
	}
	return f.span, nil
}

func TestSpanStore_SpanByBlockNumber_FaultySpanResponse(t *testing.T) {
	testcases := []struct {
		name    string
		span    *types.Span
		panics  bool
		wantMsg string
	}{
		{name: "no span", wantMsg: "span not found for id 2"},
		{name: "no selected producers", span: &types.Span{Id: 2, StartBlock: 256, EndBlock: 319}, wantMsg: "span 2 has empty SelectedProducers, possibly incomplete"},
		{name: "fetch panics", panics: true, wantMsg: "fetching span 2: unexpected response"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := &faultyClient{dynamicHeimdallClient: newLookupClient(contiguousRanges(2, 64)), span: tc.span, panics: tc.panics}
			store := NewSpanStore(client, nil, "1337", nil)
			defer store.Close()

			_, err := store.spanByBlockNumber(t.Context(), 300)
			require.EqualError(t, err, tc.wantMsg)
		})
	}
}

func TestSpanStore_SpanByBlockNumber_LargeSpanIds(t *testing.T) {
	for _, id := range []uint64{math.MaxUint64, 1 << 62} {
		t.Run(big.NewInt(0).SetUint64(id).String(), func(t *testing.T) {
			latest := makeTestSpan(id, 256, 511, "0x0000000000000000000000000000000000001001")
			client := newDynamicHeimdallClient()
			client.setSpans(map[uint64]*types.Span{
				0:  makeTestSpan(0, 0, 255, "0x0000000000000000000000000000000000001000"),
				id: latest,
			}, latest)
			store := NewSpanStore(client, nil, "1337", nil)
			defer store.Close()

			span, err := store.spanByBlockNumber(t.Context(), 300)
			require.NoError(t, err)
			require.Equal(t, id, span.Id)
		})
	}
}

func TestSpanStore_SpanById_WithoutHeimdallClient(t *testing.T) {
	testcases := []struct {
		name    string
		id      uint64
		wantMsg string
	}{
		{name: "span other than 0", id: 1, wantMsg: "unable to create test span without heimdall client for id 1"},
		{name: "span 0 without a spanner", id: 0, wantMsg: "spanner not available to fetch validator set"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewSpanStore(nil, nil, "1337", nil)
			defer store.Close()

			span, err := store.spanById(t.Context(), tc.id)
			require.EqualError(t, err, tc.wantMsg)
			require.Nil(t, span)
		})
	}
}

func TestSpanStore_PreRioSearch_LargestLatestIdTerminates(t *testing.T) {
	latest := makeTestSpan(math.MaxUint64, 256, 511, "0x0000000000000000000000000000000000001001")
	client := newDynamicHeimdallClient()
	client.setSpans(map[uint64]*types.Span{
		0:                  makeTestSpan(0, 0, 255, "0x0000000000000000000000000000000000001000"),
		math.MaxUint64:     latest,
		math.MaxUint64 / 2: makeTestSpan(math.MaxUint64/2, 256, 511, "0x0000000000000000000000000000000000001001"),
	}, latest)
	store := NewSpanStore(client, nil, "1337", big.NewInt(1_000_000))
	defer store.Close()

	done := make(chan error, 1)
	go func() {
		_, err := store.spanByBlockNumber(t.Context(), 100)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pre-rio span search did not terminate")
	}
}

func TestSpanStore_SpanById_ConcurrentPurge(t *testing.T) {
	client := newLookupClient(contiguousRanges(4, 64))
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	ctx := t.Context()
	stop := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = store.spanById(ctx, 1)
		}
	}()
	for i := 0; i < 50; i++ {
		store.PurgeCache()
	}
	close(stop)
	<-readDone
}

func TestSpanStore_SpanByBlockNumber_SpanWithoutProducers(t *testing.T) {
	testcases := []struct {
		name    string
		rio     *big.Int
		block   uint64
		wantId  uint64
		wantErr string
	}{
		{name: "after rio, block in the previous span", block: 300, wantId: 1},
		{name: "before rio, block in the previous span", rio: big.NewInt(1_000_000), block: 300, wantId: 1},
		{name: "block in the span without producers", block: 600, wantErr: "span 2 has empty SelectedProducers, possibly incomplete"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newLookupClient([]testSpanRange{{0, 255}, {256, 511}, {512, 767}})
			incomplete := makeTestSpan(2, 512, 767, "0x0000000000000000000000000000000000001002")
			incomplete.SelectedProducers = nil
			client.spans[2] = incomplete
			client.latestSpan = incomplete
			store := NewSpanStore(client, nil, "1337", tc.rio)
			defer store.Close()

			span, err := store.spanByBlockNumber(t.Context(), tc.block)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				require.Nil(t, store.cachedSpan(2))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantId, span.Id)
		})
	}
}

func TestSpanStore_SpanRangeById_UsesCachedSpan(t *testing.T) {
	client := newCountingClient(contiguousRanges(3, 64), 0)
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	_, err := store.spanById(t.Context(), 1)
	require.NoError(t, err)
	r, err := store.spanRangeById(t.Context(), 1)
	require.NoError(t, err)
	require.Equal(t, spanRange{start: 256, end: 319}, r)
	require.Equal(t, 1, client.callsFor(1))
}
