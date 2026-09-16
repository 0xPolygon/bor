package eth

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethereum/go-ethereum/rlp"
)

var (
	ErrPeerRateLimit          = errors.New("peer traffic allowance exceeded")
	errPeerResponseScheduling = errors.New("block response cannot be scheduled")
)

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
	replyBytes  *rate.Limiter
}

func newPeerLimits() peerLimits {
	return peerLimits{
		requests:    rate.NewLimiter(peerRequestRate, peerRequestBurst),
		gossip:      rate.NewLimiter(peerGossipRate, peerGossipBurst),
		hashes:      rate.NewLimiter(peerHashRate, peerHashBurst),
		gossipBytes: rate.NewLimiter(peerByteRate, peerByteBurst),
		replyBytes:  rate.NewLimiter(peerByteRate, peerByteBurst),
	}
}

func (p *Peer) checkMessageRate(code uint64, size uint32, now time.Time) error {
	if p.Trusted() {
		return nil
	}
	switch code {
	case GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg:
		if !p.limits.requests.AllowN(now, 1) {
			return fmt.Errorf("%w: block requests", ErrPeerRateLimit)
		}
	case NewBlockHashesMsg, NewBlockMsg:
		if !p.limits.gossip.AllowN(now, 1) || !p.limits.gossipBytes.AllowN(now, int(size)) {
			return fmt.Errorf("%w: block gossip", ErrPeerRateLimit)
		}
	}
	return nil
}

func (p *Peer) checkAnnouncementRate(count int, now time.Time) error {
	if !p.Trusted() && !p.limits.hashes.AllowN(now, count) {
		return fmt.Errorf("%w: block announcements", ErrPeerRateLimit)
	}
	return nil
}

func (p *Peer) checkReplyRate(id uint64, data []rlp.RawValue, now time.Time) error {
	if p.Trusted() {
		return nil
	}
	var size uint64
	for _, item := range data {
		size += uint64(len(item))
	}
	size = rlp.ListSize(uint64(rlp.IntSize(id)) + rlp.ListSize(size))
	reservation := p.limits.replyBytes.ReserveN(now, int(size))
	if !reservation.OK() {
		return fmt.Errorf("%w: %d bytes", errPeerResponseScheduling, size)
	}
	delay := reservation.DelayFrom(now)
	switch delay {
	case 0:
		return nil
	case rate.InfDuration:
		reservation.CancelAt(now)
		return fmt.Errorf("%w: byte allowance unavailable", errPeerResponseScheduling)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-p.term:
		reservation.CancelAt(time.Now())
		return ErrDisconnected
	}
}
