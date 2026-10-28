package p2p

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
)

func TestBulkPayloadWaitsForBufferAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, receiver := net.Pipe()
		defer sender.Close()
		defer receiver.Close()
		primary := newCountedRoutedRW()
		rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
		defer rw.Close()
		rw.buffers = testBulkBudget(4, 4)
		require.True(t, rw.buffers.global.TryAcquire(4))
		rw.AttachBulkChannel("bulk", &bulkStreamMsgRW{stream: receiver, log: log.New()})
		frame := make([]byte, bulkFrameHeaderSize+4)
		binary.BigEndian.PutUint32(frame[8:], 4)
		copy(frame[bulkFrameHeaderSize:], []byte{1, 2, 3, 4})
		sent := make(chan error, 1)
		go func() { _, err := sender.Write(frame); sent <- err }()
		synctest.Wait()
		time.Sleep(bulkMessageReadTimeout + time.Second)
		primary.PushMsg(9)
		msg := readRoutedTestMessage(t, rw)
		require.Equal(t, uint64(9), msg.Code)
		require.NoError(t, msg.Discard())
		rw.buffers.global.Release(4)
		synctest.Wait()
		require.True(t, rw.HasBulk())
		msg = readRoutedTestMessage(t, rw)
		payload, err := io.ReadAll(msg.Payload)
		require.NoError(t, err)
		require.Equal(t, []byte{1, 2, 3, 4}, payload)
		require.NoError(t, <-sent)
		synctest.Wait()
		assertBulkBudgetAvailable(t, rw.buffers, 4)
	})
}

func TestBulkPayloadDeadlineSpansPartialReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, receiver := net.Pipe()
		defer sender.Close()
		defer receiver.Close()
		rw := &bulkStreamMsgRW{stream: receiver, log: log.New()}
		frame := make([]byte, bulkFrameHeaderSize+1)
		binary.BigEndian.PutUint32(frame[8:], 2)
		frame[bulkFrameHeaderSize] = 42
		sent := make(chan error, 1)
		go func() { _, err := sender.Write(frame); sent <- err }()
		msg, err := rw.ReadMsg()
		require.NoError(t, err)
		time.Sleep(2 * bulkMessageReadTimeout)
		buf := make([]byte, 1)
		_, err = io.ReadFull(msg.Payload, buf)
		require.NoError(t, err)
		require.Equal(t, byte(42), buf[0])
		require.NoError(t, <-sent)
		time.Sleep(bulkMessageReadTimeout - time.Second)
		start := time.Now()
		_, err = msg.Payload.Read(buf)
		require.True(t, isTimeoutError(err), "expected payload timeout, got %v", err)
		require.Equal(t, time.Second, time.Since(start))
	})
}

func TestBulkPayloadDeadlineError(t *testing.T) {
	want := errors.New("payload deadline failed")
	payload := &bulkPayloadReader{stream: &readDeadlineErrorStream{err: want}}
	n, err := payload.Read(nil)
	require.Zero(t, n)
	require.NoError(t, err)
	require.False(t, payload.started)
	n, err = payload.Read(make([]byte, 1))
	require.Zero(t, n)
	require.ErrorIs(t, err, want)
	require.False(t, payload.started)
}
