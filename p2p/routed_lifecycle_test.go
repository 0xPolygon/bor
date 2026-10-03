package p2p

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

type countedRoutedRW struct {
	*closableScriptedRW
	closes atomic.Int32
}

func newCountedRoutedRW() *countedRoutedRW {
	return &countedRoutedRW{closableScriptedRW: &closableScriptedRW{stressMsgRW: newStressMsgRW()}}
}

func (rw *countedRoutedRW) Close() error {
	rw.closes.Add(1)
	return rw.closableScriptedRW.Close()
}

func waitRoutedClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("routed reader did not shut down")
	}
}

func TestRoutedLifecycleForwardCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    uint32
		deliver bool
	}{
		{"waiting for reader", 1, false},
		{"waiting for consumption", 1, true},
		{"empty message", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			primary := newCountedRoutedRW()
			rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
			defer rw.Close()
			forwarded := make(chan error, 1)
			go func() {
				forwarded <- rw.forwardMsg(Msg{Size: test.size, Payload: bytes.NewReader([]byte{0xc0})}, nil)
			}()
			if test.deliver {
				readRoutedTestMessage(t, rw)
			}
			require.NoError(t, rw.Close())
			require.NoError(t, rw.Close())
			select {
			case err := <-forwarded:
				require.ErrorIs(t, err, io.EOF)
			case <-time.After(time.Second):
				t.Fatal("pending forward did not stop")
			}
			require.Equal(t, int32(1), primary.closes.Load())
			_, err := rw.ReadMsg()
			require.ErrorIs(t, err, io.EOF)
			require.ErrorIs(t, rw.WriteMsg(Msg{}), io.EOF)
		})
	}
}

func TestRoutedLifecyclePrimaryError(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	primary := newCountedRoutedRW()
	bulk := newCountedRoutedRW()
	rw := NewChannelRoutedMsgReadWriter(primary, bulk, "bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
	defer rw.Close()
	bulk.PushMsg(3)
	readRoutedTestMessage(t, rw)
	primary.results <- scriptedResult{err: io.ErrUnexpectedEOF}
	waitRoutedClosed(t, rw.Done())
	waitRoutedClosed(t, bulk.closed)
	for range 2 {
		_, err := rw.ReadMsg()
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}
	require.NoError(t, rw.Close())
	require.False(t, rw.HasBulk())
	require.Equal(t, int32(1), bulk.closes.Load())
}

func TestRoutedLifecyclePeerShutdown(t *testing.T) {
	for _, events := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "events"}[events], func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			done := make(chan struct{})
			proto := &protoRW{in: make(chan Msg, 1), closed: done}
			var primary MsgReadWriter = proto
			if events {
				primary = newMsgEventer(proto, new(event.Feed), enode.ID{}, "eth", "", "")
			}
			bulk := newCountedRoutedRW()
			rw := NewChannelRoutedMsgReadWriter(primary, bulk, "bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
			defer rw.Close()
			proto.in <- Msg{Size: 1, Payload: bytes.NewReader([]byte{0xc0})}
			readRoutedTestMessage(t, rw)
			bulk.PushMsg(3)
			close(done)
			waitRoutedClosed(t, rw.Done())
			waitRoutedClosed(t, bulk.closed)
			_, err := rw.ReadMsg()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestRoutedLifecycleAttachmentShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	primary := newCountedRoutedRW()
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	var wg sync.WaitGroup
	lanes := make([]*countedRoutedRW, 32)
	for i := range lanes {
		lanes[i] = newCountedRoutedRW()
		wg.Add(1)
		go func(lane *countedRoutedRW) {
			defer wg.Done()
			rw.AttachBulkChannels([]string{"bulk", "control", "bulk", ""}, lane)
		}(lanes[i])
	}
	require.NoError(t, rw.Close())
	wg.Wait()
	require.False(t, rw.HasBulk())
	for _, lane := range lanes {
		waitRoutedClosed(t, lane.closed)
		require.Equal(t, int32(1), lane.closes.Load())
	}
	late := newCountedRoutedRW()
	rw.AttachBulkChannel("bulk", late)
	waitRoutedClosed(t, late.closed)
	require.False(t, rw.HasBulk())
}

func TestRoutedLifecycleSharedLaneReplacement(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	primary := newCountedRoutedRW()
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	defer rw.Close()
	old, next := newCountedRoutedRW(), newCountedRoutedRW()
	rw.AttachBulkChannels([]string{"control", "bulk"}, old)
	rw.AttachBulkChannel("control", next)
	require.Equal(t, int32(0), old.closes.Load())
	old.PushMsg(3)
	msg := readRoutedTestMessage(t, rw)
	require.Equal(t, uint64(3), msg.Code)
	rw.AttachBulkChannel("bulk", newCountedRoutedRW())
	waitRoutedClosed(t, old.closed)
	next.PushMsg(4)
	msg = readRoutedTestMessage(t, rw)
	require.Equal(t, uint64(4), msg.Code)
	require.NoError(t, msg.Discard())
	require.NoError(t, rw.Close())
	require.Equal(t, int32(1), old.closes.Load())
	require.Equal(t, int32(1), next.closes.Load())
}

func TestRoutedLifecycleCloseErrors(t *testing.T) {
	primary, bulk := newCountedRoutedRW(), newCountedRoutedRW()
	primary.closeErr = io.ErrClosedPipe
	bulk.closeErr = errPartialPayload
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	rw.AttachBulkChannels([]string{"control", "bulk"}, bulk)
	for range 2 {
		err := rw.Close()
		require.ErrorIs(t, err, io.ErrClosedPipe)
		require.ErrorIs(t, err, errPartialPayload)
		require.Equal(t, errPartialPayload.Error()+"\n"+io.ErrClosedPipe.Error(), err.Error())
	}
	require.Equal(t, int32(1), primary.closes.Load())
	require.Equal(t, int32(1), bulk.closes.Load())
	eventer := newMsgEventer(primary, new(event.Feed), enode.ID{}, "eth", "", "")
	require.Nil(t, eventer.Done())
}

func TestRoutedLifecycleLaneCancellation(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery", true: "consumption"}[delivered], func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
			defer rw.Close()
			closed := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				result <- rw.forwardMsg(Msg{Size: 1, Payload: bytes.NewReader([]byte{0xc0})}, closed)
			}()
			if delivered {
				readRoutedTestMessage(t, rw)
			}
			close(closed)
			select {
			case err := <-result:
				require.ErrorIs(t, err, io.EOF)
			case <-time.After(time.Second):
				t.Fatal("lane cancellation did not release forward")
			}
			require.Nil(t, rw.terminalError())
		})
	}
}

func TestRoutedLifecycleIdlePeerShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	done := make(chan struct{})
	rw := NewMultiChannelRoutedMsgReadWriter(&protoRW{closed: done}, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	defer rw.Close()
	close(done)
	waitRoutedClosed(t, rw.Done())
	_, err := rw.ReadMsg()
	require.ErrorIs(t, err, io.EOF)
}

func TestRoutedLifecyclePrimaryPayloadError(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	primary := newCountedRoutedRW()
	rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	defer rw.Close()
	primary.results <- scriptedResult{msg: Msg{Size: 2, Payload: &partialErrorReader{}}}
	msg := readRoutedTestMessage(t, rw)
	_, err := io.ReadAll(msg.Payload)
	require.ErrorIs(t, err, errPartialPayload)
	waitRoutedClosed(t, rw.Done())
	_, err = rw.ReadMsg()
	require.ErrorIs(t, err, errPartialPayload)
}
