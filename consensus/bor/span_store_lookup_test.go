package bor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/stretchr/testify/require"
)

type testSpanRange struct {
	start, end uint64
}

// countingClient counts GetSpan calls per span id and can delay each one, honoring ctx like the
// real clients do.
type countingClient struct {
	*dynamicHeimdallClient
	delay time.Duration
	// entered, when set, is closed on the first GetSpan call.
	entered     chan struct{}
	enteredOnce sync.Once
	// fail lists span ids whose fetch returns errSpanFetch; latestErr fails GetLatestSpan.
	fail      map[uint64]bool
	latestErr error

	mu    sync.Mutex
	calls map[uint64]int
}

var errSpanFetch = errors.New("span fetch failed")

func newCountingClient(ranges []testSpanRange, delay time.Duration) *countingClient {
	return &countingClient{dynamicHeimdallClient: newLookupClient(ranges), delay: delay, calls: make(map[uint64]int)}
}

func (c *countingClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	c.mu.Lock()
	c.calls[spanID]++
	c.mu.Unlock()
	if c.entered != nil {
		c.enteredOnce.Do(func() { close(c.entered) })
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(c.delay):
	}
	c.mu.Lock()
	failing := c.fail[spanID]
	c.mu.Unlock()
	if failing {
		return nil, errSpanFetch
	}
	return c.dynamicHeimdallClient.GetSpan(ctx, spanID)
}

func (c *countingClient) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	c.mu.Lock()
	latestErr := c.latestErr
	c.mu.Unlock()
	if latestErr != nil {
		return nil, latestErr
	}
	return c.dynamicHeimdallClient.GetLatestSpan(ctx)
}

func (c *countingClient) callsFor(id uint64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[id]
}

func (c *countingClient) totalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.calls {
		total += n
	}
	return total
}

func newLookupClient(ranges []testSpanRange) *dynamicHeimdallClient {
	spans := make(map[uint64]*types.Span, len(ranges))
	for id, r := range ranges {
		spans[uint64(id)] = makeTestSpan(uint64(id), r.start, r.end, fmt.Sprintf("0x%040x", 0x1000+id))
	}
	client := newDynamicHeimdallClient()
	client.setSpans(spans, spans[uint64(len(ranges)-1)])
	return client
}

// highestContaining is Heimdall's x/bor SpanByBlockNumber rule.
func highestContaining(ranges []testSpanRange, block uint64) (uint64, bool) {
	for id := len(ranges) - 1; id >= 0; id-- {
		if ranges[id].start <= block && block <= ranges[id].end {
			return uint64(id), true
		}
	}
	return 0, false
}

// Spans 0-3 are contiguous 6400-block spans, so lookupBlock sits in span 3.
var lookupBase = []testSpanRange{{0, 255}, {256, 6655}, {6656, 13055}, {13056, 19455}}

const lookupBlock = uint64(15000)

var lookupLayouts = []struct {
	name  string
	later []testSpanRange
	want  uint64
}{
	{
		name:  "rotation behind three look-ahead spans",
		later: []testSpanRange{{19456, 25855}, {25856, 32255}, {32256, 38655}, {14000, 19455}},
		want:  7,
	},
	{
		name:  "stall recovery behind look-ahead and downtime spans",
		later: []testSpanRange{{19456, 25855}, {20000, 26400}, {14001, 25855}},
		want:  6,
	},
	{
		name:  "overlapping span behind two later-starting spans",
		later: []testSpanRange{{15001, 21401}, {15002, 21402}, {14500, 20900}},
		want:  6,
	},
	{
		name:  "descending planned downtime starts",
		later: []testSpanRange{{lookupBlock + 100, lookupBlock + 6500}, {lookupBlock + 50, lookupBlock + 6450}, {lookupBlock, lookupBlock + 6400}},
		want:  6,
	},
	{
		name:  "no later span contains the block",
		later: []testSpanRange{{19456, 25855}, {15001, 21401}, {15002, 21402}},
		want:  3,
	},
}

func lookupRanges(later []testSpanRange) []testSpanRange {
	return append(append([]testSpanRange{}, lookupBase...), later...)
}

func TestSpanStore_SpanByBlockNumber_HighestIdContaining(t *testing.T) {
	for _, tc := range lookupLayouts {
		t.Run(tc.name, func(t *testing.T) {
			ranges := lookupRanges(tc.later)
			want, ok := highestContaining(ranges, lookupBlock)
			require.True(t, ok)
			require.Equal(t, tc.want, want)

			store := NewSpanStore(newLookupClient(ranges), nil, "1337", nil)
			defer store.Close()

			span, err := store.spanByBlockNumber(t.Context(), lookupBlock)
			require.NoError(t, err)
			require.Equal(t, tc.want, span.Id)
		})
	}
}

// contiguousRanges returns n spans after span 0, each length blocks long, as Heimdall creates them before Rio.
func contiguousRanges(n int, length uint64) []testSpanRange {
	ranges := []testSpanRange{{0, 255}}
	for i := 0; i < n; i++ {
		start := ranges[len(ranges)-1].end + 1
		ranges = append(ranges, testSpanRange{start, start + length - 1})
	}
	return ranges
}

// boundaryBlocks returns every span's start and end, one block either side of them, and block 0.
func boundaryBlocks(ranges []testSpanRange) []uint64 {
	blocks := []uint64{0}
	for _, r := range ranges {
		blocks = append(blocks, r.start, r.end, r.end+1)
		if r.start > 0 {
			blocks = append(blocks, r.start-1)
		}
	}
	return blocks
}

func TestSpanStore_SpanByBlockNumber_MatchesFullWalkWithRio(t *testing.T) {
	type layout struct {
		name   string
		ranges []testSpanRange
		rio    uint64
	}
	var layouts []layout
	for _, tc := range lookupLayouts {
		// Spans created from Rio on start at or after it, so Rio is at most the lowest later start.
		rio := tc.later[0].start
		for _, r := range tc.later {
			rio = min(rio, r.start)
		}
		layouts = append(layouts, layout{tc.name, lookupRanges(tc.later), rio})
	}
	preRio := contiguousRanges(40, 64)
	rio := preRio[len(preRio)-1].start + 10
	layouts = append(layouts, layout{
		name:   "rio inside the last contiguous span",
		ranges: append(preRio, testSpanRange{rio + 5, rio + 70}, testSpanRange{rio, rio + 30}, testSpanRange{rio + 71, rio + 134}),
		rio:    rio,
	})
	layouts = append(layouts, layout{
		name:   "rio at a span boundary",
		ranges: append(contiguousRanges(7, 100), testSpanRange{956, 1055}, testSpanRange{960, 1100}),
		rio:    956,
	})

	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			warm := NewSpanStore(newLookupClient(l.ranges), nil, "1337", new(big.Int).SetUint64(l.rio))
			defer warm.Close()

			for _, block := range boundaryBlocks(l.ranges) {
				want, ok := highestContaining(l.ranges, block)

				cold := NewSpanStore(newLookupClient(l.ranges), nil, "1337", new(big.Int).SetUint64(l.rio))
				require.NoError(t, cold.updateHeimdallStatus(t.Context()))
				coldSpan, coldErr := cold.spanByBlockNumber(t.Context(), block)
				cold.Close()
				warmSpan, warmErr := warm.spanByBlockNumber(t.Context(), block)

				if !ok {
					require.Error(t, coldErr, "block %d", block)
					require.Error(t, warmErr, "block %d", block)
					continue
				}
				require.NoError(t, coldErr, "block %d", block)
				require.NoError(t, warmErr, "block %d", block)
				require.Equal(t, want, coldSpan.Id, "cold store, block %d", block)
				require.Equal(t, want, warmSpan.Id, "warm store, block %d", block)
			}
		})
	}
}

func TestSpanStore_SpanByBlockNumber_PreRioLookupFetchesFewSpans(t *testing.T) {
	preRio := contiguousRanges(2000, 64)
	rio := preRio[len(preRio)-1].end + 1
	ranges := append(preRio, testSpanRange{rio, rio + 63}, testSpanRange{rio + 10, rio + 63}, testSpanRange{rio + 64, rio + 127})

	client := newCountingClient(ranges, 0)
	store := NewSpanStore(client, nil, "1337", new(big.Int).SetUint64(rio))
	defer store.Close()

	block := ranges[5].start + 1
	span, err := store.spanByBlockNumber(t.Context(), block)
	require.NoError(t, err)
	require.Equal(t, uint64(5), span.Id)
	// A binary search over ~2000 ids touches about log2(2000) of them.
	require.LessOrEqual(t, client.totalCalls(), 16)
}

func TestSpanStore_SpanByBlockNumber_ConcurrentLookupsShareFetches(t *testing.T) {
	ranges := contiguousRanges(200, 64)
	client := newCountingClient(ranges, time.Millisecond)
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	block := ranges[1].start
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			span, err := store.spanByBlockNumber(context.Background(), block)
			if err == nil && span.Id != 1 {
				err = fmt.Errorf("got span %d", span.Id)
			}
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	client.mu.Lock()
	defer client.mu.Unlock()
	for id, n := range client.calls {
		require.Equal(t, 1, n, "span %d fetched %d times", id, n)
	}
}

func TestSpanStore_SpanRangeById_CallerDeadlineDoesNotFailSharedFetch(t *testing.T) {
	ranges := contiguousRanges(3, 64)
	client := newCountingClient(ranges, 50*time.Millisecond)
	client.entered = make(chan struct{})
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	errs := make(chan error, 2)
	go func() {
		_, err := store.spanRangeById(short, 2)
		errs <- err
	}()
	<-client.entered
	r, err := store.spanRangeById(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, ranges[2].start, r.start)
	require.ErrorIs(t, <-errs, context.DeadlineExceeded)
}

func TestSpanStore_SpanByBlockNumber_IndependentOfHistory(t *testing.T) {
	testcases := []struct {
		name   string
		ranges []testSpanRange
		warmAt uint64
		block  uint64
	}{
		{
			name:   "overlap behind two later-starting spans",
			ranges: []testSpanRange{{0, 255}, {256, 755}, {760, 800}, {770, 810}, {700, 800}},
			warmAt: 790,
			block:  720,
		},
		{
			name:   "rotation ending before an older look-ahead span",
			ranges: []testSpanRange{{0, 99}, {100, 199}, {200, 299}, {300, 399}, {150, 299}},
			warmAt: 200,
			block:  300,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := highestContaining(tc.ranges, tc.block)
			require.True(t, ok)
			client := newLookupClient(tc.ranges)

			cold := NewSpanStore(client, nil, "1337", nil)
			coldSpan, err := cold.spanByBlockNumber(t.Context(), tc.block)
			cold.Close()
			require.NoError(t, err, "cold store")
			require.Equal(t, want, coldSpan.Id, "cold store")

			warm := NewSpanStore(client, nil, "1337", nil)
			for id := range tc.ranges {
				_, err := warm.spanById(t.Context(), uint64(id))
				require.NoError(t, err)
			}
			_, err = warm.spanByBlockNumber(t.Context(), tc.warmAt)
			require.NoError(t, err)
			warmSpan, err := warm.spanByBlockNumber(t.Context(), tc.block)
			warm.Close()
			require.NoError(t, err, "warm store")
			require.Equal(t, want, warmSpan.Id, "warm store")
			require.Equal(t, coldSpan.SelectedProducers[0].Signer, warmSpan.SelectedProducers[0].Signer)

			restarted := NewSpanStore(client, nil, "1337", nil)
			defer restarted.Close()
			restartedSpan, err := restarted.spanByBlockNumber(t.Context(), tc.block)
			require.NoError(t, err, "restarted store")
			require.Equal(t, warmSpan.Id, restartedSpan.Id, "restarted store")
		})
	}
}

func TestSpanStore_SpanByBlockNumber_UnmatchedPostRioBlockStopsAtRio(t *testing.T) {
	preRio := contiguousRanges(2000, 64)
	rio := preRio[len(preRio)-1].end + 1
	ranges := append(preRio, testSpanRange{rio, rio + 63}, testSpanRange{rio + 10, rio + 63}, testSpanRange{rio + 64, rio + 127})

	client := newCountingClient(ranges, 0)
	store := NewSpanStore(client, nil, "1337", new(big.Int).SetUint64(rio))
	defer store.Close()

	_, err := store.spanByBlockNumber(t.Context(), rio+128)
	require.ErrorContains(t, err, "span not found")
	// The three post-Rio spans plus the last pre-Rio one.
	require.LessOrEqual(t, client.totalCalls(), 4)
}

func TestSpanStore_SpanByBlockNumber_PreRioSearchResults(t *testing.T) {
	// Spans 0-16, all before Rio.
	ranges := contiguousRanges(16, 64)
	rio := ranges[len(ranges)-1].end + 1000

	testcases := []struct {
		name      string
		block     uint64
		fail      map[uint64]bool
		wantId    uint64
		wantErr   error
		wantMsg   string
		wantCalls int
	}{
		{
			name:      "found",
			block:     ranges[5].start + 1,
			wantId:    5,
			wantCalls: 4,
		},
		{
			name:      "fetch fails during the search",
			block:     ranges[5].start + 1,
			fail:      map[uint64]bool{8: true},
			wantErr:   errSpanFetch,
			wantCalls: 1,
		},
		{
			name:      "fetch of the final span fails",
			block:     10,
			fail:      map[uint64]bool{0: true},
			wantErr:   errSpanFetch,
			wantCalls: 5,
		},
		{
			name:      "block after the latest span",
			block:     ranges[len(ranges)-1].end + 1,
			wantMsg:   fmt.Sprintf("span not found for block %d", ranges[len(ranges)-1].end+1),
			wantCalls: 5,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newCountingClient(ranges, 0)
			client.fail = tc.fail
			store := NewSpanStore(client, nil, "1337", new(big.Int).SetUint64(rio))
			defer store.Close()

			span, err := store.spanByBlockNumber(t.Context(), tc.block)
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantMsg != "":
				require.EqualError(t, err, tc.wantMsg)
			default:
				require.NoError(t, err)
				require.Equal(t, tc.wantId, span.Id)
			}
			require.Equal(t, tc.wantCalls, client.totalCalls())
		})
	}
}

func TestSpanStore_SpanByBlockNumber_WalkResults(t *testing.T) {
	gapped := []testSpanRange{{0, 255}, {256, 511}, {600, 700}}
	preRio := contiguousRanges(5, 64)
	rio := preRio[len(preRio)-1].end + 1
	withRio := append(append([]testSpanRange{}, preRio...), testSpanRange{rio, rio + 63}, testSpanRange{rio + 10, rio + 63})

	testcases := []struct {
		name      string
		ranges    []testSpanRange
		rio       *big.Int
		block     uint64
		wantId    uint64
		wantMsg   string
		wantCalls int
	}{
		{
			name:      "found below a later span",
			ranges:    gapped,
			block:     300,
			wantId:    1,
			wantCalls: 2,
		},
		{
			name:      "no span contains the block",
			ranges:    gapped,
			block:     550,
			wantMsg:   "span not found for block 550",
			wantCalls: 3,
		},
		{
			name:      "stops at the first pre-Rio span ending below the block",
			ranges:    withRio,
			rio:       new(big.Int).SetUint64(rio),
			block:     rio + 64,
			wantMsg:   fmt.Sprintf("span not found for block %d", rio+64),
			wantCalls: 3,
		},
		{
			name:      "post-Rio span found above the pre-Rio ones",
			ranges:    withRio,
			rio:       new(big.Int).SetUint64(rio),
			block:     rio + 5,
			wantId:    6,
			wantCalls: 2,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			client := newCountingClient(tc.ranges, 0)
			store := NewSpanStore(client, nil, "1337", tc.rio)
			defer store.Close()

			span, err := store.spanByBlockNumber(t.Context(), tc.block)
			if tc.wantMsg != "" {
				require.EqualError(t, err, tc.wantMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantId, span.Id)
			}
			require.Equal(t, tc.wantCalls, client.totalCalls())
		})
	}
}

func TestSpanStore_SpanByBlockNumber_LatestSpanError(t *testing.T) {
	errLatest := errors.New("latest span unavailable")
	client := newCountingClient(contiguousRanges(3, 64), 0)
	client.latestErr = errLatest
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	_, err := store.spanByBlockNumber(t.Context(), 10)
	require.ErrorIs(t, err, errLatest)
	require.Zero(t, client.totalCalls())
}

func TestSpanStore_SpanByBlockNumber_ReusesRangesAfterSpanEviction(t *testing.T) {
	preRio := contiguousRanges(2000, 64)
	rio := preRio[len(preRio)-1].end + 1
	client := newCountingClient(preRio, 0)
	store := NewSpanStore(client, nil, "1337", new(big.Int).SetUint64(rio))
	defer store.Close()

	blockA, blockB := preRio[5].start+1, preRio[1500].start+1
	for _, block := range []uint64{blockA, blockB} {
		_, err := store.spanByBlockNumber(t.Context(), block)
		require.NoError(t, err)
	}

	// The second search evicted the first one's spans, but not their ranges.
	before := client.totalCalls()
	span, err := store.spanByBlockNumber(t.Context(), blockA)
	require.NoError(t, err)
	require.Equal(t, uint64(5), span.Id)
	require.LessOrEqual(t, client.totalCalls()-before, 1)
}

func TestSpanStore_SpanByBlockNumber_KeepsRangesWhenLatestSpanGrows(t *testing.T) {
	ranges := contiguousRanges(20, 64)
	client := newCountingClient(ranges, 0)
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()

	block := ranges[5].start + 1
	span, err := store.spanByBlockNumber(t.Context(), block)
	require.NoError(t, err)
	require.Equal(t, uint64(5), span.Id)
	require.Equal(t, 16, client.totalCalls())

	last := ranges[len(ranges)-1]
	grown := newLookupClient(append(ranges, testSpanRange{last.end + 1, last.end + 64}))
	client.setSpans(grown.spans, grown.latestSpan)
	require.NoError(t, store.updateLatestSpan(t.Context()))

	// Only the new span is fetched; the walk below it is served from the recorded ranges.
	before := client.totalCalls()
	span, err = store.spanByBlockNumber(t.Context(), block)
	require.NoError(t, err)
	require.Equal(t, uint64(5), span.Id)
	require.Equal(t, 1, client.callsFor(21))
	require.LessOrEqual(t, client.totalCalls()-before, 2)
}

func TestSpanStore_PurgeCache_DropsSpanRanges(t *testing.T) {
	store := NewSpanStore(newLookupClient([]testSpanRange{{0, 255}, {256, 511}}), nil, "1337", nil)
	defer store.Close()

	span, err := store.spanByBlockNumber(t.Context(), 300)
	require.NoError(t, err)
	require.Equal(t, uint64(1), span.Id)

	store.PurgeCache()
	store.setHeimdallClient(newLookupClient([]testSpanRange{{0, 255}, {256, 600}}))

	type result struct {
		span *types.Span
		err  error
	}
	done := make(chan result, 1)
	go func() {
		span, err := store.spanByBlockNumber(context.Background(), 550)
		done <- result{span, err}
	}()
	select {
	case res := <-done:
		require.NoError(t, res.err)
		require.Equal(t, uint64(1), res.span.Id)
		require.Equal(t, uint64(600), res.span.EndBlock)
	case <-time.After(10 * time.Second):
		t.Fatal("lookup after PurgeCache did not return")
	}
}

func TestSpanStore_LatestSpanNeverGoesBack(t *testing.T) {
	// Span 4 is a rotation span over span 3's tail, so lookupBlock resolves to it.
	ranges := lookupRanges([]testSpanRange{{14000, 19455}})
	client := newCountingClient(ranges, 0)
	store := NewSpanStore(client, nil, "1337", nil)
	defer store.Close()
	store.stopPollLoop()

	span, err := store.spanByBlockNumber(t.Context(), lookupBlock)
	require.NoError(t, err)
	require.Equal(t, uint64(4), span.Id)

	// A failover endpoint one span behind reports span 3 as the latest.
	client.setSpans(client.spans, client.spans[3])
	require.NoError(t, store.updateLatestSpan(t.Context()))
	require.Equal(t, uint64(4), store.latestSpanCache.Load().Id)

	span, err = store.spanByBlockNumber(t.Context(), lookupBlock)
	require.NoError(t, err)
	require.Equal(t, uint64(4), span.Id)

	// After PurgeCache a lower latest span is accepted again.
	store.PurgeCache()
	require.NoError(t, store.updateLatestSpan(t.Context()))
	require.Equal(t, uint64(3), store.latestSpanCache.Load().Id)
}
