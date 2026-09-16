package eth

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"
)

var ErrPeerRateLimit = errors.New("peer traffic allowance exceeded")

const (
	peerRequestRate  = 64
	peerRequestBurst = 128
	peerGossipRate   = 16
	peerGossipBurst  = 64
	peerHashRate     = 64
	peerHashBurst    = 256

	peerByteRate  = 16 * 1024 * 1024
	peerByteBurst = 64 * 1024 * 1024
)

type peerLimits struct {
	requests    *rate.Limiter
	gossip      *rate.Limiter
	hashes      *rate.Limiter
	gossipBytes *rate.Limiter
}

func newPeerLimits() peerLimits {
	return peerLimits{
		requests:    rate.NewLimiter(peerRequestRate, peerRequestBurst),
		gossip:      rate.NewLimiter(peerGossipRate, peerGossipBurst),
		hashes:      rate.NewLimiter(peerHashRate, peerHashBurst),
		gossipBytes: rate.NewLimiter(peerByteRate, peerByteBurst),
	}
}

func (p *Peer) checkMessageRate(code uint64, size uint32, now time.Time) error {
	if p.Trusted() || p.StaticDialed() {
		return nil
	}
	switch code {
	case GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg:
		if !p.limits.requests.AllowN(now, 1) {
			return fmt.Errorf("%w: data requests", ErrPeerRateLimit)
		}
	case NewBlockHashesMsg, NewBlockMsg:
		if !p.limits.gossip.AllowN(now, 1) || !p.limits.gossipBytes.AllowN(now, int(size)) {
			return fmt.Errorf("%w: block gossip", ErrPeerRateLimit)
		}
	}
	return nil
}

func (p *Peer) checkAnnouncementRate(count int, now time.Time) error {
	if !p.Trusted() && !p.StaticDialed() && !p.limits.hashes.AllowN(now, count) {
		return fmt.Errorf("%w: block announcements", ErrPeerRateLimit)
	}
	return nil
}
