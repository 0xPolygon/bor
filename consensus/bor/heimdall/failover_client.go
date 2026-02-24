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
	defaultAttemptTimeout       = 30 * time.Second
	defaultHealthCheckInterval  = 10 * time.Second
	defaultConsecutiveThreshold = 3
	defaultPromotionCooldown    = 60 * time.Second
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

// endpointHealth tracks the health state of a single endpoint.
type endpointHealth struct {
	healthy            bool
	consecutiveSuccess int
	healthySince       time.Time // when consecutive threshold was reached
	lastErr            error
}

// MultiHeimdallClient wraps N heimdall clients (primary at index 0, failovers
// at 1..N-1) and transparently cascades through them when the active client is
// unreachable. A background health registry continuously probes ALL endpoints,
// requires consecutive successes + cooldown before promotion, and gives cascade
// full visibility into endpoint health.
type MultiHeimdallClient struct {
	clients              []Endpoint
	mu                   sync.Mutex
	active               int // 0 = primary, >0 = failover
	health               []endpointHealth
	attemptTimeout       time.Duration
	healthCheckInterval  time.Duration
	consecutiveThreshold int
	promotionCooldown    time.Duration
	quit                 chan struct{}
	closeOnce            sync.Once
	startOnce            sync.Once
	probeCtx             context.Context    // cancelled on Close to abort in-flight probes
	probeCancel          context.CancelFunc
}

func NewMultiHeimdallClient(clients ...Endpoint) *MultiHeimdallClient {
	if len(clients) == 0 {
		panic("NewMultiHeimdallClient requires at least one client")
	}

	health := make([]endpointHealth, len(clients))
	// Primary starts as healthy; others start unhealthy.
	health[0] = endpointHealth{healthy: true}

	probeCtx, probeCancel := context.WithCancel(context.Background())

	return &MultiHeimdallClient{
		clients:              clients,
		health:               health,
		attemptTimeout:       defaultAttemptTimeout,
		healthCheckInterval:  defaultHealthCheckInterval,
		consecutiveThreshold: defaultConsecutiveThreshold,
		promotionCooldown:    defaultPromotionCooldown,
		quit:                 make(chan struct{}),
		probeCtx:             probeCtx,
		probeCancel:          probeCancel,
	}
}

// ensureHealthRegistry lazily starts the health registry goroutine on the first
// API call. This allows tests to configure fields (thresholds, intervals) after
// construction but before the goroutine reads them.
func (f *MultiHeimdallClient) ensureHealthRegistry() {
	if len(f.clients) > 1 {
		f.startOnce.Do(func() {
			go f.runHealthRegistry()
		})
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
	f.closeOnce.Do(func() {
		f.probeCancel() // cancel in-flight probes first
		close(f.quit)
	})

	for _, c := range f.clients {
		c.Close()
	}
}

// runHealthRegistry is an always-on goroutine (started in constructor, stopped
// on Close) that continuously probes ALL endpoints, requires consecutive
// successes before marking healthy, and enforces cooldown before promotion.
func (f *MultiHeimdallClient) runHealthRegistry() {
	ticker := time.NewTicker(f.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-f.quit:
			return
		case <-ticker.C:
		}

		f.probeAllEndpoints()
		f.maybePromote()
		f.maybeProactiveSwitch()
	}
}

// probeAllEndpoints probes every endpoint via FetchStatus and updates health state.
func (f *MultiHeimdallClient) probeAllEndpoints() {
	for i := 0; i < len(f.clients); i++ {
		// Check for shutdown between individual probes so we don't
		// burn N*timeout before noticing Close() was called.
		select {
		case <-f.quit:
			return
		default:
		}

		failoverProbeAttempts.Inc(1)

		ctx, cancel := context.WithTimeout(f.probeCtx, f.attemptTimeout)
		_, err := f.clients[i].FetchStatus(ctx)
		cancel()

		f.mu.Lock()

		if err == nil {
			f.health[i].consecutiveSuccess++
			f.health[i].lastErr = nil

			if f.health[i].consecutiveSuccess >= f.consecutiveThreshold && !f.health[i].healthy {
				f.health[i].healthy = true
				f.health[i].healthySince = time.Now()
			}

			failoverProbeSuccesses.Inc(1)
		} else {
			// Fast failure detection: one failure resets to unhealthy.
			f.health[i].consecutiveSuccess = 0
			f.health[i].healthy = false
			f.health[i].lastErr = err
		}

		f.mu.Unlock()
	}

	// Update healthy endpoints gauge.
	f.mu.Lock()
	count := int64(0)
	for i := range f.health {
		if f.health[i].healthy {
			count++
		}
	}
	f.mu.Unlock()

	failoverHealthyEndpoints.Update(count)
}

// maybePromote checks if a higher-priority endpoint (index < active) is healthy
// and has passed cooldown. If yes, promotes to the highest-priority qualified endpoint.
func (f *MultiHeimdallClient) maybePromote() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.active == 0 {
		return
	}

	for i := 0; i < f.active; i++ {
		if f.health[i].healthy && time.Since(f.health[i].healthySince) >= f.promotionCooldown {
			f.active = i
			failoverActiveGauge.Update(int64(i))
			failoverProactiveSwitches.Inc(1)

			log.Info("Heimdall health registry: promoted to higher-priority client",
				"index", i, "previous", f.active)

			return
		}
	}
}

// maybeProactiveSwitch detects if the active endpoint is unhealthy and switches
// to the highest-priority healthy endpoint.
func (f *MultiHeimdallClient) maybeProactiveSwitch() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.health[f.active].healthy {
		return
	}

	// Active is unhealthy. Find the best alternative.
	// Pass 1: healthy + cooled.
	for i := 0; i < len(f.clients); i++ {
		if i == f.active {
			continue
		}

		if f.health[i].healthy && time.Since(f.health[i].healthySince) >= f.promotionCooldown {
			prev := f.active
			f.active = i

			failoverActiveGauge.Update(int64(i))
			failoverProactiveSwitches.Inc(1)

			log.Warn("Heimdall health registry: proactive switch (active unhealthy, cooled target)",
				"from", prev, "to", i)

			return
		}
	}

	// Pass 2: healthy but NOT cooled (emergency).
	for i := 0; i < len(f.clients); i++ {
		if i == f.active {
			continue
		}

		if f.health[i].healthy {
			prev := f.active
			f.active = i

			failoverActiveGauge.Update(int64(i))
			failoverProactiveSwitches.Inc(1)

			log.Warn("Heimdall health registry: proactive switch (active unhealthy, uncooled target)",
				"from", prev, "to", i)

			return
		}
	}
}

// callWithFailover executes fn against the active client. If the active client
// fails with a failover-eligible error, it marks it unhealthy and cascades
// through remaining clients using health registry information.
func callWithFailover[T any](f *MultiHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error)) (T, error) {
	f.ensureHealthRegistry()

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

	// Mark the active endpoint unhealthy in the registry.
	f.mu.Lock()
	f.health[active].consecutiveSuccess = 0
	f.health[active].healthy = false
	f.health[active].lastErr = err
	f.mu.Unlock()

	if active == 0 {
		log.Warn("Heimdall failover: primary failed, cascading", "err", err)
	}

	return cascadeClients(f, ctx, fn, active, err)
}

// cascadeClients tries all endpoints in priority order using health registry
// information. It uses a three-pass approach:
//  1. Healthy + cooled endpoints in priority order (skipping failed active)
//  2. Healthy but NOT cooled endpoints in priority order
//  3. Unhealthy endpoints in priority order (last resort)
func cascadeClients[T any](f *MultiHeimdallClient, ctx context.Context, fn func(context.Context, Endpoint) (T, error), failed int, lastErr error) (T, error) {
	n := len(f.clients)

	// Build candidate lists based on health state.
	f.mu.Lock()

	var cooled, uncooled, unhealthy []int

	for i := 0; i < n; i++ {
		if i == failed {
			continue
		}

		if f.health[i].healthy {
			if time.Since(f.health[i].healthySince) >= f.promotionCooldown {
				cooled = append(cooled, i)
			} else {
				uncooled = append(uncooled, i)
			}
		} else {
			unhealthy = append(unhealthy, i)
		}
	}

	f.mu.Unlock()

	// Try each pass in order.
	passes := [][]int{cooled, uncooled, unhealthy}

	for _, candidates := range passes {
		for _, i := range candidates {
			subCtx, cancel := context.WithTimeout(ctx, f.attemptTimeout)
			result, err := fn(subCtx, f.clients[i])
			cancel()

			if err == nil {
				f.mu.Lock()
				f.active = i
				f.health[i].consecutiveSuccess++
				if !f.health[i].healthy && f.health[i].consecutiveSuccess >= f.consecutiveThreshold {
					f.health[i].healthy = true
					f.health[i].healthySince = time.Now()
				}
				f.mu.Unlock()

				failoverSwitchCounter.Inc(1)
				failoverActiveGauge.Update(int64(i))

				log.Warn("Heimdall failover: switched to client", "index", i)

				return result, nil
			}

			lastErr = err

			if !isFailoverError(err, ctx) {
				var zero T
				return zero, err
			}

			// Mark this endpoint unhealthy too.
			f.mu.Lock()
			f.health[i].consecutiveSuccess = 0
			f.health[i].healthy = false
			f.health[i].lastErr = err
			f.mu.Unlock()
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
