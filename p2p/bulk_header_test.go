package p2p

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
)

func TestBulkHeaderPartialTimeout(t *testing.T) {
	for _, size := range []int{1, 6, bulkFrameHeaderSize - 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sender, receiver := net.Pipe()
				defer sender.Close()
				defer receiver.Close()
				primary := newCountedRoutedRW()
				bulk := &bulkStreamMsgRW{stream: receiver, log: log.New()}
				rw := NewChannelRoutedMsgReadWriter(primary, bulk, "eth-bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
				defer rw.Close()
				_, err := sender.Write(make([]byte, size))
				require.NoError(t, err)
				time.Sleep(bulkMessageReadTimeout + time.Second)
				synctest.Wait()
				require.False(t, rw.HasBulk())
				primary.PushMsg(9)
				msg := readRoutedTestMessage(t, rw)
				require.Equal(t, uint64(9), msg.Code)
				require.NoError(t, msg.Discard())
				_, err = sender.Write([]byte{0})
				require.ErrorIs(t, err, io.ErrClosedPipe)
			})
		})
	}
}

func TestBulkHeaderIdleAndCompleteFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, receiver := net.Pipe()
		defer sender.Close()
		defer receiver.Close()
		rw := &bulkStreamMsgRW{stream: receiver, log: log.New()}
		for code := uint64(1); code <= 2; code++ {
			result := make(chan scriptedResult, 1)
			go func() { msg, err := rw.ReadMsg(); result <- scriptedResult{msg: msg, err: err} }()
			time.Sleep(2 * bulkMessageReadTimeout)
			select {
			case got := <-result:
				t.Fatalf("idle lane returned before a frame: %v", got.err)
			default:
			}
			frame := make([]byte, bulkFrameHeaderSize+1)
			binary.BigEndian.PutUint64(frame, code)
			binary.BigEndian.PutUint32(frame[8:], 1)
			frame[bulkFrameHeaderSize] = 42
			sent := make(chan error, 1)
			go func() { _, err := sender.Write(frame); sent <- err }()
			got := <-result
			require.NoError(t, got.err)
			require.Equal(t, code, got.msg.Code)
			time.Sleep(2 * bulkMessageReadTimeout)
			payload, err := io.ReadAll(got.msg.Payload)
			require.NoError(t, err)
			require.Equal(t, []byte{42}, payload)
			require.NoError(t, <-sent)
		}
	})
}

func TestBulkHeaderDeadlineSpansPartialReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, receiver := net.Pipe()
		defer sender.Close()
		defer receiver.Close()
		rw := &bulkStreamMsgRW{stream: receiver, log: log.New()}
		result := make(chan error, 1)
		go func() { _, err := rw.ReadMsg(); result <- err }()
		_, err := sender.Write([]byte{0})
		require.NoError(t, err)
		time.Sleep(bulkMessageReadTimeout - time.Second)
		_, err = sender.Write(make([]byte, 5))
		require.NoError(t, err)
		start := time.Now()
		select {
		case err := <-result:
			require.True(t, isTimeoutError(err), "expected header timeout, got %v", err)
			require.Equal(t, time.Second, time.Since(start))
		case <-time.After(2 * time.Second):
			t.Fatal("partial header did not time out at its original deadline")
		}
	})
}

type headerDeadlineStream struct {
	*helloTestStream
	calls  int
	failAt int
	err    error
}

func (s *headerDeadlineStream) SetReadDeadline(deadline time.Time) error {
	s.calls++
	if s.calls == s.failAt {
		return s.err
	}
	return s.helloTestStream.SetReadDeadline(deadline)
}

func TestBulkHeaderDeadlineErrors(t *testing.T) {
	for _, phase := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			want := errors.New("header deadline failed")
			stream := &headerDeadlineStream{helloTestStream: &helloTestStream{}, failAt: phase, err: want}
			_, err := stream.Buffer.Write(make([]byte, bulkFrameHeaderSize))
			require.NoError(t, err)
			rw := &bulkStreamMsgRW{stream: stream, log: log.New()}
			_, err = rw.ReadMsg()
			require.ErrorIs(t, err, want)
			require.Equal(t, phase, stream.calls)
		})
	}
}

func TestBulkHeaderReadErrors(t *testing.T) {
	for _, test := range []struct {
		size  int
		err   error
		calls int
	}{
		{0, io.EOF, 1},
		{6, io.ErrUnexpectedEOF, 2},
	} {
		t.Run(fmt.Sprint(test.size), func(t *testing.T) {
			stream := &headerDeadlineStream{helloTestStream: &helloTestStream{}}
			_, err := stream.Buffer.Write(make([]byte, test.size))
			require.NoError(t, err)
			rw := &bulkStreamMsgRW{stream: stream, log: log.New()}
			_, err = rw.ReadMsg()
			require.ErrorIs(t, err, test.err)
			require.Equal(t, test.calls, stream.calls)
		})
	}
}
