package metrics

// GaugeVec samples gauges sharing one label at collection time. The source must
// be concurrency-safe and return a fresh, bounded map; callers own the snapshot.
// Collection-time sampling lets values expire without background goroutines.
type GaugeVec struct {
	label  string
	source func() map[string]int64
}

func NewGaugeVec(label string, source func() map[string]int64) *GaugeVec {
	return &GaugeVec{label: label, source: source}
}

func (g *GaugeVec) Label() string { return g.label }

func (g *GaugeVec) Snapshot() map[string]int64 { return g.source() }
