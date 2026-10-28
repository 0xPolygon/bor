// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package p2p

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func (b *BulkSidecar) adoptConn(peer *Peer, conn *quic.Conn) *bulkSession {
	remote := peer.Node()
	session := b.peerSession(remote, peer)
	if session == nil {
		return nil
	}
	session.lock.Lock()
	defer session.lock.Unlock()
	if session.closed {
		return nil
	}
	if session.liveConnLocked() != nil {
		return nil
	}
	session.conn = conn
	session.connClosed = conn.Context().Done()
	session.channels = make(map[string]MsgReadWriter)
	session.signalConnLocked()
	bulkSidecarSessionMeter.Mark(1)
	b.stats.markSessionEstablished()
	b.log.Debug("Bulk sidecar session established", "peer", remote.ID(), "remote", conn.RemoteAddr())
	return session
}

func (s *bulkSession) runConn(conn *quic.Conn) {
	s.lock.Lock()
	current := !s.closed && s.conn == conn
	s.lock.Unlock()
	if !current {
		_ = conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk session superseded")
		return
	}
	defer s.clearConn(conn)
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, maxBulkChannelsPerSession)
	for {
		select {
		case slots <- struct{}{}:
		case <-conn.Context().Done():
			return
		}
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			s.handleChannel(conn, stream)
		}()
	}
}

func (s *bulkSession) handleChannel(conn *quic.Conn, stream *quic.Stream) {
	if err := s.acceptChannel(conn, stream); err != nil {
		if errors.Is(err, errBulkChannelTimeout) || errors.Is(err, errBulkChannelAbandoned) {
			return
		}
		_ = conn.CloseWithError(bulkSidecarProtocolError, err.Error())
	}
}

func (s *bulkSession) clearConn(conn *quic.Conn) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.conn == conn {
		s.clearConnLocked()
	}
}

// clearConnLocked requires s.lock and retires all state owned by the connection.
func (s *bulkSession) clearConnLocked() {
	s.conn = nil
	s.connClosed = nil
	clear(s.channels)
	for name, waiters := range s.waiters {
		for _, waiter := range waiters {
			waiter <- bulkChannelResult{err: io.EOF}
			close(waiter)
		}
		delete(s.waiters, name)
	}
}

func (s *bulkSession) close() {
	s.lock.Lock()
	s.closed = true
	s.signalConnLocked()
	if s.dialCancel != nil {
		s.dialCancel()
	}
	conn := s.conn
	s.clearConnLocked()
	s.lock.Unlock()
	if conn != nil {
		_ = conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk peer dropped")
	}
}

func (s *bulkSession) ensureConn(ctx context.Context) (*quic.Conn, error) {
	s.lock.Lock()
	if s.closed {
		s.lock.Unlock()
		return nil, io.EOF
	}
	if conn := s.liveConnLocked(); conn != nil {
		s.lock.Unlock()
		return conn, nil
	}
	if bytes.Compare(s.sidecar.localID[:], s.remoteID[:]) > 0 {
		s.lock.Unlock()
		return s.waitIncomingConn(ctx)
	}
	if s.dialing {
		wait := s.dialWait
		s.lock.Unlock()
		return s.waitDial(ctx, wait)
	}
	s.dialing = true
	wait := make(chan struct{})
	s.dialWait = wait
	ctx, cancel := context.WithCancel(ctx)
	s.dialCancel = cancel
	remote := s.remote
	s.lock.Unlock()
	defer cancel()
	return s.dial(ctx, remote, wait)
}

// liveConnLocked requires s.lock. A closed transport must never be reused.
func (s *bulkSession) liveConnLocked() *quic.Conn {
	if s.conn != nil {
		select {
		case <-s.connClosed:
			s.clearConnLocked()
		default:
		}
	}
	return s.conn
}

// waitIncomingConn parks until the lower-ID side dials in. The local node never
// dials in that direction, so there is nothing to poll for — adoptConn and
// close both wake the waiters directly.
func (s *bulkSession) waitIncomingConn(ctx context.Context) (*quic.Conn, error) {
	for {
		s.lock.Lock()
		if s.closed {
			s.lock.Unlock()
			return nil, io.EOF
		}
		if conn := s.liveConnLocked(); conn != nil {
			s.lock.Unlock()
			return conn, nil
		}
		ready := s.connReadyLocked()
		s.lock.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
	}
}

// connReadyLocked requires s.lock. The returned channel is closed by the next
// signalConnLocked, so a waiter that reads it under the lock cannot miss a
// connection installed right after it releases the lock.
func (s *bulkSession) connReadyLocked() <-chan struct{} {
	if s.connReady == nil {
		s.connReady = make(chan struct{})
	}
	return s.connReady
}

// signalConnLocked requires s.lock and wakes everyone in waitIncomingConn.
func (s *bulkSession) signalConnLocked() {
	if s.connReady != nil {
		close(s.connReady)
		s.connReady = nil
	}
}

func (s *bulkSession) waitDial(ctx context.Context, wait <-chan struct{}) (*quic.Conn, error) {
	select {
	case <-wait:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if !s.closed {
		if conn := s.liveConnLocked(); conn != nil {
			return conn, nil
		}
	}
	return nil, errBulkSidecarNoPeer
}

func (s *bulkSession) dial(ctx context.Context, remote *enode.Node, wait chan struct{}) (*quic.Conn, error) {
	// Resolve once per connection attempt, not once per lane. Reconnects must
	// still pick up newer endpoints from discovery even if RLPx stays connected.
	remote = s.refreshRecord(s.sidecar.peerRecord(remote))
	conn, err := s.sidecar.dialConn(ctx, remote)

	s.lock.Lock()
	defer s.lock.Unlock()
	s.dialing = false
	close(wait)
	s.dialWait = nil
	s.dialCancel = nil
	if err != nil {
		return nil, err
	}
	if s.closed || ctx.Err() != nil {
		return nil, errors.Join(io.EOF, conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk peer dropped"))
	}
	if s.liveConnLocked() == nil {
		s.conn = conn
		s.connClosed = conn.Context().Done()
		s.channels = make(map[string]MsgReadWriter)
		s.signalConnLocked()
		bulkSidecarSessionMeter.Mark(1)
		s.sidecar.stats.markSessionEstablished()
		s.sidecar.log.Debug("Bulk sidecar session established", "peer", s.remoteID, "remote", conn.RemoteAddr())
		s.sidecar.startWorker(func() { s.runConn(conn) })
		return conn, nil
	}
	_ = conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk connection superseded")
	return s.conn, nil
}

func (s *bulkSession) refreshRecord(remote *enode.Node) *enode.Node {
	s.lock.Lock()
	defer s.lock.Unlock()
	if remote.Seq() > s.remote.Seq() {
		s.remote = remote
	}
	return s.remote
}

func (s *bulkSession) openChannel(ctx context.Context, channel string) (MsgReadWriter, error) {
	if bulkChannelProtocol(channel) == "" {
		return nil, errors.New("invalid bulk channel")
	}
	gate := s.channelOpenGate(channel)
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.sidecar.closeCh:
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rw, ok := s.getChannel(channel); ok {
		return s.claimChannel(ctx, channel, rw)
	}
	conn, err := s.ensureConn(ctx)
	if err != nil {
		return nil, err
	}
	if bytes.Compare(s.sidecar.localID[:], s.remoteID[:]) < 0 {
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return nil, err
		}
		return s.openChannelStream(ctx, conn, stream, channel)
	}
	return s.waitChannel(ctx, conn, channel)
}

// Protocols may attach their lanes in different orders on the two nodes. Only
// duplicate opens of the same channel should wait behind its acknowledgement.
func (s *bulkSession) channelOpenGate(channel string) chan struct{} {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.openGates == nil {
		s.openGates = make(map[string]chan struct{})
	}
	if s.openGates[channel] == nil {
		s.openGates[channel] = make(chan struct{}, 1)
	}
	return s.openGates[channel]
}

func (s *bulkSession) openChannelStream(ctx context.Context, conn *quic.Conn, stream bulkFrameStream, channel string) (MsgReadWriter, error) {
	rw := &bulkStreamMsgRW{
		stream: stream, channel: channel, stats: &s.sidecar.stats,
		log: log.New("peer", s.remoteID, "channel", channel),
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	// Sending the hello may let the receiver claim ownership. From this point,
	// its bounded ack-or-reset decides the outcome, even if the caller cancels.
	if err := writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: channel}); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	var ack bulkChannelHello
	if err := readBulkControlWithTimeout(stream, bulkChannelControlMaxSize, &ack, bulkChannelAckTimeout); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	if ack.Version != bulkSidecarVersion || ack.Channel != channel {
		return nil, errors.Join(errors.New("invalid bulk channel acknowledgement"), rw.Close())
	}
	if err := s.storeChannel(conn, channel, rw, true); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	return rw, nil
}

func (s *bulkSession) acceptChannel(conn *quic.Conn, stream bulkFrameStream) (err error) {
	rw := &bulkStreamMsgRW{stream: stream, stats: &s.sidecar.stats}
	defer func() {
		if err != nil {
			s.removeChannel(rw.channel, rw)
			err = errors.Join(err, rw.Close())
		}
		if rw.ready != nil {
			rw.ready.err = err
			close(rw.ready.done)
		}
	}()
	var hello bulkChannelHello
	if err := readBulkControl(stream, bulkChannelControlMaxSize, &hello); err != nil {
		return err
	}
	if hello.Version != bulkSidecarVersion {
		return fmt.Errorf("unsupported bulk channel version %d", hello.Version)
	}
	if bulkChannelProtocol(hello.Channel) == "" {
		return errors.New("invalid bulk channel name")
	}
	// Fail closed: an unauthenticated session must never reach the channel
	// table, and only the lower-ID side is allowed to open streams.
	if s.peer == nil {
		return errors.New("bulk channel on unauthenticated session")
	}
	if bytes.Compare(s.sidecar.localID[:], s.remoteID[:]) <= 0 {
		return errors.New("bulk channel opened by non-initiator")
	}
	if err := validateBulkChannel(s.peer, hello.Channel); err != nil {
		return err
	}
	rw.channel = hello.Channel
	rw.log = log.New("peer", s.remoteID, "channel", hello.Channel)
	rw.ready = &bulkChannelReady{claimed: make(chan struct{}), done: make(chan struct{})}
	if err := s.storeChannel(conn, hello.Channel, rw, false); err != nil {
		return err
	}
	return s.acknowledgeChannel(conn, rw, hello)
}

func (s *bulkSession) getChannel(channel string) (MsgReadWriter, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.closed {
		return nil, false
	}
	s.liveConnLocked()
	rw, ok := s.channels[channel]
	return rw, ok
}

func (s *bulkSession) waitChannel(ctx context.Context, conn *quic.Conn, channel string) (MsgReadWriter, error) {
	waiter := make(chan bulkChannelResult, 1)
	s.lock.Lock()
	if s.closed || s.liveConnLocked() != conn {
		s.lock.Unlock()
		return nil, io.EOF
	}
	if rw, ok := s.channels[channel]; ok {
		s.lock.Unlock()
		return s.claimChannel(ctx, channel, rw)
	}
	s.waiters[channel] = append(s.waiters[channel], waiter)
	s.lock.Unlock()
	defer s.removeWaiter(channel, waiter)

	select {
	case result := <-waiter:
		if result.err != nil {
			return nil, result.err
		}
		return s.claimChannel(ctx, channel, result.rw)
	case <-ctx.Done():
		return nil, errBulkChannelTimeout
	}
}

func (s *bulkSession) removeWaiter(channel string, waiter chan bulkChannelResult) {
	s.lock.Lock()
	defer s.lock.Unlock()
	waiters := s.waiters[channel]
	if i := slices.Index(waiters, waiter); i >= 0 {
		waiters = slices.Delete(waiters, i, i+1)
		if len(waiters) == 0 {
			delete(s.waiters, channel)
		} else {
			s.waiters[channel] = waiters
		}
	}
}

func (s *bulkSession) storeChannel(conn *quic.Conn, channel string, rw MsgReadWriter, replace bool) error {
	s.lock.Lock()
	if s.closed || s.liveConnLocked() != conn {
		s.lock.Unlock()
		return io.EOF
	}
	old, exists := s.channels[channel]
	if exists && !replace {
		s.lock.Unlock()
		return errors.New("duplicate bulk channel")
	}
	if !exists && len(s.channels) >= maxBulkChannelsPerSession {
		s.lock.Unlock()
		return errBulkChannelLimit
	}
	s.channels[channel] = rw
	waiters := s.waiters[channel]
	delete(s.waiters, channel)
	s.lock.Unlock()

	if exists {
		if closer, ok := old.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				s.sidecar.log.Debug("Bulk sidecar replaced channel close failed", "peer", s.remoteID, "channel", channel, "err", err)
			}
		}
		bulkSidecarChannelReplaceMeter.Mark(1)
		s.sidecar.stats.markChannelReplaced(channel)
	} else {
		bulkSidecarChannelOpenMeter.Mark(1)
		s.sidecar.stats.markChannelOpened(channel)
	}
	s.sidecar.log.Debug("Bulk sidecar channel opened", "peer", s.remoteID, "channel", channel)

	for _, waiter := range waiters {
		waiter <- bulkChannelResult{rw: rw}
		close(waiter)
	}
	return nil
}
