package eth

import "github.com/ethereum/go-ethereum/rlp"

// HeavyResponseLimiter shares lookup capacity among one node's peers.
type HeavyResponseLimiter struct {
	slots chan struct{}
}

func NewHeavyResponseLimiter() *HeavyResponseLimiter {
	return &HeavyResponseLimiter{slots: make(chan struct{}, heavyResponseWorkerLimit())}
}

// Wait in the peer's message loop, which bounds queued work without dropping
// sync requests. Release the lookup slot before writing a potentially slow reply.
// The slot limiter only applies to peers on sidecar-enabled nodes; others bypass
// it so serving is unaffected by another node in the same process.
func serveHeavyResponseQuery(peer *Peer, query func() []rlp.RawValue) ([]rlp.RawValue, bool) {
	var disconnected <-chan struct{}
	if source, ok := peer.rw.(interface{ Done() <-chan struct{} }); ok {
		disconnected = source.Done()
	}
	if limiter := peer.heavyResponseLimiter; limiter != nil {
		select {
		case limiter.slots <- struct{}{}:
			defer func() { <-limiter.slots }()
		case <-disconnected:
			return nil, false
		case <-peer.term:
			return nil, false
		}
	}
	select {
	case <-disconnected:
		return nil, false
	case <-peer.term:
		return nil, false
	default:
		return query(), true
	}
}
