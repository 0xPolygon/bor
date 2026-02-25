package heimdall

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// EndpointHealth tracks the health state of a single endpoint.
type EndpointHealth struct {
	Healthy            bool
	ConsecutiveSuccess int
	HealthySince       time.Time // when consecutive threshold was reached
	LastErr            error
}

// RegistryMetrics holds the metrics counters/gauges that a HealthRegistry reports to.
// Nil pointers are safe — the registry checks before calling.
type RegistryMetrics struct {
	ProbeAttempts     *metrics.Counter
	ProbeSuccesses    *metrics.Counter
	ProactiveSwitches *metrics.Counter
	ActiveGauge       *metrics.Gauge
	HealthyEndpoints  *metrics.Gauge
}

// HealthRegistry is a shared health state machine for N endpoints.
// It runs a background goroutine that probes all endpoints, promotes
// higher-priority endpoints when healthy+cooled, and proactively switches
// away from unhealthy active endpoints.
type HealthRegistry struct {
	mu     sync.Mutex
	health []EndpointHealth
	active int
	n      int

	// Exported config fields — set after construction, before Start().
	HealthCheckInterval  time.Duration
	ConsecutiveThreshold int
	PromotionCooldown    time.Duration

	probeFunc func(i int) error
	onSwitch  func(from, to int) // called outside mu to avoid lock-ordering issues

	metrics RegistryMetrics

	quit      chan struct{}
	done      chan struct{} // closed when run() exits
	closeOnce sync.Once
	startOnce sync.Once
}

// NewHealthRegistry creates a registry for n endpoints.
// probeFunc is called for each endpoint index to test reachability.
// onSwitch (optional) is called outside the registry lock when the active
// endpoint changes due to promotion, proactive switch, or SetActive.
func NewHealthRegistry(n int, probeFunc func(int) error, onSwitch func(from, to int), m RegistryMetrics) *HealthRegistry {
	health := make([]EndpointHealth, n)
	// Primary starts as healthy; others start unhealthy.
	health[0] = EndpointHealth{Healthy: true}

	return &HealthRegistry{
		health:               health,
		n:                    n,
		HealthCheckInterval:  defaultHealthCheckInterval,
		ConsecutiveThreshold: defaultConsecutiveThreshold,
		PromotionCooldown:    defaultPromotionCooldown,
		probeFunc:            probeFunc,
		onSwitch:             onSwitch,
		metrics:              m,
		quit:                 make(chan struct{}),
		done:                 make(chan struct{}),
	}
}

// Active returns the index of the currently active endpoint.
func (r *HealthRegistry) Active() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.active
}

// SetActive sets the active endpoint index, updates the gauge, and calls onSwitch
// if the active endpoint changed. The caller must NOT hold r.mu.
func (r *HealthRegistry) SetActive(i int) {
	r.mu.Lock()
	prev := r.active
	r.active = i

	if r.metrics.ActiveGauge != nil {
		r.metrics.ActiveGauge.Update(int64(i))
	}
	r.mu.Unlock()

	// Call onSwitch outside r.mu to avoid lock-ordering deadlock.
	// The WS client's onWSSwitch callback acquires c.mu, so calling it
	// under r.mu would create a registry.mu → c.mu path that conflicts
	// with the c.mu → registry.mu path in tryUntilSubscribeMilestoneEvents.
	if prev != i && r.onSwitch != nil {
		r.onSwitch(prev, i)
	}
}

// MarkUnhealthy resets the health state of endpoint i to unhealthy.
func (r *HealthRegistry) MarkUnhealthy(i int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.health[i].ConsecutiveSuccess = 0
	r.health[i].Healthy = false
	r.health[i].LastErr = err
}

// MarkSuccess increments the consecutive success count for endpoint i and
// transitions it to healthy if the threshold is met.
func (r *HealthRegistry) MarkSuccess(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.health[i].ConsecutiveSuccess++
	r.health[i].LastErr = nil

	if r.health[i].ConsecutiveSuccess >= r.ConsecutiveThreshold && !r.health[i].Healthy {
		r.health[i].Healthy = true
		r.health[i].HealthySince = time.Now()
	}
}

// HealthSnapshot returns a copy of all endpoint health states.
func (r *HealthRegistry) HealthSnapshot() []EndpointHealth {
	r.mu.Lock()
	defer r.mu.Unlock()

	snap := make([]EndpointHealth, r.n)
	copy(snap, r.health)

	return snap
}

// SetHealth directly overrides the health state of endpoint i.
// Intended for tests that need to manipulate state.
func (r *HealthRegistry) SetHealth(i int, h EndpointHealth) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.health[i] = h
}

// Start lazily starts the background health-check goroutine via startOnce.
func (r *HealthRegistry) Start() {
	r.startOnce.Do(func() {
		go r.run()
	})
}

// Stop closes the quit channel and waits for the background goroutine to exit.
func (r *HealthRegistry) Stop() {
	// If Start() was never called, close done so the wait below doesn't block.
	r.startOnce.Do(func() {
		close(r.done)
	})

	r.closeOnce.Do(func() {
		close(r.quit)
	})

	<-r.done
}

// run is the background goroutine: probe → promote → proactive switch.
func (r *HealthRegistry) run() {
	defer close(r.done)

	// Run an immediate probe cycle so a down primary is detected within
	// seconds of boot rather than waiting for the first ticker fire.
	r.probeAll()
	r.maybePromote()
	r.maybeProactiveSwitch()

	ticker := time.NewTicker(r.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.quit:
			return
		case <-ticker.C:
		}

		r.probeAll()
		r.maybePromote()
		r.maybeProactiveSwitch()
	}
}

// probeAll probes every endpoint concurrently and updates health state.
// Each goroutine applies its own result immediately so that a request
// arriving mid-cycle (via callWithFailover → HealthSnapshot) sees fresh
// data for already-completed probes rather than stale data for all of them.
func (r *HealthRegistry) probeAll() {
	// Check for shutdown before launching probes.
	select {
	case <-r.quit:
		return
	default:
	}

	var wg sync.WaitGroup
	wg.Add(r.n)

	for i := 0; i < r.n; i++ {
		if r.metrics.ProbeAttempts != nil {
			r.metrics.ProbeAttempts.Inc(1)
		}

		go func(idx int) {
			defer wg.Done()

			err := r.probeFunc(idx)

			// Apply this probe's result immediately.
			r.mu.Lock()
			if err == nil {
				r.health[idx].ConsecutiveSuccess++
				r.health[idx].LastErr = nil

				if r.health[idx].ConsecutiveSuccess >= r.ConsecutiveThreshold && !r.health[idx].Healthy {
					r.health[idx].Healthy = true
					r.health[idx].HealthySince = time.Now()
				}

				if r.metrics.ProbeSuccesses != nil {
					r.metrics.ProbeSuccesses.Inc(1)
				}
			} else {
				r.health[idx].ConsecutiveSuccess = 0
				r.health[idx].Healthy = false
				r.health[idx].LastErr = err
			}
			r.mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Update gauge after all probes complete — needs to scan all results.
	select {
	case <-r.quit:
		return
	default:
	}

	if r.metrics.HealthyEndpoints != nil {
		r.mu.Lock()
		healthyCount := int64(0)
		for i := 0; i < r.n; i++ {
			if r.health[i].Healthy {
				healthyCount++
			}
		}
		r.mu.Unlock()

		r.metrics.HealthyEndpoints.Update(healthyCount)
	}
}

// maybePromote checks if a higher-priority endpoint (index < active) is healthy
// and has passed cooldown. If yes, promotes to the highest-priority qualified endpoint.
func (r *HealthRegistry) maybePromote() {
	var prev, next int
	doSwitch := false

	r.mu.Lock()

	if r.active != 0 {
		for i := 0; i < r.active; i++ {
			if r.health[i].Healthy && time.Since(r.health[i].HealthySince) >= r.PromotionCooldown {
				prev = r.active
				next = i
				r.active = i
				doSwitch = true

				if r.metrics.ActiveGauge != nil {
					r.metrics.ActiveGauge.Update(int64(i))
				}

				if r.metrics.ProactiveSwitches != nil {
					r.metrics.ProactiveSwitches.Inc(1)
				}

				break
			}
		}
	}

	r.mu.Unlock()

	if doSwitch {
		log.Info("Health registry: promoted to higher-priority endpoint",
			"index", next, "previous", prev)

		if r.onSwitch != nil {
			r.onSwitch(prev, next)
		}
	}
}

// maybeProactiveSwitch detects if the active endpoint is unhealthy and switches
// to the highest-priority healthy endpoint.
func (r *HealthRegistry) maybeProactiveSwitch() {
	var prev, next int
	doSwitch := false
	var logMsg string

	r.mu.Lock()

	if r.health[r.active].Healthy {
		r.mu.Unlock()
		return
	}

	// Active is unhealthy. Find the best alternative.
	// Pass 1: healthy + cooled.
	for i := 0; i < r.n; i++ {
		if i == r.active {
			continue
		}

		if r.health[i].Healthy && time.Since(r.health[i].HealthySince) >= r.PromotionCooldown {
			prev = r.active
			next = i
			r.active = i
			doSwitch = true
			logMsg = "Health registry: proactive switch (active unhealthy, cooled target)"

			if r.metrics.ActiveGauge != nil {
				r.metrics.ActiveGauge.Update(int64(i))
			}

			if r.metrics.ProactiveSwitches != nil {
				r.metrics.ProactiveSwitches.Inc(1)
			}

			break
		}
	}

	// Pass 2: healthy but NOT cooled (emergency).
	if !doSwitch {
		for i := 0; i < r.n; i++ {
			if i == r.active {
				continue
			}

			if r.health[i].Healthy {
				prev = r.active
				next = i
				r.active = i
				doSwitch = true
				logMsg = "Health registry: proactive switch (active unhealthy, uncooled target)"

				if r.metrics.ActiveGauge != nil {
					r.metrics.ActiveGauge.Update(int64(i))
				}

				if r.metrics.ProactiveSwitches != nil {
					r.metrics.ProactiveSwitches.Inc(1)
				}

				break
			}
		}
	}

	r.mu.Unlock()

	if doSwitch {
		log.Warn(logMsg, "from", prev, "to", next)

		if r.onSwitch != nil {
			r.onSwitch(prev, next)
		}
	}
}
