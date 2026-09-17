package eth

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethereum/go-ethereum/common"
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
	if p.Trusted() || p.Static() {
		return nil
	}
	switch code {
	case GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg:
		if !p.limits.requests.AllowN(now, 1) {
			return fmt.Errorf("%w: data requests", ErrPeerRateLimit)
		}
	case NewBlockHashesMsg:
		maxSize := rlp.ListSize(peerHashBurst * rlp.ListSize(common.HashLength+1+uint64(rlp.IntSize(math.MaxUint64))))
		if uint64(size) > maxSize {
			return fmt.Errorf("%w: block announcements", ErrPeerRateLimit)
		}
		fallthrough
	case NewBlockMsg:
		if !p.limits.gossip.AllowN(now, 1) || !p.limits.gossipBytes.AllowN(now, int(size)) {
			return fmt.Errorf("%w: block gossip", ErrPeerRateLimit)
		}
	}
	return nil
}

func (p *Peer) checkAnnouncementRate(count int, now time.Time) error {
	if !p.Trusted() && !p.Static() && !p.limits.hashes.AllowN(now, count) {
		return fmt.Errorf("%w: block announcements", ErrPeerRateLimit)
	}
	return nil
}

type peerReplies struct {
	mu      sync.Mutex
	queue   chan p2p.Msg
	bytes   int
	limiter *rate.Limiter
}

func newPeerReplies() *peerReplies {
	return &peerReplies{queue: make(chan p2p.Msg, peerRequestBurst), limiter: rate.NewLimiter(peerByteRate, peerByteBurst)}
}

var errReplyQueueFull = errors.New("peer response queue full")

func (p *Peer) queueReply(code uint64, packet interface{}) error {
	if p.Trusted() || p.Static() {
		return p2p.Send(p.rw, code, packet)
	}
	data, err := rlp.EncodeToBytes(packet)
	if err != nil {
		return err
	}
	if len(data) > maxMessageSize {
		return errMsgTooLarge
	}
	replies := p.blockReplies
	if code == PooledTransactionsMsg {
		replies = p.txReplies
	}
	replies.mu.Lock()
	defer replies.mu.Unlock()
	select {
	case <-p.term:
		return ErrDisconnected
	default:
	}
	if replies.queue == nil {
		return ErrDisconnected
	}
	if replies.bytes+len(data) > peerByteBurst/2 {
		return errReplyQueueFull
	}
	select {
	case replies.queue <- p2p.Msg{Code: code, Size: uint32(len(data)), Payload: bytes.NewReader(data)}:
		replies.bytes += len(data)
		return nil
	default:
		return errReplyQueueFull
	}
}

// Close cancels allowance waits; transport shutdown interrupts an active write.
func (p *Peer) sendReplies(replies *peerReplies) {
	replies.mu.Lock()
	queue := replies.queue
	replies.mu.Unlock()
	defer func() {
		replies.mu.Lock()
		defer replies.mu.Unlock()
		replies.queue = nil
		replies.bytes = 0
	}()
	for {
		select {
		case msg := <-queue:
			if err := p.waitReplyAllowance(replies.limiter, int(msg.Size)); err != nil {
				return
			}
			if err := p.rw.WriteMsg(msg); err != nil {
				p.Log().Debug("Failed to send response", "err", err)
				p.Disconnect(p2p.DiscNetworkError)
				return
			}
			replies.mu.Lock()
			replies.bytes -= int(msg.Size)
			replies.mu.Unlock()
		case <-p.term:
			return
		}
	}
}

func (p *Peer) waitReplyAllowance(limiter *rate.Limiter, size int) error {
	reservation := limiter.ReserveN(time.Now(), size)
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
