package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// PreBlockerDuration tracks the time taken by PreBlocker function.
	PreBlockerDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "pre_blocker_duration_seconds",
			Help:      "Time taken by PreBlocker function in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// BeginBlockerDuration tracks the time taken by BeginBlocker function.
	BeginBlockerDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "begin_blocker_duration_seconds",
			Help:      "Time taken by BeginBlocker function in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// EndBlockerDuration tracks the time taken by EndBlocker function.
	EndBlockerDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "end_blocker_duration_seconds",
			Help:      "Time taken by EndBlocker function in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// PrepareProposalDuration tracks the time taken by PrepareProposal handler.
	PrepareProposalDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "prepare_proposal_duration_seconds",
			Help:      "Time taken by PrepareProposal handler in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// ProcessProposalDuration tracks the time taken by ProcessProposal handler.
	ProcessProposalDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "process_proposal_duration_seconds",
			Help:      "Time taken by ProcessProposal handler in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// ExtendVoteDuration tracks the time taken by ExtendVote handler.
	ExtendVoteDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "extend_vote_duration_seconds",
			Help:      "Time taken by ExtendVote handler in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)

	// VerifyVoteExtensionDuration tracks the time taken by VerifyVoteExtension handler.
	VerifyVoteExtensionDuration = promauto.NewSummary(
		prometheus.SummaryOpts{
			Namespace: Namespace,
			Subsystem: "abci",
			Name:      "verify_vote_extension_duration_seconds",
			Help:      "Time taken by VerifyVoteExtension handler in seconds",
			Objectives: map[float64]float64{
				0.50: 0.05,
				0.90: 0.01,
				0.99: 0.001,
			},
		},
	)
)

// RecordABCIHandlerDuration records the time taken for any ABCI handler.
func RecordABCIHandlerDuration(metric prometheus.Summary, start time.Time) {
	duration := time.Since(start)
	metric.Observe(duration.Seconds())
}
