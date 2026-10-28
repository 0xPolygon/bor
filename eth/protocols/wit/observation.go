package wit

import "github.com/ethereum/go-ethereum/p2p/peerpolicy"

// SetObserver enables reputation only after the ETH session is admitted.
// WIT messages may already be running concurrently.
func (p *Peer) SetObserver(observer func(peerpolicy.Reason)) {
	p.observer.Store(&observer)
}

// Observer snapshots admission for deferred validation.
func (p *Peer) Observer() func(peerpolicy.Reason) {
	if observer := p.observer.Load(); observer != nil {
		return *observer
	}
	return nil
}

func (p *Peer) Observe(reason peerpolicy.Reason) {
	if observer := p.Observer(); observer != nil {
		observer(reason)
	}
}
