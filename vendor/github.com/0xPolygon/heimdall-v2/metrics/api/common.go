package api

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/0xPolygon/heimdall-v2/metrics"
)

const (
	// QueryType is the type of API call.
	QueryType = "query"
	// TxType is the type of API call.
	TxType = "tx"
	// SideType is the type of side handler call.
	SideType = "side"
	// PostType is the type of post handler call.
	PostType = "post"

	// Module subsystems.
	BorSubsystem        = "bor"
	CheckpointSubsystem = "checkpoint"
	ClerkSubsystem      = "clerk"
	MilestoneSubsystem  = "milestone"
	StakeSubsystem      = "stake"
	TopupSubsystem      = "topup"
)

// ModuleMetrics holds the API metrics for a specific module.
type ModuleMetrics struct {
	// Total API calls counter.
	TotalCalls *prometheus.CounterVec
	// Successful API calls counter.
	SuccessCalls *prometheus.CounterVec
	// API response time summary.
	ResponseTime *prometheus.SummaryVec
}

// Global registry for module metrics.
var (
	moduleMetrics = make(map[string]*ModuleMetrics)
	metricsMutex  sync.RWMutex
)

// GetModuleMetrics returns or creates metrics for a given module.
func GetModuleMetrics(subsystem string) *ModuleMetrics {
	metricsMutex.RLock()
	if existing, ok := moduleMetrics[subsystem]; ok {
		metricsMutex.RUnlock()
		return existing
	}
	metricsMutex.RUnlock()

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// Create new metrics for this module.
	moduleMetrics[subsystem] = &ModuleMetrics{
		TotalCalls: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metrics.Namespace,
				Subsystem: subsystem,
				Name:      "api_calls_total",
				Help:      "Total number of API calls to " + subsystem + " module",
			},
			[]string{"method", "type"},
		),
		SuccessCalls: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metrics.Namespace,
				Subsystem: subsystem,
				Name:      "api_calls_success_total",
				Help:      "Total number of successful API calls to " + subsystem + " module",
			},
			[]string{"method", "type"},
		),
		ResponseTime: promauto.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace: metrics.Namespace,
				Subsystem: subsystem,
				Name:      "api_response_time_seconds",
				Help:      "Response time of API calls to " + subsystem + " module in seconds",
				Objectives: map[float64]float64{
					0.50: 0.05,  // 50th percentile +/-5% error
					0.90: 0.01,  // 90th percentile +/-1% error
					0.99: 0.001, // 99th percentile +/-0.1% error
				},
				MaxAge:     24 * time.Hour,
				AgeBuckets: 6,
			},
			[]string{"method", "type"},
		),
	}

	return moduleMetrics[subsystem]
}

// RecordAPICall is the main generic function to record API metrics for any module.
func RecordAPICall(subsystem, method, apiType string, success bool, duration time.Duration) {
	metrics := GetModuleMetrics(subsystem)
	durationSeconds := duration.Seconds()

	// Record total calls.
	metrics.TotalCalls.WithLabelValues(method, apiType).Inc()

	// Record success calls only if successful.
	if success {
		metrics.SuccessCalls.WithLabelValues(method, apiType).Inc()
	}

	// Record response time.
	metrics.ResponseTime.WithLabelValues(method, apiType).Observe(durationSeconds)
}

// RecordAPICallWithStart is a convenience function that calculates duration from start time.
func RecordAPICallWithStart(subsystem, method, apiType string, success bool, start time.Time) {
	duration := time.Since(start)
	RecordAPICall(subsystem, method, apiType, success, duration)
}
