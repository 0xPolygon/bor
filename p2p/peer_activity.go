// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package p2p

import (
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

const (
	peerIdleTimeout       = 10 * time.Minute
	peerIdleCheckInterval = time.Minute
	peerIdleCooldown      = 5 * time.Minute
	peerIdleMinimumPeers  = 5
	peerActivityClosing   = -1
)

var idlePeerDisconnects = metrics.NewRegisteredCounter("p2p/peers/idle/disconnects", nil)

func (p *Peer) recordProtocolActivity() {
	last := p.lastProtocolActivity.Load()
	if last != peerActivityClosing {
		p.lastProtocolActivity.CompareAndSwap(last, int64(p.clock.Now()))
	}
}

func peerSlotsScarce(count, limit int) bool {
	return limit > 0 && count >= limit-limit/10
}

// Called on the server loop, which also serializes peer and trust changes.
func (srv *Server) expireIdlePeer(peers map[enode.ID]*Peer) {
	if !srv.PeerIdleEviction || srv.peerJail == nil {
		return
	}
	count, inbound := livePeerCounts(peers)
	if count <= peerIdleMinimumPeers {
		return
	}
	now := srv.clock.Now()
	peer, last := srv.idlePeer(peers, count, inbound, now)
	if peer == nil || !peer.lastProtocolActivity.CompareAndSwap(last, peerActivityClosing) {
		return
	}
	select {
	case peer.disc <- DiscJailed:
	default:
		return
	}
	srv.peerJail.JailPeerFor(peer.ID(), peerIdleCooldown)
	peer.Log().Info("Disconnecting inactive peer to free a connection slot",
		"inactive", now.Sub(mclock.AbsTime(last)), "cooldown", peerIdleCooldown)
	idlePeerDisconnects.Inc(1)
}

func (srv *Server) idlePeer(peers map[enode.ID]*Peer, count, inbound int, now mclock.AbsTime) (*Peer, int64) {
	var candidate *Peer
	var oldest int64
	for _, peer := range peers {
		last, idle := peer.idleSince(now)
		if !idle || !srv.needsPeerSlot(peer, count, inbound) {
			continue
		}
		if candidate == nil || last < oldest {
			candidate, oldest = peer, last
		}
	}
	return candidate, oldest
}

func (p *Peer) idleSince(now mclock.AbsTime) (int64, bool) {
	last := p.lastProtocolActivity.Load()
	idle := p.isLive() && now.Sub(mclock.AbsTime(last)) >= peerIdleTimeout
	return last, idle && !p.Trusted() && !p.Static()
}

func (p *Peer) isLive() bool {
	if !p.protocolStarted.Load() || p.lastProtocolActivity.Load() == peerActivityClosing {
		return false
	}
	select {
	case <-p.closed:
		return false
	default:
		return true
	}
}

func livePeerCounts(peers map[enode.ID]*Peer) (count, inbound int) {
	for _, peer := range peers {
		if !peer.isLive() {
			continue
		}
		count++
		if peer.Inbound() {
			inbound++
		}
	}
	return count, inbound
}

func (srv *Server) needsPeerSlot(peer *Peer, count, inbound int) bool {
	if peerSlotsScarce(count, srv.MaxPeers) {
		return true
	}
	if peer.Inbound() {
		return peerSlotsScarce(inbound, srv.MaxInboundConns())
	}
	return peerSlotsScarce(count-inbound, srv.MaxDialedConns())
}
