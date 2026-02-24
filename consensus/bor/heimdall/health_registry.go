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
	onSwitch  func(from, to int) // called under mu; may acquire other locks

	metrics RegistryMetrics

	quit      chan struct{}
	closeOnce sync.Once
	startOnce sync.Once
}

// NewHealthRegistry creates a registry for n endpoints.
// probeFunc is called for each endpoint index to test reachability.
// onSwitch (optional) is called under the registry lock when the active
// endpoint changes due to promotion or proactive switch.
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
	defer r.mu.Unlock()

	prev := r.active
	r.active = i

	if r.metrics.ActiveGauge != nil {
		r.metrics.ActiveGauge.Update(int64(i))
	}

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

// Stop closes the quit channel, stopping the background goroutine.
func (r *HealthRegistry) Stop() {
	r.closeOnce.Do(func() {
		close(r.quit)
	})
}

// run is the background goroutine: probe → promote → proactive switch.
func (r *HealthRegistry) run() {
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

// probeAll probes every endpoint and updates health state.
func (r *HealthRegistry) probeAll() {
	for i := 0; i < r.n; i++ {
		// Check for shutdown between individual probes.
		select {
		case <-r.quit:
			return
		default:
		}

		if r.metrics.ProbeAttempts != nil {
			r.metrics.ProbeAttempts.Inc(1)
		}

		err := r.probeFunc(i)

		r.mu.Lock()

		if err == nil {
			r.health[i].ConsecutiveSuccess++
			r.health[i].LastErr = nil

			if r.health[i].ConsecutiveSuccess >= r.ConsecutiveThreshold && !r.health[i].Healthy {
				r.health[i].Healthy = true
				r.health[i].HealthySince = time.Now()
			}

			if r.metrics.ProbeSuccesses != nil {
				r.metrics.ProbeSuccesses.Inc(1)
			}
		} else {
			r.health[i].ConsecutiveSuccess = 0
			r.health[i].Healthy = false
			r.health[i].LastErr = err
		}

		r.mu.Unlock()
	}

	// Update healthy endpoints gauge.
	r.mu.Lock()
	count := int64(0)

	for i := range r.health {
		if r.health[i].Healthy {
			count++
		}
	}

	r.mu.Unlock()

	if r.metrics.HealthyEndpoints != nil {
		r.metrics.HealthyEndpoints.Update(count)
	}
}

// maybePromote checks if a higher-priority endpoint (index < active) is healthy
// and has passed cooldown. If yes, promotes to the highest-priority qualified endpoint.
func (r *HealthRegistry) maybePromote() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active == 0 {
		return
	}

	for i := 0; i < r.active; i++ {
		if r.health[i].Healthy && time.Since(r.health[i].HealthySince) >= r.PromotionCooldown {
			prev := r.active
			r.active = i

			if r.metrics.ActiveGauge != nil {
				r.metrics.ActiveGauge.Update(int64(i))
			}

			if r.metrics.ProactiveSwitches != nil {
				r.metrics.ProactiveSwitches.Inc(1)
			}

			log.Info("Health registry: promoted to higher-priority endpoint",
				"index", i, "previous", prev)

			if r.onSwitch != nil {
				r.onSwitch(prev, i)
			}

			return
		}
	}
}

// maybeProactiveSwitch detects if the active endpoint is unhealthy and switches
// to the highest-priority healthy endpoint.
func (r *HealthRegistry) maybeProactiveSwitch() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.health[r.active].Healthy {
		return
	}

	// Active is unhealthy. Find the best alternative.
	// Pass 1: healthy + cooled.
	for i := 0; i < r.n; i++ {
		if i == r.active {
			continue
		}

		if r.health[i].Healthy && time.Since(r.health[i].HealthySince) >= r.PromotionCooldown {
			prev := r.active
			r.active = i

			if r.metrics.ActiveGauge != nil {
				r.metrics.ActiveGauge.Update(int64(i))
			}

			if r.metrics.ProactiveSwitches != nil {
				r.metrics.ProactiveSwitches.Inc(1)
			}

			log.Warn("Health registry: proactive switch (active unhealthy, cooled target)",
				"from", prev, "to", i)

			if r.onSwitch != nil {
				r.onSwitch(prev, i)
			}

			return
		}
	}

	// Pass 2: healthy but NOT cooled (emergency).
	for i := 0; i < r.n; i++ {
		if i == r.active {
			continue
		}

		if r.health[i].Healthy {
			prev := r.active
			r.active = i

			if r.metrics.ActiveGauge != nil {
				r.metrics.ActiveGauge.Update(int64(i))
			}

			if r.metrics.ProactiveSwitches != nil {
				r.metrics.ProactiveSwitches.Inc(1)
			}

			log.Warn("Health registry: proactive switch (active unhealthy, uncooled target)",
				"from", prev, "to", i)

			if r.onSwitch != nil {
				r.onSwitch(prev, i)
			}

			return
		}
	}
}
