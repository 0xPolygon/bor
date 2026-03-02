package heimdall

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthRegistry_Constructor_PrimaryHealthy(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})

	snap := r.HealthSnapshot()
	assert.Len(t, snap, 3)
	assert.True(t, snap[0].Healthy, "primary should start healthy")
	assert.False(t, snap[1].Healthy, "secondary should start unhealthy")
	assert.False(t, snap[2].Healthy, "tertiary should start unhealthy")
	assert.Equal(t, 0, r.Active())
}

func TestHealthRegistry_MarkUnhealthy(t *testing.T) {
	r := NewHealthRegistry(2, func(i int) error { return nil }, nil, RegistryMetrics{})

	r.MarkUnhealthy(0, errors.New("down"))

	snap := r.HealthSnapshot()
	assert.False(t, snap[0].Healthy)
	assert.Equal(t, 0, snap[0].ConsecutiveSuccess)
	assert.EqualError(t, snap[0].LastErr, "down")
}

func TestHealthRegistry_MarkSuccess_Transitions(t *testing.T) {
	r := NewHealthRegistry(2, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.ConsecutiveThreshold = 3

	// Endpoint 1 starts unhealthy.
	snap := r.HealthSnapshot()
	assert.False(t, snap[1].Healthy)

	// Two successes: still unhealthy.
	r.MarkSuccess(1)
	r.MarkSuccess(1)
	snap = r.HealthSnapshot()
	assert.False(t, snap[1].Healthy)
	assert.Equal(t, 2, snap[1].ConsecutiveSuccess)

	// Third success: transitions to healthy.
	r.MarkSuccess(1)
	snap = r.HealthSnapshot()
	assert.True(t, snap[1].Healthy)
	assert.Equal(t, 3, snap[1].ConsecutiveSuccess)
	assert.False(t, snap[1].HealthySince.IsZero())
}

func TestHealthRegistry_MarkSuccess_ResetByFailure(t *testing.T) {
	r := NewHealthRegistry(2, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.ConsecutiveThreshold = 3

	r.MarkSuccess(1)
	r.MarkSuccess(1)
	r.MarkUnhealthy(1, errors.New("fail"))

	snap := r.HealthSnapshot()
	assert.False(t, snap[1].Healthy)
	assert.Equal(t, 0, snap[1].ConsecutiveSuccess)

	// Need 3 more successes after reset.
	r.MarkSuccess(1)
	snap = r.HealthSnapshot()
	assert.False(t, snap[1].Healthy)
}

func TestHealthRegistry_SetActive_CallsOnSwitch(t *testing.T) {
	var switchFrom, switchTo int
	called := false

	r := NewHealthRegistry(2, func(i int) error { return nil }, func(from, to int) {
		called = true
		switchFrom = from
		switchTo = to
	}, RegistryMetrics{})

	r.SetActive(1)
	assert.True(t, called)
	assert.Equal(t, 0, switchFrom)
	assert.Equal(t, 1, switchTo)
	assert.Equal(t, 1, r.Active())
}

func TestHealthRegistry_SetActive_NoCallOnSameIndex(t *testing.T) {
	called := false
	r := NewHealthRegistry(2, func(i int) error { return nil }, func(from, to int) {
		called = true
	}, RegistryMetrics{})

	r.SetActive(0) // same as current
	assert.False(t, called, "onSwitch should not be called when active doesn't change")
}

func TestHealthRegistry_SetHealth(t *testing.T) {
	r := NewHealthRegistry(2, func(i int) error { return nil }, nil, RegistryMetrics{})

	h := EndpointHealth{
		Healthy:            true,
		ConsecutiveSuccess: 5,
		HealthySince:       time.Now().Add(-1 * time.Hour),
	}
	r.SetHealth(1, h)

	snap := r.HealthSnapshot()
	assert.True(t, snap[1].Healthy)
	assert.Equal(t, 5, snap[1].ConsecutiveSuccess)
}

func TestHealthRegistry_ProbeAll(t *testing.T) {
	probeResults := []error{nil, errors.New("fail"), nil}
	probeCount := atomic.Int32{}

	r := NewHealthRegistry(3, func(i int) error {
		probeCount.Add(1)
		return probeResults[i]
	}, nil, RegistryMetrics{})
	r.ConsecutiveThreshold = 1

	r.probeAll()

	assert.Equal(t, int32(3), probeCount.Load())

	snap := r.HealthSnapshot()
	// Index 0 was already healthy, stays healthy.
	assert.True(t, snap[0].Healthy)
	// Index 1 failed: unhealthy.
	assert.False(t, snap[1].Healthy)
	assert.EqualError(t, snap[1].LastErr, "fail")
	// Index 2 succeeded once with threshold=1: becomes healthy.
	assert.True(t, snap[2].Healthy)
}

func TestHealthRegistry_MaybePromote(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.PromotionCooldown = 0
	r.ConsecutiveThreshold = 1

	// Set active to 2, mark index 0 as unhealthy, make index 1 healthy+cooled.
	r.SetActive(2)
	r.SetHealth(0, EndpointHealth{Healthy: false})
	r.SetHealth(1, EndpointHealth{
		Healthy:      true,
		HealthySince: time.Now().Add(-1 * time.Hour),
	})

	r.maybePromote()

	assert.Equal(t, 1, r.Active(), "should promote to index 1")
}

func TestHealthRegistry_MaybePromote_RespectsOrder(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.PromotionCooldown = 0

	// Active at 2, both 0 and 1 healthy — should promote to 0 (highest priority).
	r.SetActive(2)
	r.SetHealth(0, EndpointHealth{Healthy: true, HealthySince: time.Now().Add(-1 * time.Hour)})
	r.SetHealth(1, EndpointHealth{Healthy: true, HealthySince: time.Now().Add(-1 * time.Hour)})

	r.maybePromote()

	assert.Equal(t, 0, r.Active(), "should promote to index 0 (highest priority)")
}

func TestHealthRegistry_MaybePromote_RespectsCooldown(t *testing.T) {
	r := NewHealthRegistry(2, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.PromotionCooldown = 1 * time.Hour

	// Active at 1, index 0 healthy but recently (not cooled).
	r.SetActive(1)
	r.SetHealth(0, EndpointHealth{Healthy: true, HealthySince: time.Now()})

	r.maybePromote()

	assert.Equal(t, 1, r.Active(), "should not promote — cooldown not met")
}

func TestHealthRegistry_MaybeProactiveSwitch_CooledFirst(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.PromotionCooldown = 0

	// Active at 0, mark it unhealthy. Index 2 is healthy+cooled.
	r.SetHealth(0, EndpointHealth{Healthy: false})
	r.SetHealth(2, EndpointHealth{Healthy: true, HealthySince: time.Now().Add(-1 * time.Hour)})

	r.maybeProactiveSwitch()

	assert.Equal(t, 2, r.Active(), "should switch to cooled healthy endpoint")
}

func TestHealthRegistry_MaybeProactiveSwitch_UncooledFallback(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})
	r.PromotionCooldown = 1 * time.Hour

	// Active at 0, mark it unhealthy. Index 1 is healthy but NOT cooled.
	r.SetHealth(0, EndpointHealth{Healthy: false})
	r.SetHealth(1, EndpointHealth{Healthy: true, HealthySince: time.Now()}) // not cooled

	r.maybeProactiveSwitch()

	assert.Equal(t, 1, r.Active(), "should fall back to uncooled healthy endpoint")
}

func TestHealthRegistry_MaybeProactiveSwitch_NoHealthy(t *testing.T) {
	r := NewHealthRegistry(3, func(i int) error { return nil }, nil, RegistryMetrics{})

	// All unhealthy.
	r.SetHealth(0, EndpointHealth{Healthy: false})
	r.SetHealth(1, EndpointHealth{Healthy: false})
	r.SetHealth(2, EndpointHealth{Healthy: false})

	r.maybeProactiveSwitch()

	assert.Equal(t, 0, r.Active(), "should stay on 0 when no alternatives are healthy")
}

func TestHealthRegistry_ImmediateProbeOnStart(t *testing.T) {
	probeCount := atomic.Int32{}

	r := NewHealthRegistry(2, func(i int) error {
		probeCount.Add(1)
		return nil
	}, nil, RegistryMetrics{})
	r.HealthCheckInterval = 10 * time.Second // long interval — should NOT gate first probe

	r.Start()
	defer r.Stop()

	// The first probe cycle should fire immediately, not after HealthCheckInterval.
	require.Eventually(t, func() bool {
		return probeCount.Load() >= 2 // 2 endpoints probed
	}, 2*time.Second, 10*time.Millisecond, "first probe cycle should run immediately on Start")
}

func TestHealthRegistry_ProbeAll_IncrementalUpdate(t *testing.T) {
	// Verify that a fast probe's result is visible before a slow probe completes.
	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{})

	r := NewHealthRegistry(2, func(i int) error {
		if i == 0 {
			// Fast probe: returns immediately.
			return nil
		}
		// Slow probe: blocks until released.
		close(slowStarted)
		<-slowRelease
		return nil
	}, nil, RegistryMetrics{})
	r.ConsecutiveThreshold = 1

	// Run probeAll in a goroutine since the slow probe blocks.
	done := make(chan struct{})
	go func() {
		r.probeAll()
		close(done)
	}()

	// Wait for the slow probe to start (meaning the fast probe has already completed).
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slow probe to start")
	}

	// The fast probe (index 0) should already be applied even though the slow
	// probe (index 1) is still in flight.
	snap := r.HealthSnapshot()
	assert.True(t, snap[0].Healthy, "fast probe result should be visible before slow probe completes")

	// Release the slow probe and wait for probeAll to finish.
	close(slowRelease)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for probeAll to finish")
	}

	snap = r.HealthSnapshot()
	assert.True(t, snap[1].Healthy, "slow probe result should be applied after release")
}

func TestHealthRegistry_Stop_HaltsGoroutine(t *testing.T) {
	probeCount := atomic.Int32{}

	r := NewHealthRegistry(2, func(i int) error {
		probeCount.Add(1)
		return nil
	}, nil, RegistryMetrics{})
	r.HealthCheckInterval = 50 * time.Millisecond

	r.Start()
	time.Sleep(150 * time.Millisecond)
	r.Stop()

	countAfterStop := probeCount.Load()
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, countAfterStop, probeCount.Load(), "no probes should run after Stop")
}

func TestHealthRegistry_Run_Integration(t *testing.T) {
	probeResults := []error{errors.New("down"), nil}
	var results atomic.Value
	results.Store(probeResults)

	r := NewHealthRegistry(2, func(i int) error {
		return results.Load().([]error)[i]
	}, nil, RegistryMetrics{})
	r.HealthCheckInterval = 50 * time.Millisecond
	r.ConsecutiveThreshold = 1
	r.PromotionCooldown = 0

	r.Start()
	defer r.Stop()

	// Primary is down, secondary is healthy. Should proactively switch.
	require.Eventually(t, func() bool {
		return r.Active() == 1
	}, 2*time.Second, 20*time.Millisecond, "should switch to healthy secondary")

	// Bring primary back.
	results.Store([]error{nil, nil})

	// Should promote back to primary.
	require.Eventually(t, func() bool {
		return r.Active() == 0
	}, 2*time.Second, 20*time.Millisecond, "should promote back to primary")
}
