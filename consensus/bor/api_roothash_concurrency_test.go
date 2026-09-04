package bor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

// gatingChain pauses and counts reads of one block, so a test can hold the
// computation open while callers pile up, then check how many actually ran.
type gatingChain struct {
	*rawdbChain

	gateBlock    uint64 // counted/gated; read only during the range fetch
	computeCount atomic.Int64
	started      chan struct{} // closed when the first gated read begins
	release      chan struct{} // unblocks gated reads when closed
	once         sync.Once
}

func (c *gatingChain) GetHeaderByNumber(number uint64) *types.Header {
	if number == c.gateBlock {
		c.computeCount.Add(1)
		c.once.Do(func() { close(c.started) })
		<-c.release
	}

	return c.rawdbChain.GetHeaderByNumber(number)
}

// TestGetRootHashSingleFlight: concurrent calls for the same range collapse into
// a single computation, and all callers get the same root.
func TestGetRootHashSingleFlight(t *testing.T) {
	base := newTestRawDBChain(t)
	base.reorgToChainA()

	// Gate block 1: read only during the range fetch, never in the endHash
	// pre-read (end=3), so callers reach singleflight before the gate fires.
	chain := &gatingChain{
		rawdbChain: base,
		gateBlock:  1,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}

	api := &API{chain: chain}
	// Eager-init the cache like Bor.APIs does, so the concurrent callers below
	// don't race the lazy init.
	require.NoError(t, api.initializeRootHashCache())

	const callers = 8

	results := make([]struct {
		root string
		err  error
	}, callers)

	var wg sync.WaitGroup

	wg.Add(callers)
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			results[i].root, results[i].err = api.GetRootHash(1, 3)
		}(i)
	}

	// Let the computation start and the others park in singleflight, then release it.
	<-chain.started
	time.Sleep(200 * time.Millisecond)
	close(chain.release)

	wg.Wait()

	require.Equal(t, int64(1), chain.computeCount.Load(), "concurrent same-range calls must trigger one computation")

	for i := range results {
		require.NoError(t, results[i].err)
		require.Equal(t, results[0].root, results[i].root, "all callers must get the same root")
	}

	require.NotEmpty(t, results[0].root)
}
