package p2p

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
)

type helloTestStream struct {
	writeTestStream
	writeLimit   int
	readDeadline error
	readCancel   quic.StreamErrorCode
	writeCancel  quic.StreamErrorCode
}

func (s *helloTestStream) Write(p []byte) (int, error) {
	if s.writeLimit < len(p) {
		n, err := s.Buffer.Write(p[:s.writeLimit])
		s.writeLimit -= n
		return n, errors.Join(err, errPartialPayload)
	}
	n, err := s.Buffer.Write(p)
	s.writeLimit -= n
	return n, err
}

func (s *helloTestStream) SetReadDeadline(time.Time) error       { return s.readDeadline }
func (s *helloTestStream) CancelRead(code quic.StreamErrorCode)  { s.readCancel = code }
func (s *helloTestStream) CancelWrite(code quic.StreamErrorCode) { s.writeCancel = code }

// newHelloTestSession builds a session that is a legitimate channel acceptor:
// authenticated, with every protocol negotiated, and a local ID above the
// remote so it is the side that accepts rather than opens streams.
func newHelloTestSession() *bulkSession {
	return &bulkSession{
		sidecar:  &BulkSidecar{log: log.New(), localID: enode.ID{2}},
		peer:     newTestTrackedPeer(nil),
		remoteID: enode.ID{1},
		channels: make(map[string]MsgReadWriter),
		waiters:  make(map[string][]chan bulkChannelResult),
	}
}

func assertHelloStreamClosed(t *testing.T, stream *helloTestStream) {
	t.Helper()
	require.True(t, stream.closed)
	require.Equal(t, quic.StreamErrorCode(bulkSidecarCloseErrorCode), stream.readCancel)
	require.Equal(t, quic.StreamErrorCode(bulkSidecarCloseErrorCode), stream.writeCancel)
}

func TestBulkChannelHelloWriteCleanup(t *testing.T) {
	for _, test := range []struct {
		name     string
		limit    int
		deadline error
	}{
		{"deadline", 100, errPartialPayload},
		{"header", 2, nil},
		{"payload", 5, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			closeErr := errors.New("close failed")
			stream := &helloTestStream{
				writeTestStream: writeTestStream{deadlineErr: test.deadline, closeErr: closeErr},
				writeLimit:      test.limit,
			}
			session := newHelloTestSession()
			rw, err := session.openChannelStream(nil, stream, "bodies")
			require.Nil(t, rw)
			require.ErrorIs(t, err, errPartialPayload)
			require.ErrorIs(t, err, closeErr)
			require.Empty(t, session.channels)
			assertHelloStreamClosed(t, stream)
		})
	}
}

func TestBulkChannelHelloReadCleanup(t *testing.T) {
	valid := &writeTestStream{}
	require.NoError(t, writeBulkControl(valid, bulkChannelHello{Version: bulkSidecarVersion, Channel: "bodies"}))
	for _, test := range []struct {
		name     string
		data     []byte
		deadline error
		hello    *bulkChannelHello
	}{
		{name: "deadline", deadline: errPartialPayload},
		{name: "header", data: []byte{0}},
		{name: "payload", data: valid.Bytes()[:5]},
		{name: "size", data: []byte{0, 0, 0, 0}},
		{name: "encoding", data: []byte{0, 0, 0, 1, 0xff}},
		{name: "version", hello: &bulkChannelHello{Version: bulkSidecarVersion + 1, Channel: "bodies"}},
		{name: "empty channel", hello: &bulkChannelHello{Version: bulkSidecarVersion}},
		{name: "long channel", hello: &bulkChannelHello{Version: bulkSidecarVersion, Channel: string(bytes.Repeat([]byte{'x'}, 65))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &helloTestStream{writeLimit: 1024, readDeadline: test.deadline}
			if test.hello != nil {
				require.NoError(t, writeBulkControl(stream, test.hello))
			} else {
				_, err := stream.Buffer.Write(test.data)
				require.NoError(t, err)
			}
			session := newHelloTestSession()
			require.Error(t, session.acceptChannel(nil, stream))
			require.Empty(t, session.channels)
			assertHelloStreamClosed(t, stream)
		})
	}
}

func TestBulkChannelHelloOwnership(t *testing.T) {
	sender := newHelloTestSession()
	receiver := newHelloTestSession()
	stream := &helloTestStream{writeLimit: 1024}
	rw, err := sender.openChannelStream(nil, stream, "eth-bulk")
	require.NoError(t, err)
	require.Same(t, rw, sender.channels["eth-bulk"])
	require.NoError(t, receiver.acceptChannel(nil, stream))
	require.Contains(t, receiver.channels, "eth-bulk")
	require.False(t, stream.closed)
	require.Zero(t, stream.readCancel)
	require.Zero(t, stream.writeCancel)
	require.NoError(t, rw.(io.Closer).Close())
	assertHelloStreamClosed(t, stream)
}

func TestBulkControlPayloadValidation(t *testing.T) {
	for _, msg := range []any{make(chan byte), bytes.Repeat([]byte{1}, bulkAuthControlMaxSize)} {
		stream := &writeTestStream{}
		require.Error(t, writeBulkControl(stream, msg))
		require.Empty(t, stream.Bytes())
	}
	hello := bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}
	payload, err := rlp.EncodeToBytes(hello)
	require.NoError(t, err)
	for _, exact := range []bool{false, true} {
		stream := &helloTestStream{writeLimit: 1024}
		require.NoError(t, writeBulkControl(stream, hello))
		limit := uint32(len(payload))
		if !exact {
			limit--
		}
		var got bulkChannelHello
		err := readBulkControl(stream, limit, &got)
		if exact {
			require.NoError(t, err)
			require.Equal(t, hello, got)
		} else {
			require.ErrorContains(t, err, "invalid size")
		}
	}
}
