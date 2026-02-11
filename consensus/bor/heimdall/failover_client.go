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

// heimdallClient is a local interface matching bor.IHeimdallClient to avoid
// an import cycle with the consensus/bor package.
type heimdallClient interface {
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

// FailoverHeimdallClient wraps two heimdall clients (primary + secondary) and
// transparently fails over from primary to secondary when the primary is
// unreachable. After a cooldown period it probes the primary again.
type FailoverHeimdallClient struct {
	clients        [2]heimdallClient
	mu             sync.Mutex
	active         int       // 0 = primary, 1 = secondary
	lastSwitch     time.Time // when we last switched to secondary
	attemptTimeout time.Duration
	cooldown       time.Duration
}

func NewFailoverHeimdallClient(primary, secondary heimdallClient) *FailoverHeimdallClient {
	return &FailoverHeimdallClient{
		clients:        [2]heimdallClient{primary, secondary},
		attemptTimeout: defaultAttemptTimeout,
		cooldown:       defaultSecondaryCooldown,
	}
}

func (f *FailoverHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) ([]*clerk.EventRecordWithTime, error) {
		return c.StateSyncEvents(ctx, fromID, to)
	})
}

func (f *FailoverHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (*types.Span, error) {
		return c.GetSpan(ctx, spanID)
	})
}

func (f *FailoverHeimdallClient) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (*types.Span, error) {
		return c.GetLatestSpan(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (*checkpoint.Checkpoint, error) {
		return c.FetchCheckpoint(ctx, number)
	})
}

func (f *FailoverHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (int64, error) {
		return c.FetchCheckpointCount(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (*milestone.Milestone, error) {
		return c.FetchMilestone(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (int64, error) {
		return c.FetchMilestoneCount(ctx)
	})
}

func (f *FailoverHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return callWithFailover(f, ctx, func(ctx context.Context, c heimdallClient) (*ctypes.SyncInfo, error) {
		return c.FetchStatus(ctx)
	})
}

func (f *FailoverHeimdallClient) Close() {
	f.clients[0].Close()
	f.clients[1].Close()
}

// callWithFailover executes fn against the active client. If the active client
// is primary and the call fails with a failover-eligible error, it retries on
// the secondary. If on secondary past the cooldown, it probes the primary first.
func callWithFailover[T any](f *FailoverHeimdallClient, ctx context.Context, fn func(context.Context, heimdallClient) (T, error)) (T, error) {
	f.mu.Lock()
	active := f.active
	shouldProbe := active == 1 && time.Since(f.lastSwitch) >= f.cooldown
	f.mu.Unlock()

	// If on secondary and cooldown has elapsed, probe primary
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

		// Primary still down, stay on secondary
		f.mu.Lock()
		f.lastSwitch = time.Now()
		f.mu.Unlock()

		log.Debug("Heimdall failover: primary still down after probe, staying on secondary", "err", err)

		// Secondary calls use the caller's ctx directly (no sub-timeout).
		// The timeout is only needed on primary to bound the failover decision.
		// Once on secondary there is no further fallback, so the caller's
		// context (which always has a cancellation path in Bor) governs lifetime.
		return fn(ctx, f.clients[1])
	}

	if active == 1 {
		// On secondary, not yet time to probe: use secondary directly
		return fn(ctx, f.clients[1])
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

	// Failover to secondary
	f.mu.Lock()
	f.active = 1
	f.lastSwitch = time.Now()
	f.mu.Unlock()

	log.Warn("Heimdall failover: primary failed, switching to secondary", "err", err)

	return fn(ctx, f.clients[1])
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
