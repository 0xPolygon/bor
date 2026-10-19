package peerpolicy

// SetClassResolver installs the authenticated live-connection lookup. The second
// result reports whether a connection exists; otherwise the retained class is used.
// Calls run under the tracker mutex, so the resolver must be bounded, perform no
// I/O, and never call back into the tracker. Any locks it takes must not be held
// by callers entering the tracker.
func (t *Tracker) SetClassResolver(resolve func(string) (exempt, connected bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.classResolver = resolve
}

// RefreshClass retains the latest connection class before disconnect. It neither
// creates a record nor treats the administrative update as peer activity.
func (t *Tracker) RefreshClass(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers.Peek(id); ok {
		p.setExempt(t.connectionClass(id, p.exempt))
	}
}

func (t *Tracker) connectionClass(id string, fallback bool) bool {
	if t.classResolver != nil {
		if exempt, connected := t.classResolver(id); connected {
			return exempt
		}
	}
	return fallback
}
