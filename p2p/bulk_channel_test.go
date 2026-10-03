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

func newHelloTestSession() *bulkSession {
	return &bulkSession{
		sidecar:  &BulkSidecar{log: log.New()},
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
			rw, err := session.openChannelStream(stream, "bodies")
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
			require.Error(t, session.acceptChannel(stream))
			require.Empty(t, session.channels)
			assertHelloStreamClosed(t, stream)
		})
	}
}

func TestBulkChannelHelloOwnership(t *testing.T) {
	sender := newHelloTestSession()
	receiver := newHelloTestSession()
	stream := &helloTestStream{writeLimit: 1024}
	rw, err := sender.openChannelStream(stream, "bodies")
	require.NoError(t, err)
	require.Same(t, rw, sender.channels["bodies"])
	require.NoError(t, receiver.acceptChannel(stream))
	require.Contains(t, receiver.channels, "bodies")
	require.False(t, stream.closed)
	require.Zero(t, stream.readCancel)
	require.Zero(t, stream.writeCancel)
	require.NoError(t, rw.(io.Closer).Close())
	assertHelloStreamClosed(t, stream)
}
