package eth

import "github.com/ethereum/go-ethereum/rlp"

const heavyResponseSmallQueryLimit = 16

// HeavyResponseLimiter shares lookup capacity among one node's peers.
type HeavyResponseLimiter struct {
	slots chan struct{}
}

func NewHeavyResponseLimiter() *HeavyResponseLimiter {
	return &HeavyResponseLimiter{slots: make(chan struct{}, heavyResponseWorkerLimit())}
}

// Wait in the peer's message loop, which bounds queued work without dropping
// sync requests. Release the lookup slot before writing a potentially slow reply.
// Small requests bypass the bulk queue so fresh-block fetches are not delayed by
// historical sync. Sidecar-disabled nodes bypass the limiter entirely.
func serveHeavyResponseQuery(peer *Peer, hashes int, query func() []rlp.RawValue) ([]rlp.RawValue, bool) {
	var disconnected <-chan struct{}
	if source, ok := peer.rw.(interface{ Done() <-chan struct{} }); ok {
		disconnected = source.Done()
	}
	if limiter := peer.heavyResponseLimiter; limiter != nil && hashes > heavyResponseSmallQueryLimit {
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
