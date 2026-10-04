package p2p

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkSidecarCloseReleasesSocket(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	addr := server.bulk.Addr().(*net.UDPAddr)
	server.bulk.Close()
	conn, err := net.ListenUDP("udp", addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestBulkSocketBufferFailure(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, BulkSidecarSocketBuffers{}, configureBulkSidecarSocketBuffers(conn))
}

func TestBulkChannelCancelledWaiterRemoved(t *testing.T) {
	session := newHelloTestSession()
	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := session.waitChannel(ctx, nil, "eth-bulk")
		require.Error(t, err)
		require.Empty(t, session.waiters)
	}
}

func TestBulkConcurrentOpenCancellation(t *testing.T) {
	session := newHelloTestSession()
	gate := session.channelOpenGate()
	gate <- struct{}{}
	defer func() { <-gate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := session.openChannel(ctx, "eth-bulk")
	require.ErrorIs(t, err, context.Canceled)
}

func TestBulkChannelRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{"unknown", "eth-other", "snap-bulk", "wit-control"} {
		t.Run(name, func(t *testing.T) {
			session := newHelloTestSession()
			stream := &helloTestStream{writeLimit: 1024}
			require.NoError(t, writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: name}))
			require.Error(t, session.acceptChannel(nil, stream))
			require.Empty(t, session.channels)
			assertHelloStreamClosed(t, stream)
		})
	}
}

func TestBulkAuthRequiresMatchingTLSConnection(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	client := newTestBulkServer(t)
	t.Cleanup(client.close)
	server.setPeer(newTestTrackedPeer(client.localnode.Node()))
	proxy, err := quic.ListenAddr("127.0.0.1:0", client.bulk.tls, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxy.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientConn, err := quic.DialAddr(ctx, proxy.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	defer clientConn.CloseWithError(0, "test complete")
	proxyConn, err := proxy.Accept(ctx)
	require.NoError(t, err)
	defer proxyConn.CloseWithError(0, "test complete")
	upstream, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	defer upstream.CloseWithError(0, "test complete")
	result := make(chan error, 1)
	go func() { result <- client.bulk.initiateAuth(clientConn, server.localnode.Node()) }()
	downstream, err := proxyConn.AcceptStream(ctx)
	require.NoError(t, err)
	forward, err := upstream.OpenStreamSync(ctx)
	require.NoError(t, err)
	var hello bulkAuthHello
	require.NoError(t, readBulkControl(downstream, bulkAuthControlMaxSize, &hello))
	require.NoError(t, writeBulkControl(forward, hello))
	var challenge bulkAuthChallenge
	require.NoError(t, readBulkControl(forward, bulkAuthControlMaxSize, &challenge))
	require.NoError(t, writeBulkControl(downstream, challenge))
	select {
	case err := <-result:
		require.ErrorContains(t, err, "signature invalid")
	case <-ctx.Done():
		t.Fatal("authentication across different TLS connections did not finish")
	}
}

func TestBulkAuthValidatesIncomingMessages(t *testing.T) {
	for _, test := range []struct {
		name     string
		change   func(*bulkAuthHello)
		response []byte
		wantErr  string
	}{
		{"version", func(h *bulkAuthHello) { h.Version++ }, nil, "unsupported bulk auth version"},
		{"target", func(h *bulkAuthHello) { h.To = enode.ID{} }, nil, "target mismatch"},
		{"peer", func(h *bulkAuthHello) { h.From = enode.ID{} }, nil, errBulkSidecarNoPeer.Error()},
		{"short response", nil, []byte{1}, "signature length invalid"},
		{"response", nil, make([]byte, 64), "signature invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, client := newTestBulkServer(t), newTestBulkServer(t)
			t.Cleanup(server.close)
			t.Cleanup(client.close)
			server.setPeer(newTestTrackedPeer(client.localnode.Node()))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.CloseWithError(0, "test complete")) })
			stream, err := conn.OpenStreamSync(ctx)
			require.NoError(t, err)
			hello := bulkAuthHello{Version: bulkSidecarVersion, From: client.bulk.localID, To: server.bulk.localID}
			if test.change != nil {
				test.change(&hello)
			}
			require.NoError(t, writeBulkControl(stream, hello))
			if test.response != nil {
				var challenge bulkAuthChallenge
				require.NoError(t, readBulkControl(stream, bulkAuthControlMaxSize, &challenge))
				require.NoError(t, writeBulkControl(stream, bulkAuthResponse{Signature: test.response}))
			}
			select {
			case <-conn.Context().Done():
				require.ErrorContains(t, context.Cause(conn.Context()), test.wantErr)
			case <-ctx.Done():
				t.Fatal("invalid authentication did not close connection")
			}
		})
	}
}

func TestBulkClosedSessionRejectsChannelWait(t *testing.T) {
	session := newHelloTestSession()
	session.close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := session.waitChannel(ctx, nil, "eth-bulk")
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, session.waiters)
}

func TestBulkChannelNegotiatedProtocols(t *testing.T) {
	peer := newTestTrackedPeer(nil)
	require.ErrorContains(t, validateBulkChannel(peer, "unknown"), "unsupported")
	for _, channel := range []string{
		"eth-control", "eth-blocks", "eth-tx", "eth-tx-fetch", "eth-bulk",
		"snap-accounts", "snap-storage", "snap-code", "snap-trie", "wit-bulk",
	} {
		require.NoError(t, validateBulkChannel(peer, channel))
	}
	delete(peer.running, "snap")
	require.ErrorContains(t, validateBulkChannel(peer, "snap-trie"), "not negotiated")
	require.NoError(t, validateBulkChannel(peer, "eth-bulk"))
	close(peer.closed)
	require.ErrorIs(t, validateBulkChannel(peer, "eth-bulk"), errBulkSidecarNoPeer)
}

func TestBulkChannelIncomingAuthorization(t *testing.T) {
	for _, test := range []struct {
		name       string
		localID    byte
		negotiated bool
		wantErr    string
	}{
		{"initiator", 2, true, ""},
		{"non-initiator", 0, true, "non-initiator"},
		{"self", 1, true, "non-initiator"},
		{"unnegotiated", 2, false, "not negotiated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newHelloTestSession()
			session.sidecar.localID = enode.ID{test.localID}
			session.remoteID = enode.ID{1}
			session.peer = newTestTrackedPeer(nil)
			if !test.negotiated {
				delete(session.peer.running, "snap")
			}
			stream := &helloTestStream{writeLimit: 1024}
			require.NoError(t, writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: "snap-trie"}))
			err := session.acceptChannel(nil, stream)
			if test.wantErr == "" {
				require.NoError(t, err)
				require.Contains(t, session.channels, "snap-trie")
			} else {
				require.ErrorContains(t, err, test.wantErr)
				assertHelloStreamClosed(t, stream)
				require.Empty(t, session.channels)
			}
		})
	}
}

func TestBulkChannelDuplicateRetainsOriginal(t *testing.T) {
	session := newHelloTestSession()
	first := &helloTestStream{writeLimit: 1024}
	rw, err := session.openChannelStream(nil, first, "eth-bulk")
	require.NoError(t, err)
	second := &helloTestStream{writeLimit: 1024}
	require.NoError(t, writeBulkControl(second, bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}))
	require.ErrorContains(t, session.acceptChannel(nil, second), "duplicate")
	assertHelloStreamClosed(t, second)
	require.False(t, first.closed)
	require.Same(t, rw, session.channels["eth-bulk"])
}

func TestBulkClosedPeerCannotRecreateSession(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	peer := newTestTrackedPeer(server.localnode.Node())
	session := server.bulk.peerSession(peer.Node(), peer)
	close(peer.closed)
	server.bulk.DropPeer(peer.ID())
	require.Nil(t, server.bulk.peerSession(peer.Node(), peer))
	require.Empty(t, server.bulk.sessions)
	_, err := session.ensureConn(context.Background())
	require.ErrorIs(t, err, io.EOF)
	require.ErrorIs(t, session.storeChannel(nil, "eth-bulk", &closeTrackingRW{}), io.EOF)
}

func TestBulkAuthTranscriptFields(t *testing.T) {
	from, to := enode.ID{1}, enode.ID{2}
	a, b := [32]byte{3}, [32]byte{4}
	binding := bytes.Repeat([]byte{5}, bulkAuthBindingLength)
	want := bulkAuthTranscriptHash(from, to, a, b, binding, "server")
	for _, got := range [][]byte{
		bulkAuthTranscriptHash(to, to, a, b, binding, "server"),
		bulkAuthTranscriptHash(from, from, a, b, binding, "server"),
		bulkAuthTranscriptHash(from, to, b, b, binding, "server"),
		bulkAuthTranscriptHash(from, to, a, a, binding, "server"),
		bulkAuthTranscriptHash(from, to, a, b, bytes.Repeat([]byte{6}, bulkAuthBindingLength), "server"),
		bulkAuthTranscriptHash(from, to, a, b, binding, "client"),
	} {
		require.NotEqual(t, want, got)
	}
}

func TestBulkChannelRejectsStaleConnection(t *testing.T) {
	for _, incoming := range []bool{true, false} {
		session := newHelloTestSession()
		old, current := new(quic.Conn), new(quic.Conn)
		session.conn = current
		stream := &helloTestStream{writeLimit: 1024}
		if incoming {
			require.NoError(t, writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}))
			require.ErrorIs(t, session.acceptChannel(old, stream), io.EOF)
		} else {
			_, err := session.openChannelStream(old, stream, "eth-bulk")
			require.ErrorIs(t, err, io.EOF)
		}
		assertHelloStreamClosed(t, stream)
		require.Empty(t, session.channels)
		session.clearConn(old)
		require.Same(t, current, session.conn)
	}
}

func TestBulkFrameLargerThanReceiveWindow(t *testing.T) {
	left, right := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(left.close)
	t.Cleanup(right.close)
	if bytes.Compare(left.bulk.localID[:], right.bulk.localID[:]) > 0 {
		left, right = right, left
	}
	left.setQUICPort()
	right.setQUICPort()
	lp, rp := newTestTrackedPeer(right.localnode.Node()), newTestTrackedPeer(left.localnode.Node())
	left.setPeer(lp)
	right.setPeer(rp)
	sender, err := left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	receiver, err := right.bulk.OpenChannel(rp, "eth-bulk")
	require.NoError(t, err)
	payload := bytes.Repeat([]byte{0xac}, bulkMaxMessageSize)
	sent := make(chan error, 1)
	go func() {
		sent <- sender.WriteMsg(Msg{Code: 1, Size: uint32(len(payload)), Payload: bytes.NewReader(payload)})
	}()
	msg, err := receiver.ReadMsg()
	require.NoError(t, err)
	require.EqualValues(t, len(payload), msg.Size)
	hash := sha256.New()
	n, err := io.Copy(hash, msg.Payload)
	require.NoError(t, err)
	require.EqualValues(t, len(payload), n)
	expected := sha256.Sum256(payload)
	require.Equal(t, expected[:], hash.Sum(nil))
	require.NoError(t, <-sent)
}
