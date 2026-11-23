package metrics

import (
	"maps"
	"sync"
)

// GaugeVec samples gauges sharing one label at collection time. The source must
// be concurrency-safe and return a fresh, bounded map; callers own the snapshot.
// Collection-time sampling lets values expire without background goroutines.
type GaugeVec struct {
	label  string
	source func() map[string]int64
	group  *GaugeVecGroup
	name   string
}

func NewGaugeVec(label string, source func() map[string]int64) *GaugeVec {
	return &GaugeVec{label: label, source: source}
}

func (g *GaugeVec) Label() string { return g.label }

func (g *GaugeVec) Snapshot() map[string]int64 {
	if g.group != nil {
		return maps.Clone(g.group.source()[g.name])
	}
	return g.source()
}

// GaugeVecGroup samples related families once per registry iteration.
type GaugeVecGroup struct {
	source func() map[string]map[string]int64
}

func NewGaugeVecGroup(source func() map[string]map[string]int64) *GaugeVecGroup {
	return &GaugeVecGroup{source: source}
}

func (g *GaugeVecGroup) Gauge(name, label string) *GaugeVec {
	return &GaugeVec{group: g, name: name, label: label}
}

func (g *GaugeVecGroup) snapshot() *GaugeVecGroup {
	var once sync.Once
	var values map[string]map[string]int64
	return NewGaugeVecGroup(func() map[string]map[string]int64 {
		once.Do(func() { values = g.source() })
		return values
	})
}
