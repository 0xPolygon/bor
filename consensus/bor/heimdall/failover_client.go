package heimdall

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"

	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/log"
)

const (
	defaultAttemptTimeout    = 30 * time.Second
	defaultSecondaryCooldown = 2 * time.Minute
)

// Endpoint matches bor.IHeimdallClient. It is exported so that external
// packages can build []Endpoint slices for NewFailoverHeimdallClient without
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

// FailoverHeimdallClient wraps N heimdall clients (primary at index 0, failovers
// at 1..N-1) and transparently cascades through them when the active client is
// unreachable. After a cooldown period it probes the primary again.
type FailoverHeimdallClient struct {
	clients        []Endpoint
	mu             sync.Mutex
	active         int       // 0 = primary, >0 = failover
	lastSwitch     time.Time // when we last switched away from primary
	attemptTimeout time.Duration
	cooldown       time.Duration
}

func NewFailoverHeimdallClient(clients ...Endpoint) *FailoverHeimdallClient {
	return &FailoverHeimdallClient{
		clients:        clients,
		attemptTimeout: defaultAttemptTimeout,
		cooldown:       defaultSecondaryCooldown,
	}
}

func (f *FailoverHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) ([]*clerk.EventRecordWithTime, error) {
		return c.StateSyncEvents(ctx, fromID, to)
	})
}

func (f *FailoverHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*types.Span, error) {
		return c.GetSpan(ctx, spanID)
	})
}

func (f *FailoverHeimdallClient) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*types.Span, error) {
		return c.GetLatestSpan(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*checkpoint.Checkpoint, error) {
		return c.FetchCheckpoint(ctx, number)
	})
}

func (f *FailoverHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (int64, error) {
		return c.FetchCheckpointCount(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*milestone.Milestone, error) {
		return c.FetchMilestone(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (int64, error) {
		return c.FetchMilestoneCount(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c Endpoint) (*ctypes.SyncInfo, error) {
		return c.FetchStatus(ctx)
	})
}

func (f *FailoverHeimdallClient) Close() {
	for _, c := range f.clients {
		c.Close()
	}
}

// callWithFailover executes fn against the active client. If the active client
// fails with a failover-eligible error, it cascades through remaining clients.
// If on a non-primary client past the cooldown, it probes the primary first.
func callWithFailover[T any](f *FailoverHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error)) (T, error) {
	f.mu.Lock()
	active := f.active
	shouldProbe := active != 0 && time.Since(f.lastSwitch) >= f.cooldown
	f.mu.Unlock()

	// If on a non-primary client and cooldown has elapsed, probe primary
	if shouldProbe {
		subCtx, cancel := context.WithTimeout(ctx, f.attemptTimeout)
		result, err := fn(subCtx, f.clients[0])
		cancel()

		if err == nil {
			f.mu.Lock()
			f.active = 0
			f.mu.Unlock()

			log.Info("Heimdall failover: primary recovered, switching back")

			return result, nil
		}

		if !isFailoverError(err, ctx) {
			var zero T
			return zero, err
		}

		// Primary still down, stay on current client
		f.mu.Lock()
		f.lastSwitch = time.Now()
		f.mu.Unlock()

		log.Debug("Heimdall failover: primary still down after probe, staying on current", "active", active, "err", err)

		// Try current client, then cascade through remaining on failure
		result, err = fn(ctx, f.clients[active])
		if err == nil {
			return result, nil
		}

		if !isFailoverError(err, ctx) {
			var zero T
			return zero, err
		}

		return cascadeClients(f, ctx, fn, active, err)
	}

	if active != 0 {
		// On a non-primary client, not yet time to probe: use current directly
		result, err := fn(ctx, f.clients[active])
		if err == nil {
			return result, nil
		}

		if !isFailoverError(err, ctx) {
			var zero T
			return zero, err
		}

		return cascadeClients(f, ctx, fn, active, err)
	}

	// Active is primary: try with timeout
	subCtx, cancel := context.WithTimeout(ctx, f.attemptTimeout)
	result, err := fn(subCtx, f.clients[0])
	cancel()

	if err == nil {
		return result, nil
	}

	if !isFailoverError(err, ctx) {
		var zero T
		return zero, err
	}

	// Cascade through clients [1, 2, ..., N-1]
	log.Warn("Heimdall failover: primary failed, cascading to next client", "err", err)

	return cascadeClients(f, ctx, fn, 0, err)
}

// cascadeClients tries clients after the given index. On first success it
// switches the active client and returns. If all fail, returns the last error.
func cascadeClients[T any](f *FailoverHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error), after int, lastErr error) (T, error) {
	for i := after + 1; i < len(f.clients); i++ {
		result, err := fn(ctx, f.clients[i])
		if err == nil {
			f.mu.Lock()
			f.active = i
			f.lastSwitch = time.Now()
			f.mu.Unlock()

			log.Warn("Heimdall failover: switched to client", "index", i)

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

	// Non-successful HTTP response (4xx, 5xx excluding 503)
	if errors.Is(err, ErrNotSuccessfulResponse) {
		return true
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
