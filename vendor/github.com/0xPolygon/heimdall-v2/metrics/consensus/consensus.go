package consensus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/0xPolygon/heimdall-v2/metrics"
)

var (
	// SideTxConsensusApproved tracks when side transactions reach 2/3 YES votes.
	SideTxConsensusApproved = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "sidetx_consensus_approved_total",
			Help:      "Total number of side transactions that reached 2/3 YES consensus",
		},
	)

	// SideTxConsensusRejected tracks when side transactions reach 2/3 NO votes.
	SideTxConsensusRejected = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "sidetx_consensus_rejected_total",
			Help:      "Total number of side transactions that reached 2/3 NO consensus",
		},
	)

	// SideTxConsensusFailures tracks when we cannot reach 2/3 agreement on side transactions.
	SideTxConsensusFailures = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "sidetx_consensus_failures_total",
			Help:      "Total number of side transactions that failed to reach 2/3 consensus",
		},
	)
)

// RecordConsensusFailure records when a side transaction fails to reach 2/3 consensus.
func RecordConsensusFailure() {
	SideTxConsensusFailures.Inc()
}

// RecordConsensusApproved records when a side transaction reaches 2/3 YES consensus.
func RecordConsensusApproved() {
	SideTxConsensusApproved.Inc()
}

// RecordConsensusRejected records when a side transaction reaches 2/3 NO consensus.
func RecordConsensusRejected() {
	SideTxConsensusRejected.Inc()
}
