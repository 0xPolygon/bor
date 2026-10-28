package eth

import "github.com/ethereum/go-ethereum/rlp"

// Wait in the peer's message loop, which bounds queued work without dropping
// sync requests. Release the lookup slot before writing a potentially slow reply.
func serveHeavyResponseQuery(peer *Peer, query func() []rlp.RawValue) ([]rlp.RawValue, bool) {
	var disconnected <-chan struct{}
	if source, ok := peer.rw.(interface{ Done() <-chan struct{} }); ok {
		disconnected = source.Done()
	}
	select {
	case heavyResponseServeSlots <- struct{}{}:
		defer func() { <-heavyResponseServeSlots }()
	case <-disconnected:
		return nil, false
	case <-peer.term:
		return nil, false
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
