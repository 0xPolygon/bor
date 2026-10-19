package peerpolicy

import "sync"

const maxPeerMetricIdentities = 1024

// Keep admissions for the process lifetime: expiry, eviction and collector
// re-registration must not allow remote identity churn to mint new series.
var processPeerMetricIdentities peerMetricIdentities

type peerMetricIdentities struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (p *peerMetricIdentities) filter(values map[string]int64) (omitted, admitted int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ids == nil {
		p.ids = make(map[string]struct{})
	}
	for id := range values {
		if _, exists := p.ids[id]; exists {
			continue
		}
		if len(p.ids) >= maxPeerMetricIdentities {
			delete(values, id)
			omitted++
			continue
		}
		p.ids[id] = struct{}{}
	}
	return omitted, int64(len(p.ids))
}
