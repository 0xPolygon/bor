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
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlogwriter"
	"golang.org/x/sync/semaphore"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	bulkSidecarNextProto      = "bor-bulk/2"
	bulkSidecarVersion        = uint64(2)
	bulkAuthControlMaxSize    = 1024
	bulkChannelControlMaxSize = 256
	bulkFrameHeaderSize       = 12
	// Set to the largest ceiling any routed protocol declares (wit's 16MB —
	// witnesses are paged at eth.PageSize, 15MB). A lower cap would silently
	// push every witness page onto the fallback lane. eth and snap cap
	// themselves at 10MB and still reject oversized frames in their own
	// handlers; this is the transport bound, and buffering stays limited by
	// the per-peer and process budgets in bulk_buffer.go.
	bulkMaxMessageSize        = 16 * 1024 * 1024
	bulkDialTimeout           = 5 * time.Second
	bulkAuthTimeout           = 5 * time.Second
	bulkChannelOpenTimeout    = 5 * time.Second
	bulkMessageReadTimeout    = 30 * time.Second
	bulkMessageWriteTimeout   = 20 * time.Second
	bulkSidecarTLSServerName  = "bor-bulk-sidecar"
	bulkSocketReadBufferSize  = 8 * 1024 * 1024
	bulkSocketWriteBufferSize = 8 * 1024 * 1024
	bulkSidecarCloseErrorCode = quic.ApplicationErrorCode(0x424f52)
	bulkSidecarProtocolError  = quic.ApplicationErrorCode(0x424f53)
	bulkSidecarCertLifetime   = 365 * 24 * time.Hour
	// Leave time to retry a failed rotation before the current certificate expires.
	bulkSidecarCertRefresh = 24 * time.Hour
	bulkAuthBindingLabel   = "bor bulk sidecar auth binding"
	bulkAuthBindingLength  = 32
)

// maxBulkChannelsPerSession bounds the channel table a single remote can grow.
// The allowlist already rejects unknown names, so this is exactly the number of
// lanes that can legitimately exist.
var maxBulkChannelsPerSession = len(bulkChannels)

var (
	errBulkSidecarNoPeer  = errors.New("bulk sidecar peer not connected")
	errBulkSidecarNoQUIC  = errors.New("peer has no bulk sidecar endpoint")
	errBulkChannelTimeout = errors.New("bulk channel open timed out")
	errBulkChannelLimit   = errors.New("bulk channel limit reached")
)

type BulkSidecar struct {
	srv       *Server
	listener  *quic.Listener
	transport *quic.Transport
	tls       *tls.Config
	config    *quic.Config
	log       log.Logger

	localID enode.ID
	priv    *ecdsa.PrivateKey
	windows *semaphore.Weighted

	closeOnce sync.Once
	closeCh   chan struct{}

	lock     sync.Mutex
	sessions map[enode.ID]*bulkSession
}

type bulkSession struct {
	sidecar  *BulkSidecar
	peer     *Peer
	remote   *enode.Node
	remoteID enode.ID

	openGate   chan struct{}
	lock       sync.Mutex
	conn       *quic.Conn
	connClosed <-chan struct{}
	connReady  chan struct{}
	dialing    bool
	dialWait   chan struct{}
	dialCancel context.CancelFunc
	closed     bool
	channels   map[string]MsgReadWriter
	waiters    map[string][]chan bulkChannelResult
}

type bulkChannelResult struct {
	rw  MsgReadWriter
	err error
}

type bulkAuthHello struct {
	Version uint64
	From    enode.ID
	To      enode.ID
	Nonce   [32]byte
}

type bulkAuthChallenge struct {
	Nonce     [32]byte
	Signature []byte
}

type bulkAuthResponse struct {
	Signature []byte
}

type bulkChannelHello struct {
	Version uint64
	Channel string
}

type bulkFrameStream interface {
	io.Reader
	io.Writer
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type bulkStreamMsgRW struct {
	stream   bulkFrameStream
	channel  string
	log      log.Logger
	write    sync.Mutex
	writeErr error
}

func newBulkSidecar(srv *Server, listenAddr string) (*BulkSidecar, error) {
	certs := new(bulkCertRotator)
	// Fail startup if the first certificate cannot be generated.
	if _, err := certs.certificate(nil); err != nil {
		return nil, err
	}
	tlsConf := &tls.Config{
		GetCertificate: certs.certificate,
		NextProtos:     []string{bulkSidecarNextProto},
		MinVersion:     tls.VersionTLS13,
	}
	quicConf := &quic.Config{
		HandshakeIdleTimeout: bulkAuthTimeout,
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      15 * time.Second,
		// One stream per allowlisted lane, plus the authentication stream.
		MaxIncomingStreams:             int64(len(bulkChannels) + 1),
		MaxIncomingUniStreams:          -1,
		InitialStreamReceiveWindow:     bulkStreamReceiveWindow,
		MaxStreamReceiveWindow:         bulkStreamReceiveWindowMax,
		InitialConnectionReceiveWindow: bulkConnReceiveWindow,
		MaxConnectionReceiveWindow:     bulkConnReceiveWindowMax,
		Tracer: func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace {
			return bulkSidecarStats.newConnectionTrace()
		},
	}
	windows := newBulkQUICWindows(srv.MaxPeers, srv.MaxPendingPeers)

	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	socketBuffers := configureBulkSidecarSocketBuffers(udpConn)
	bulkSidecarStats.setSocketBuffers(socketBuffers)

	transport := &quic.Transport{
		Conn:        udpConn,
		Tracer:      bulkSidecarStats.newTransportRecorder(),
		ConnContext: bulkServerConnContext(srv, windows),
	}
	listener, err := transport.Listen(tlsConf, quicConf)
	if err != nil {
		_ = udpConn.Close()
		return nil, err
	}
	return &BulkSidecar{
		srv:       srv,
		listener:  listener,
		transport: transport,
		tls:       tlsConf,
		config:    quicConf,
		log:       srv.log,
		localID:   srv.localnode.ID(),
		priv:      srv.PrivateKey,
		windows:   windows,
		closeCh:   make(chan struct{}),
		sessions:  make(map[enode.ID]*bulkSession),
	}, nil
}

func (b *BulkSidecar) Addr() net.Addr {
	return b.listener.Addr()
}

func (b *BulkSidecar) Close() {
	b.closeOnce.Do(func() {
		close(b.closeCh)
		if b.transport != nil {
			if err := b.transport.Close(); err != nil {
				b.log.Debug("Bulk transport close failed", "err", err)
			}
			// Transport does not close a caller-owned UDP socket.
			if err := b.transport.Conn.Close(); err != nil {
				b.log.Debug("Bulk socket close failed", "err", err)
			}
		} else if b.listener != nil {
			_ = b.listener.Close()
		}
		b.lock.Lock()
		sessions := make([]*bulkSession, 0, len(b.sessions))
		for _, session := range b.sessions {
			sessions = append(sessions, session)
		}
		b.lock.Unlock()
		for _, session := range sessions {
			session.close()
		}
	})
}

func (b *BulkSidecar) run() {
	for {
		conn, err := b.listener.Accept(context.Background())
		if err != nil {
			select {
			case <-b.closeCh:
				return
			default:
				// Accept only fails terminally; the sidecar is off from here.
				b.log.Warn("Bulk sidecar listener stopped", "err", err)
				return
			}
		}
		go b.handleIncomingConn(conn)
	}
}

func (b *BulkSidecar) OpenChannel(peer *Peer, channel string) (MsgReadWriter, error) {
	return b.OpenChannelContext(context.Background(), peer, channel)
}

func (b *BulkSidecar) OpenChannelContext(ctx context.Context, peer *Peer, channel string) (MsgReadWriter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if peer == nil || peer.Node() == nil {
		return nil, errBulkSidecarNoPeer
	}
	if err := validateBulkChannel(peer, channel); err != nil {
		return nil, err
	}
	session := b.peerSession(peer.Node(), peer)
	if session == nil {
		return nil, io.EOF
	}
	ctx, cancel := context.WithTimeout(ctx, bulkChannelOpenTimeout)
	defer cancel()
	return session.openChannel(ctx, channel)
}

func (b *BulkSidecar) DropPeer(id enode.ID) {
	b.lock.Lock()
	session := b.sessions[id]
	delete(b.sessions, id)
	b.lock.Unlock()
	if session != nil {
		session.close()
	}
}

func (b *BulkSidecar) session(remote *enode.Node) *bulkSession {
	return b.peerSession(remote, nil)
}

func (b *BulkSidecar) peerSession(remote *enode.Node, peer *Peer) *bulkSession {
	remoteID := remote.ID()
	b.lock.Lock()
	defer b.lock.Unlock()
	if !b.accepting(peer) {
		return nil
	}
	if existing := b.sessions[remoteID]; existing != nil {
		existing.refreshRecord(remote)
		return existing
	}
	session := &bulkSession{
		sidecar:  b,
		peer:     peer,
		remote:   remote,
		remoteID: remoteID,
		channels: make(map[string]MsgReadWriter),
		waiters:  make(map[string][]chan bulkChannelResult),
	}
	b.sessions[remoteID] = session
	return session
}

func (b *BulkSidecar) accepting(peer *Peer) bool {
	select {
	case <-b.closeCh:
		return false
	default:
	}
	if peer != nil {
		select {
		case <-peer.Done():
			return false
		default:
		}
	}
	return true
}

func (b *BulkSidecar) handleIncomingConn(conn *quic.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), bulkAuthTimeout)
	defer cancel()

	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		_ = conn.CloseWithError(bulkSidecarProtocolError, "missing auth stream")
		return
	}
	remote, err := b.acceptAuth(conn, stream)
	if err != nil {
		_ = conn.CloseWithError(bulkSidecarProtocolError, err.Error())
		return
	}
	session := b.adoptConn(remote, conn)
	if session == nil {
		_ = conn.CloseWithError(bulkSidecarCloseErrorCode, "duplicate bulk connection")
		return
	}
	releaseBulkPendingAuth(conn.Context())
	session.runConn(conn)
}

func (b *BulkSidecar) dialConn(ctx context.Context, remote *enode.Node) (*quic.Conn, error) {
	endpoint, ok := remote.QUICEndpoint()
	if !ok {
		b.log.Debug("Bulk sidecar peer missing QUIC endpoint", "peer", remote.ID(), "node", remote.String(), "ip", remote.IPAddr(), "tcp", remote.TCP(), "udp", remote.UDP())
		return nil, errBulkSidecarNoQUIC
	}
	if b.srv.NetRestrict != nil && !b.srv.NetRestrict.ContainsAddr(endpoint.Addr()) {
		return nil, errNetRestrict
	}
	dialCtx, cancel := context.WithTimeout(ctx, bulkDialTimeout)
	defer cancel()

	releaseWindow, err := reserveBulkQUICWindow(b.windows)
	if err != nil {
		return nil, err
	}
	conn, err := b.transport.Dial(dialCtx, net.UDPAddrFromAddrPort(endpoint), newBulkSidecarVerifiedTLSConfig(), b.config)
	if err != nil {
		releaseWindow()
		return nil, err
	}
	context.AfterFunc(conn.Context(), releaseWindow)
	stop := context.AfterFunc(ctx, func() {
		if err := conn.CloseWithError(bulkSidecarCloseErrorCode, "bulk dial cancelled"); err != nil {
			b.log.Debug("Bulk dial close failed", "err", err)
		}
	})
	defer stop()
	if err := b.initiateAuth(conn, remote); err != nil {
		_ = conn.CloseWithError(bulkSidecarProtocolError, err.Error())
		return nil, err
	}
	return conn, nil
}

func (rw *bulkStreamMsgRW) ReadMsg() (Msg, error) {
	// Idle lanes may wait indefinitely, but a started header must finish within
	// one read timeout regardless of how many reads it takes.
	if err := rw.stream.SetReadDeadline(time.Time{}); err != nil {
		return Msg{}, err
	}
	var header [bulkFrameHeaderSize]byte
	if _, err := io.ReadFull(rw.stream, header[:1]); err != nil {
		return Msg{}, err
	}
	if err := rw.stream.SetReadDeadline(time.Now().Add(bulkMessageReadTimeout)); err != nil {
		return Msg{}, err
	}
	if _, err := io.ReadFull(rw.stream, header[1:]); err != nil {
		return Msg{}, err
	}
	size := binary.BigEndian.Uint32(header[8:])
	if size > bulkMaxMessageSize {
		return Msg{}, fmt.Errorf("bulk message too large: %d", size)
	}
	// Local buffer admission must not consume a network read timeout.
	if err := rw.stream.SetReadDeadline(time.Time{}); err != nil {
		return Msg{}, err
	}
	msg := Msg{
		Code:    binary.BigEndian.Uint64(header[:8]),
		Size:    size,
		Payload: io.LimitReader(&bulkPayloadReader{stream: rw.stream}, int64(size)),
	}
	bulkSidecarStats.markChannelRead(rw.channel)
	rw.log.Trace("Bulk sidecar read message", "code", msg.Code, "size", msg.Size)
	return msg, nil
}

func (rw *bulkStreamMsgRW) WriteMsg(msg Msg) error {
	rw.write.Lock()
	defer rw.write.Unlock()
	if rw.writeErr != nil {
		return rw.writeErr
	}
	err := rw.writeFrame(msg)
	if err != nil {
		rw.writeErr = err
		if closeErr := rw.Close(); closeErr != nil {
			rw.log.Debug("Bulk sidecar failed stream close", "err", closeErr)
		}
	}
	return err
}

func (rw *bulkStreamMsgRW) writeFrame(msg Msg) error {
	if msg.Size > bulkMaxMessageSize {
		return &bulkWriteError{err: fmt.Errorf("bulk message too large: %d", msg.Size)}
	}
	if err := rw.stream.SetWriteDeadline(time.Now().Add(bulkMessageWriteTimeout)); err != nil {
		return &bulkWriteError{err: err}
	}
	var header [bulkFrameHeaderSize]byte
	binary.BigEndian.PutUint64(header[:8], msg.Code)
	binary.BigEndian.PutUint32(header[8:], msg.Size)
	n, err := rw.stream.Write(header[:])
	if err != nil {
		return &bulkWriteError{err: err, committed: n > 0}
	}
	if n != len(header) {
		return &bulkWriteError{err: io.ErrShortWrite, committed: n > 0}
	}
	written, err := io.Copy(rw.stream, io.LimitReader(msg.Payload, int64(msg.Size)))
	if err != nil {
		return &bulkWriteError{err: err, committed: true}
	}
	if written != int64(msg.Size) {
		return &bulkWriteError{err: io.ErrUnexpectedEOF, committed: true}
	}
	bulkSidecarStats.markChannelWrite(rw.channel)
	rw.log.Trace("Bulk sidecar wrote message", "code", msg.Code, "size", msg.Size)
	return nil
}

func (rw *bulkStreamMsgRW) Close() error {
	var err error
	if closer, ok := rw.stream.(interface{ Close() error }); ok {
		err = closer.Close()
	}
	if canceler, ok := rw.stream.(interface {
		CancelRead(quic.StreamErrorCode)
		CancelWrite(quic.StreamErrorCode)
	}); ok {
		code := quic.StreamErrorCode(bulkSidecarCloseErrorCode)
		canceler.CancelRead(code)
		canceler.CancelWrite(code)
	}
	return err
}

func writeBulkControl(stream bulkFrameStream, msg interface{}) error {
	payload, err := rlp.EncodeToBytes(msg)
	if err != nil {
		return err
	}
	if len(payload) > bulkAuthControlMaxSize {
		return errors.New("bulk control payload too large")
	}
	if err := stream.SetWriteDeadline(time.Now().Add(bulkAuthTimeout)); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if _, err := stream.Write(size[:]); err != nil {
		return err
	}
	_, err = stream.Write(payload)
	return err
}

func configureBulkSidecarSocketBuffers(conn *net.UDPConn) BulkSidecarSocketBuffers {
	status := BulkSidecarSocketBuffers{
		ConfiguredReadBuffer:  bulkSocketReadBufferSize,
		ConfiguredWriteBuffer: bulkSocketWriteBufferSize,
	}
	if err := conn.SetReadBuffer(status.ConfiguredReadBuffer); err != nil {
		status.ConfiguredReadBuffer = 0
	}
	if err := conn.SetWriteBuffer(status.ConfiguredWriteBuffer); err != nil {
		status.ConfiguredWriteBuffer = 0
	}
	status.ActualReadBuffer = socketBufferSize(conn, syscall.SO_RCVBUF)
	status.ActualWriteBuffer = socketBufferSize(conn, syscall.SO_SNDBUF)
	return status
}

func socketBufferSize(conn *net.UDPConn, opt int) int {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0
	}
	value := 0
	controlErr := rawConn.Control(func(fd uintptr) {
		if size, sockErr := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, opt); sockErr == nil {
			value = size
		}
	})
	if controlErr != nil {
		return 0
	}
	return value
}

func readBulkControl(stream bulkFrameStream, maxSize uint32, out interface{}) error {
	if err := stream.SetReadDeadline(time.Now().Add(bulkAuthTimeout)); err != nil {
		return err
	}
	var size [4]byte
	if _, err := io.ReadFull(stream, size[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > maxSize {
		return fmt.Errorf("bulk control payload invalid size %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(stream, payload); err != nil {
		return err
	}
	return rlp.DecodeBytes(payload, out)
}
