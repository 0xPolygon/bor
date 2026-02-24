package heimdall

import "github.com/ethereum/go-ethereum/metrics"

var (
	// HTTP/gRPC failover metrics (used within this package)
	failoverSwitchCounter     = metrics.NewRegisteredCounter("client/failover/switches", nil)
	failoverActiveGauge       = metrics.NewRegisteredGauge("client/failover/active", nil)
	failoverProbeAttempts     = metrics.NewRegisteredCounter("client/failover/probe/attempts", nil)
	failoverProbeSuccesses    = metrics.NewRegisteredCounter("client/failover/probe/successes", nil)
	failoverHealthyEndpoints  = metrics.NewRegisteredGauge("client/failover/healthy_endpoints", nil)
	failoverProactiveSwitches = metrics.NewRegisteredCounter("client/failover/proactive_switches", nil)

	// WS failover metrics (exported for use by heimdallws package)
	FailoverWSSwitchCounter     = metrics.NewRegisteredCounter("client/failover/ws/switches", nil)
	FailoverWSActiveGauge       = metrics.NewRegisteredGauge("client/failover/ws/active", nil)
	FailoverWSProbeAttempts     = metrics.NewRegisteredCounter("client/failover/ws/probe/attempts", nil)
	FailoverWSProbeSuccesses    = metrics.NewRegisteredCounter("client/failover/ws/probe/successes", nil)
	FailoverWSHealthyEndpoints  = metrics.NewRegisteredGauge("client/failover/ws/healthy_endpoints", nil)
	FailoverWSProactiveSwitches = metrics.NewRegisteredCounter("client/failover/ws/proactive_switches", nil)
)
