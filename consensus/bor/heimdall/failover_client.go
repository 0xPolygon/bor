package heimdall

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"

	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/log"
)

const (
	defaultAttemptTimeout      = 30 * time.Second
	defaultHealthCheckInterval = 30 * time.Second
)

// Endpoint matches bor.IHeimdallClient. It is exported so that external
// packages can build []Endpoint slices for NewMultiHeimdallClient without
// running into Go's covariant-slice restriction.
type Endpoint interface {
	StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error)
	GetSpan(ctx context.Context, spanID uint64) (*types.Span, error)
	GetLatestSpan(ctx context.Context) (*types.Span, error)
	FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error)
	FetchCheckpointCount(ctx context.Context) (int64, error)
	FetchMilestone(ctx context.Context) (*milestone.Milestone, error)
	FetchMilestoneCount(ctx context.Context) (int64, error)
	FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error)
	Close()
}

// MultiHeimdallClient wraps N heimdall clients (primary at index 0, failovers
// at 1..N-1) and transparently cascades through them when the active client is
// unreachable. A background goroutine periodically health-checks higher-priority
// endpoints and promotes back when one recovers.
type MultiHeimdallClient struct {
	clients             []Endpoint
	mu                  sync.Mutex
	active              int // 0 = primary, >0 = failover
	attemptTimeout      time.Duration
	healthCheckInterval time.Duration
	quit                chan struct{}
	closeOnce           sync.Once
	probing             atomic.Bool
}

func NewMultiHeimdallClient(clients ...Endpoint) *MultiHeimdallClient {
	if len(clients) == 0 {
		panic("NewMultiHeimdallClient requires at least one client")
	}

	return &MultiHeimdallClient{
		clients:             clients,
		attemptTimeout:      defaultAttemptTimeout,
		healthCheckInterval: defaultHealthCheckInterval,
		quit:                make(chan struct{}),
	}
}

func (f *MultiHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) ([]*clerk.EventRecordWithTime, error) {
		return c.StateSyncEvents(ctx, fromID, to)
	})
}

func (f *MultiHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*types.Span, error) {
		return c.GetSpan(ctx, spanID)
	})
}

func (f *MultiHeimdallClient) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*types.Span, error) {
		return c.GetLatestSpan(ctx)
	})
}

func (f *MultiHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*checkpoint.Checkpoint, error) {
		return c.FetchCheckpoint(ctx, number)
	})
}

func (f *MultiHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (int64, error) {
		return c.FetchCheckpointCount(ctx)
	})
}

func (f *MultiHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*milestone.Milestone, error) {
		return c.FetchMilestone(ctx)
	})
}

func (f *MultiHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (int64, error) {
		return c.FetchMilestoneCount(ctx)
	})
}

func (f *MultiHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*ctypes.SyncInfo, error) {
		return c.FetchStatus(ctx)
	})
}

func (f *MultiHeimdallClient) Close() {
	f.closeOnce.Do(func() { close(f.quit) })

	for _, c := range f.clients {
		c.Close()
	}
}

// startHealthCheck runs in a background goroutine, periodically probing
// higher-priority endpoints. When one recovers, it promotes active and
// self-terminates. This keeps real requests off the probe path.
func (f *MultiHeimdallClient) startHealthCheck() {
	defer f.probing.Store(false)

	ticker := time.NewTicker(f.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-f.quit:
			return
		case <-ticker.C:
		}

		f.mu.Lock()
		active := f.active
		f.mu.Unlock()

		if active == 0 {
			// Already on primary, nothing to probe.
			return
		}

		// Probe clients 0..active-1 (highest priority first).
		for i := 0; i < active; i++ {
			failoverProbeAttempts.Inc(1)

			ctx, cancel := context.WithTimeout(context.Background(), f.attemptTimeout)
			_, err := f.clients[i].FetchStatus(ctx)
			cancel()

			if err == nil {
				f.mu.Lock()
				f.active = i
				f.mu.Unlock()

				failoverProbeSuccesses.Inc(1)
				failoverActiveGauge.Update(int64(i))

				log.Info("Heimdall health-check: promoted to higher-priority client", "index", i)

				if i == 0 {
					return
				}

				break // keep ticking to probe even higher-priority clients
			}
		}
	}
}

// callWithFailover executes fn against the active client. If the active client
// fails with a failover-eligible error, it cascades through remaining clients.
func callWithFailover[T any](f *MultiHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error)) (T, error) {
	f.mu.Lock()
	active := f.active
	f.mu.Unlock()

	subCtx, cancel := context.WithTimeout(ctx, f.attemptTimeout)
	result, err := fn(subCtx, f.clients[active])
	cancel()

	if err == nil {
		return result, nil
	}

	if !isFailoverError(err, ctx) {
		var zero T
		return zero, err
	}

	if active == 0 {
		log.Warn("Heimdall failover: primary failed, cascading to next client", "err", err)
	}

	return cascadeClients(f, ctx, fn, active, err)
}

// cascadeClients tries clients after the given index. On first success it
// switches the active client and returns. If all fail, returns the last error.
func cascadeClients[T any](f *MultiHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error), after int, lastErr error) (T, error) {
	for i := after + 1; i < len(f.clients); i++ {
		subCtx, cancel := context.WithTimeout(ctx, f.attemptTimeout)
		result, err := fn(subCtx, f.clients[i])
		cancel()

		if err == nil {
			f.mu.Lock()
			f.active = i
			f.mu.Unlock()

			failoverSwitchCounter.Inc(1)
			failoverActiveGauge.Update(int64(i))

			log.Warn("Heimdall failover: switched to client", "index", i)

			if i > 0 && f.probing.CompareAndSwap(false, true) {
				go f.startHealthCheck()
			}

			return result, nil
		}

		lastErr = err

		if !isFailoverError(err, ctx) {
			var zero T
			return zero, err
		}
	}

	var zero T
	return zero, lastErr
}

// isFailoverError returns true if the error warrants trying the secondary.
// It distinguishes between sub-context timeouts (failover-eligible) and
// caller context cancellation (not eligible).
func isFailoverError(err error, callerCtx context.Context) bool {
	if err == nil {
		return false
	}

	// If the caller's context is done, this is not a failover scenario
	if callerCtx.Err() != nil {
		return false
	}

	// Shutdown detected - not a transport error
	if errors.Is(err, ErrShutdownDetected) {
		return false
	}

	// 503 is a Heimdall feature-gate, not a transport issue
	if errors.Is(err, ErrServiceUnavailable) {
		return false
	}

	// Transport errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// No response from Heimdall
	if errors.Is(err, ErrNoResponse) {
		return true
	}

	// Server-side HTTP error (5xx, excluding 503 which is already handled above).
	// Client errors (4xx) are logical errors; the secondary would return the same response.
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}

	// Sub-context deadline exceeded (the caller's context is still alive at this point)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Context canceled from sub-context (caller ctx is still alive)
	if errors.Is(err, context.Canceled) {
		return true
	}

	return false
}
