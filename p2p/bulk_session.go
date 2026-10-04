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
	bulkSidecarStats.markSessionEstablished()
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
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		if err := s.acceptChannel(conn, stream); err != nil {
			_ = conn.CloseWithError(bulkSidecarProtocolError, err.Error())
			return
		}
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
		bulkSidecarStats.markSessionEstablished()
		s.sidecar.log.Debug("Bulk sidecar session established", "peer", s.remoteID, "remote", conn.RemoteAddr())
		go s.runConn(conn)
		return conn, nil
	}
	_ = conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk connection superseded")
	return s.conn, nil
}

func (s *bulkSession) openChannel(ctx context.Context, channel string) (MsgReadWriter, error) {
	gate := s.channelOpenGate()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bulkChannelProtocol(channel) == "" {
		return nil, errors.New("invalid bulk channel")
	}
	if rw, ok := s.getChannel(channel); ok {
		return rw, nil
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
		return s.openChannelStream(conn, stream, channel)
	}
	return s.waitChannel(ctx, conn, channel)
}

func (s *bulkSession) channelOpenGate() chan struct{} {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.openGate == nil {
		s.openGate = make(chan struct{}, 1)
	}
	return s.openGate
}

func (s *bulkSession) openChannelStream(conn *quic.Conn, stream bulkFrameStream, channel string) (MsgReadWriter, error) {
	rw := &bulkStreamMsgRW{
		stream: stream, channel: channel,
		log: log.New("peer", s.remoteID, "channel", channel),
	}
	if err := writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: channel}); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	if err := s.storeChannel(conn, channel, rw); err != nil {
		return nil, errors.Join(err, rw.Close())
	}
	return rw, nil
}

func (s *bulkSession) acceptChannel(conn *quic.Conn, stream bulkFrameStream) (err error) {
	rw := &bulkStreamMsgRW{stream: stream}
	defer func() {
		if err != nil {
			err = errors.Join(err, rw.Close())
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
	if _, exists := s.getChannel(hello.Channel); exists {
		return errors.New("duplicate bulk channel")
	}
	rw.channel = hello.Channel
	rw.log = log.New("peer", s.remoteID, "channel", hello.Channel)
	return s.storeChannel(conn, hello.Channel, rw)
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
		return rw, nil
	}
	s.waiters[channel] = append(s.waiters[channel], waiter)
	s.lock.Unlock()
	defer s.removeWaiter(channel, waiter)

	select {
	case result := <-waiter:
		return result.rw, result.err
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

func (s *bulkSession) storeChannel(conn *quic.Conn, channel string, rw MsgReadWriter) error {
	s.lock.Lock()
	if s.closed || s.liveConnLocked() != conn {
		s.lock.Unlock()
		return io.EOF
	}
	old, exists := s.channels[channel]
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
		bulkSidecarStats.markChannelReplaced(channel)
	} else {
		bulkSidecarChannelOpenMeter.Mark(1)
		bulkSidecarStats.markChannelOpened(channel)
	}
	s.sidecar.log.Debug("Bulk sidecar channel opened", "peer", s.remoteID, "channel", channel)

	for _, waiter := range waiters {
		waiter <- bulkChannelResult{rw: rw}
		close(waiter)
	}
	return nil
}
