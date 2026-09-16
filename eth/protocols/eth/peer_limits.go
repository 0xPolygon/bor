package eth

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
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

var errReplyQueueFull = errors.New("peer response queue full")

func (p *Peer) queueReply(code uint64, packet interface{}) error {
	if p.Trusted() || p.StaticDialed() {
		return p2p.Send(p.rw, code, packet)
	}
	data, err := rlp.EncodeToBytes(packet)
	if err != nil {
		return err
	}
	if len(data) > maxMessageSize {
		return errMsgTooLarge
	}
	p.replyLock.Lock()
	defer p.replyLock.Unlock()
	select {
	case <-p.term:
		return ErrDisconnected
	default:
	}
	if p.replyQueue == nil {
		return ErrDisconnected
	}
	if p.replyBytes+len(data) > peerByteBurst {
		return errReplyQueueFull
	}
	select {
	case p.replyQueue <- p2p.Msg{Code: code, Size: uint32(len(data)), Payload: bytes.NewReader(data)}:
		p.replyBytes += len(data)
		return nil
	default:
		return errReplyQueueFull
	}
}

// Close cancels allowance waits; transport shutdown interrupts an active write.
func (p *Peer) sendReplies() {
	defer func() {
		p.replyLock.Lock()
		defer p.replyLock.Unlock()
		p.replyQueue = nil
		p.replyBytes = 0
	}()
	for {
		select {
		case msg := <-p.replyQueue:
			if err := p.waitReplyAllowance(int(msg.Size)); err != nil {
				return
			}
			if err := p.rw.WriteMsg(msg); err != nil {
				p.Log().Debug("Failed to send response", "err", err)
				p.Disconnect(p2p.DiscNetworkError)
				return
			}
			p.replyLock.Lock()
			p.replyBytes -= int(msg.Size)
			p.replyLock.Unlock()
		case <-p.term:
			return
		}
	}
}

func (p *Peer) waitReplyAllowance(size int) error {
	reservation := p.limits.replyBytes.ReserveN(time.Now(), size)
	if !reservation.OK() {
		return errMsgTooLarge
	}
	if delay := reservation.Delay(); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-p.term:
			reservation.Cancel()
			return ErrDisconnected
		}
	}
	select {
	case <-p.term:
		reservation.Cancel()
		return ErrDisconnected
	default:
		return nil
	}
}
