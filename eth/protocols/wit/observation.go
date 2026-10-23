package wit

import "github.com/ethereum/go-ethereum/p2p/peerpolicy"

// SetObserver enables reputation only after the ETH session is admitted.
// WIT messages may already be running concurrently.
func (p *Peer) SetObserver(observer func(peerpolicy.Reason)) {
	p.observer.Store(&observer)
}

func (p *Peer) Observe(reason peerpolicy.Reason) {
	if observer := p.observer.Load(); observer != nil && *observer != nil {
		(*observer)(reason)
	}
}
