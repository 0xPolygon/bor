package rpc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// submitBlockingTasks submits n tasks that block until release is closed,
// reporting live and peak concurrency.
func submitBlockingTasks(pool *SafePool, n int) (current, peak *atomic.Int64, release chan struct{}, wg *sync.WaitGroup) {
	current = new(atomic.Int64)
	peak = new(atomic.Int64)
	release = make(chan struct{})
	wg = new(sync.WaitGroup)
	wg.Add(n)

	task := func() error {
		cur := current.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}

		<-release

		current.Add(-1)
		wg.Done()

		return nil
	}

	for range n {
		go pool.Submit(context.Background(), task)
	}

	return current, peak, release, wg
}

func waitForAtomic(t *testing.T, v *atomic.Int64, want int64, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.Load() == want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("atomic value %d did not reach %d within %v", v.Load(), want, timeout)
}

// TestExecutionPoolSizeZeroUsesFastPath: a size-0 pool runs tasks unbounded.
func TestExecutionPoolSizeZeroUsesFastPath(t *testing.T) {
	pool := NewExecutionPool(0, 0, "", false)
	assert.True(t, pool.fastPath.Load(), "size-0 pool must use the fast path")

	const submitted = 64

	current, peak, release, wg := submitBlockingTasks(pool, submitted)
	waitForAtomic(t, current, submitted, 5*time.Second)
	assert.Equal(t, int64(submitted), peak.Load(), "fast path must run all tasks concurrently (unbounded)")

	close(release)
	wg.Wait()
}

// TestExecutionPoolSizedIsBounded: a sized pool caps concurrency at its size.
func TestExecutionPoolSizedIsBounded(t *testing.T) {
	const size = 8

	pool := NewExecutionPool(size, 0, "", false)
	defer pool.Stop()

	assert.False(t, pool.fastPath.Load(), "sized pool must not use the fast path")

	const submitted = 64

	current, peak, release, wg := submitBlockingTasks(pool, submitted)
	waitForAtomic(t, current, size, 5*time.Second)
	assert.Equal(t, int64(size), current.Load(), "exactly size tasks should run concurrently")
	assert.LessOrEqual(t, peak.Load(), int64(size), "concurrency must never exceed the pool size")

	close(release)
	wg.Wait()
	assert.LessOrEqual(t, peak.Load(), int64(size))
}

// TestChangeSizeClearsFastPath: raising size from 0 must bound a fast-path pool
// (regression for the inert admin_setHttpExecutionPoolSize override).
func TestChangeSizeClearsFastPath(t *testing.T) {
	pool := NewExecutionPool(0, 0, "", false)
	assert.True(t, pool.fastPath.Load())

	const size = 8

	pool.ChangeSize(size)
	defer pool.Stop()

	assert.False(t, pool.fastPath.Load(), "ChangeSize to a positive size must clear the fast path")
	assert.Equal(t, size, pool.Size())

	const submitted = 64

	current, peak, release, wg := submitBlockingTasks(pool, submitted)
	waitForAtomic(t, current, size, 5*time.Second)
	assert.LessOrEqual(t, peak.Load(), int64(size), "concurrency must be bounded after ChangeSize")

	close(release)
	wg.Wait()
}

// TestChangeSizeZeroReentersFastPath: lowering size to 0 returns to the fast path.
func TestChangeSizeZeroReentersFastPath(t *testing.T) {
	pool := NewExecutionPool(4, 0, "", false)
	assert.False(t, pool.fastPath.Load())

	pool.ChangeSize(0)
	assert.True(t, pool.fastPath.Load(), "ChangeSize(0) must re-enter the fast path")
	assert.Equal(t, 0, pool.Size())
}

// TestSubmitShedsAtTimeout: at capacity, the configured timeout lets Submit run
// fn unbounded instead of blocking the caller indefinitely.
func TestSubmitShedsAtTimeout(t *testing.T) {
	pool := NewExecutionPool(1, 50*time.Millisecond, "", false)
	defer pool.Stop()

	current, _, release, wg := submitBlockingTasks(pool, 1) // occupies the only slot
	waitForAtomic(t, current, 1, time.Second)

	ran := make(chan struct{})
	go pool.Submit(context.Background(), func() error {
		close(ran)
		return nil
	})

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("saturated pool must shed fn after the timeout, not block forever")
	}

	close(release)
	wg.Wait()
}

// TestSubmitShedsOnCtxCancel: a cancelled ctx lets Submit run fn unbounded
// instead of blocking on a saturated pool (timeout disabled).
func TestSubmitShedsOnCtxCancel(t *testing.T) {
	pool := NewExecutionPool(1, 0, "", false)
	defer pool.Stop()

	current, _, release, wg := submitBlockingTasks(pool, 1)
	waitForAtomic(t, current, 1, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := make(chan struct{})
	go pool.Submit(ctx, func() error {
		close(ran)
		return nil
	})

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("saturated pool must shed fn when ctx is cancelled")
	}

	close(release)
	wg.Wait()
}
