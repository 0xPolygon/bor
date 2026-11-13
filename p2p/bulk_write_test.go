package p2p

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
)

type limitedWriteConn struct {
	net.Conn
	remaining int
	short     bool
}

func (c *limitedWriteConn) Write(p []byte) (int, error) {
	if len(p) < c.remaining {
		n, err := c.Conn.Write(p)
		c.remaining -= n
		return n, err
	}
	n := 0
	if c.remaining > 0 {
		var err error
		n, err = c.Conn.Write(p[:c.remaining])
		c.remaining -= n
		if err != nil {
			return n, err
		}
	}
	if c.short {
		return n, nil
	}
	return n, errPartialPayload
}

type recordingPrimary struct {
	*stressMsgRW
	payloads [][]byte
}

func (rw *recordingPrimary) WriteMsg(msg Msg) error {
	payload, err := io.ReadAll(msg.Payload)
	rw.payloads = append(rw.payloads, payload)
	return err
}

type writeTestStream struct {
	bytes.Buffer
	deadlineErr error
	closeErr    error
	closed      bool
}

func (s *writeTestStream) SetReadDeadline(time.Time) error  { return nil }
func (s *writeTestStream) SetWriteDeadline(time.Time) error { return s.deadlineErr }
func (s *writeTestStream) Close() error {
	s.closed = true
	return s.closeErr
}

func TestBulkWriteValidation(t *testing.T) {
	for _, test := range []struct {
		name        string
		size        uint32
		deadlineErr error
		closeErr    error
		committed   bool
	}{
		{name: "size", size: bulkMaxMessageSize + 1},
		{name: "deadline", deadlineErr: errPartialPayload},
		{name: "close", deadlineErr: errPartialPayload, closeErr: errPartialPayload},
		{name: "payload", size: 1, committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &writeTestStream{deadlineErr: test.deadlineErr, closeErr: test.closeErr}
			rw := &bulkStreamMsgRW{stream: stream, log: log.New()}
			err := rw.WriteMsg(Msg{Size: test.size, Payload: bytes.NewReader(nil)})
			var writeErr *bulkWriteError
			require.ErrorAs(t, err, &writeErr)
			require.Equal(t, test.committed, writeErr.committed)
			require.True(t, stream.closed)
			require.Equal(t, err, rw.WriteMsg(Msg{}))
			if test.committed {
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			} else {
				require.Empty(t, stream.Bytes())
			}
		})
	}
}

func TestRoutedBulkFrameWriteFailures(t *testing.T) {
	for _, size := range []int{0, 5, bulkFrameHeaderSize, bulkFrameHeaderSize + 2, bulkFrameHeaderSize + 4} {
		t.Run(fmt.Sprint(size), func(t *testing.T) { testRoutedBulkFrameWriteFailure(t, size, false) })
	}
	for _, size := range []int{0, 5} {
		t.Run(fmt.Sprintf("short-header-%d", size), func(t *testing.T) { testRoutedBulkFrameWriteFailure(t, size, true) })
	}
}

func TestRoutedWritePayloadBounds(t *testing.T) {
	payload := make([]byte, bulkMaxMessageSize+1)
	for _, size := range []int{bulkMaxMessageSize - 1, bulkMaxMessageSize, bulkMaxMessageSize + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			primary := &recordingPrimary{stressMsgRW: newStressMsgRW()}
			t.Cleanup(primary.Close)
			bulk := &recordingPrimary{stressMsgRW: newStressMsgRW()}
			t.Cleanup(bulk.Close)
			routed := NewRoutedMsgReadWriter(primary, bulk, func(uint64) bool { return true })
			err := routed.WriteMsg(Msg{Size: uint32(size), Payload: bytes.NewReader(payload[:size])})
			require.NoError(t, err)
			if size > bulkMaxMessageSize {
				require.Empty(t, bulk.payloads)
				require.Equal(t, [][]byte{payload[:size]}, primary.payloads)
			} else {
				require.Empty(t, primary.payloads)
				require.Equal(t, [][]byte{payload[:size]}, bulk.payloads)
			}
		})
	}
}

func TestRoutedWriteRejectsIncompletePayload(t *testing.T) {
	primary := &recordingPrimary{stressMsgRW: newStressMsgRW()}
	t.Cleanup(primary.Close)
	bulk := &recordingPrimary{stressMsgRW: newStressMsgRW()}
	t.Cleanup(bulk.Close)
	routed := NewRoutedMsgReadWriter(primary, bulk, func(uint64) bool { return true })
	err := routed.WriteMsg(Msg{Size: 2, Payload: bytes.NewReader([]byte{1})})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Empty(t, primary.payloads)
	require.Empty(t, bulk.payloads)
}

func testRoutedBulkFrameWriteFailure(t *testing.T, size int, short bool) {
	t.Helper()
	sender, receiver := net.Pipe()
	t.Cleanup(func() { require.NoError(t, sender.Close()) })
	t.Cleanup(func() { require.NoError(t, receiver.Close()) })
	primary := &recordingPrimary{stressMsgRW: newStressMsgRW()}
	t.Cleanup(primary.Close)
	bulk := &bulkStreamMsgRW{stream: &limitedWriteConn{Conn: sender, remaining: size, short: short}, log: log.New()}
	routed := NewRoutedMsgReadWriter(primary, bulk, func(uint64) bool { return true }).(*routedMsgReadWriter)
	remote := &bulkStreamMsgRW{stream: receiver, log: log.New()}
	readDone := make(chan error, 1)
	go func() {
		msg, err := remote.ReadMsg()
		if err == nil {
			payload := make([]byte, msg.Size)
			_, err = io.ReadFull(msg.Payload, payload)
		}
		readDone <- err
	}()

	payload := []byte{1, 2, 3, 4}
	err := routed.WriteMsg(Msg{Code: 2, Size: uint32(len(payload)), Payload: bytes.NewReader(payload)})
	if size == 0 {
		require.NoError(t, err)
		require.Equal(t, [][]byte{payload}, primary.payloads)
	} else {
		want := errPartialPayload
		if short {
			want = io.ErrShortWrite
		}
		require.ErrorIs(t, err, want)
		require.Empty(t, primary.payloads, "a committed frame must not be replayed")
	}
	require.False(t, routed.HasBulk())
	select {
	case err := <-readDone:
		if size < bulkFrameHeaderSize+len(payload) {
			require.Error(t, err, "remote must observe the truncated frame")
		}
	case <-time.After(time.Second):
		t.Fatal("remote reader was not released after the write failure")
	}
	var writeErr *bulkWriteError
	require.True(t, errors.As(bulk.WriteMsg(Msg{}), &writeErr))
	require.Equal(t, size > 0, writeErr.committed)
}
