package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// heimdallV2InfoGauge stores the HeimdallV2 version and commit details.
var heimdallV2InfoGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: Namespace,
	Name:      "info",
	Help:      "HeimdallV2 version and commit details",
}, []string{"version", "commit"})

// UpdateHeimdallV2Info updates the heimdallv2_info metric with the current version and commit details.
func UpdateHeimdallV2Info(version, commit string) {
	heimdallV2InfoGauge.WithLabelValues(
		version,
		commit,
	).Set(1)
}
