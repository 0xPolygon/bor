package p2p

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"golang.org/x/sync/semaphore"

	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func testBulkBudget(peer, global int64) *bulkBufferBudget {
	return &bulkBufferBudget{
		peer: semaphore.NewWeighted(peer), global: semaphore.NewWeighted(global),
		writePeer: semaphore.NewWeighted(peer), writeGlobal: semaphore.NewWeighted(global),
	}
}

func assertBulkBudgetAvailable(t *testing.T, budget *bulkBufferBudget, size uint32) {
	t.Helper()
	require.Eventually(t, func() bool {
		if !budget.tryAcquire(size) {
			return false
		}
		budget.release(size)
		if !budget.tryAcquireWrite(size) {
			return false
		}
		budget.releaseWrite(size)
		return true
	}, time.Second, time.Millisecond)
}

func TestBulkBufferBudget(t *testing.T) {
	for _, test := range []struct {
		name         string
		peer, global int64
	}{
		{"peer", 4, 8}, {"global", 8, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := testBulkBudget(test.peer, test.global)
			require.NoError(t, budget.acquire(context.Background(), 4))
			require.False(t, budget.tryAcquire(1))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- budget.acquire(ctx, 4) }()
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
			budget.release(4)
			assertBulkBudgetAvailable(t, budget, 4)
			require.True(t, budget.peer.TryAcquire(test.peer))
			budget.peer.Release(test.peer)
			require.True(t, budget.global.TryAcquire(test.global))
			budget.global.Release(test.global)
		})
	}
}

func TestBulkBufferGlobalWaitCancellation(t *testing.T) {
	budget := testBulkBudget(4, 4)
	require.True(t, budget.global.TryAcquire(4))
	defer budget.global.Release(4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- budget.acquire(ctx, 4) }()
	require.Eventually(t, func() bool {
		if budget.peer.TryAcquire(1) {
			budget.peer.Release(1)
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("global buffer wait did not stop")
	}
	require.True(t, budget.peer.TryAcquire(4))
	budget.peer.Release(4)
}

func TestBulkBufferProtocolSharing(t *testing.T) {
	budget := newBulkBufferBudget()
	for _, events := range []bool{false, true} {
		for range 3 {
			var primary MsgReadWriter = &protoRW{buffers: budget}
			if events {
				primary = newMsgEventer(primary, new(event.Feed), enode.ID{}, "eth", "", "")
			}
			rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
			require.Same(t, budget, rw.buffers)
			require.NoError(t, rw.Close())
		}
	}
	other := bulkBuffersFor(newStressMsgRW())
	require.NotSame(t, budget.peer, other.peer)
	require.Same(t, budget.global, other.global)
}

type observedBufferReader struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *observedBufferReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}

func newBudgetRoutedRW(t *testing.T, budget *bulkBufferBudget) (*routedMsgReadWriter, *countedRoutedRW) {
	t.Helper()
	rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	rw.buffers = budget
	t.Cleanup(func() { require.NoError(t, rw.Close()) })
	bulk := newCountedRoutedRW()
	rw.AttachBulkChannel("bulk", bulk)
	return rw, bulk
}

func TestRoutedBufferConsumption(t *testing.T) {
	for _, sharedPeer := range []bool{false, true} {
		t.Run(map[bool]string{true: "peer", false: "process"}[sharedPeer], func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			first := testBulkBudget(4, 4)
			second := testBulkBudget(4, 4)
			second.global = first.global
			if sharedPeer {
				second = first
			}
			rw1, lane1 := newBudgetRoutedRW(t, first)
			defer rw1.Close()
			rw2, lane2 := newBudgetRoutedRW(t, second)
			defer rw2.Close()
			lane1.results <- scriptedResult{msg: Msg{Size: 4, Payload: bytes.NewReader([]byte{1, 2, 3, 4})}}
			msg1 := readRoutedTestMessage(t, rw1)
			reader := &observedBufferReader{Reader: bytes.NewReader([]byte{5, 6, 7, 8}), started: make(chan struct{})}
			lane2.results <- scriptedResult{msg: Msg{Size: 4, Payload: reader}}
			buf := make([]byte, 1)
			_, err := msg1.Payload.Read(buf)
			require.NoError(t, err)
			require.Never(t, func() bool {
				select {
				case <-reader.started:
					return true
				default:
					return false
				}
			}, 20*time.Millisecond, time.Millisecond)
			require.NoError(t, msg1.Discard())
			waitRoutedClosed(t, reader.started)
			msg2 := readRoutedTestMessage(t, rw2)
			payload, err := io.ReadAll(msg2.Payload)
			require.NoError(t, err)
			require.Equal(t, []byte{5, 6, 7, 8}, payload)
			assertBulkBudgetAvailable(t, first, 4)
			assertBulkBudgetAvailable(t, second, 4)
		})
	}
}

func TestRoutedBufferShutdown(t *testing.T) {
	for _, deliver := range []bool{false, true} {
		t.Run(map[bool]string{true: "delivered", false: "queued"}[deliver], func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			budget := testBulkBudget(4, 4)
			rw, bulk := newBudgetRoutedRW(t, budget)
			defer rw.Close()
			reader := &observedBufferReader{Reader: bytes.NewReader([]byte{1, 2, 3, 4}), started: make(chan struct{})}
			bulk.results <- scriptedResult{msg: Msg{Size: 4, Payload: reader}}
			waitRoutedClosed(t, reader.started)
			var msg Msg
			if deliver {
				msg = readRoutedTestMessage(t, rw)
			}
			require.NoError(t, rw.Close())
			assertBulkBudgetAvailable(t, budget, 4)
			if deliver {
				payload, err := io.ReadAll(msg.Payload)
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				require.Empty(t, payload)
			}
		})
	}
}

func TestRoutedBufferWaitCancellation(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{true: "replace", false: "close"}[replace], func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			budget := testBulkBudget(4, 4)
			require.True(t, budget.tryAcquire(4))
			rw, bulk := newBudgetRoutedRW(t, budget)
			defer rw.Close()
			bulk.PushMsg(1)
			if replace {
				rw.AttachBulkChannel("bulk", newCountedRoutedRW())
			} else {
				require.NoError(t, rw.Close())
			}
			waitRoutedClosed(t, bulk.closed)
			budget.release(4)
			assertBulkBudgetAvailable(t, budget, 4)
		})
	}
}

func TestRoutedBufferWriteFallback(t *testing.T) {
	for _, globalFull := range []bool{false, true} {
		t.Run(map[bool]string{true: "global", false: "peer"}[globalFull], func(t *testing.T) {
			budget := testBulkBudget(4, 4)
			occupied := budget.peer
			if globalFull {
				occupied = budget.global
			}
			require.True(t, occupied.TryAcquire(4))
			primary := &recordingPrimary{stressMsgRW: newStressMsgRW()}
			defer primary.Close()
			bulk := &recordingPrimary{stressMsgRW: newStressMsgRW()}
			defer bulk.Close()
			rw := NewRoutedMsgReadWriter(primary, bulk, func(uint64) bool { return true }).(*routedMsgReadWriter)
			defer rw.Close()
			rw.buffers = budget
			require.NoError(t, rw.WriteMsg(Msg{Size: 4, Payload: bytes.NewReader([]byte{1, 2, 3, 4})}))
			require.Equal(t, [][]byte{{1, 2, 3, 4}}, primary.payloads)
			require.Empty(t, bulk.payloads)
			require.True(t, rw.HasBulk())
			occupied.Release(4)
			assertBulkBudgetAvailable(t, budget, 4)
		})
	}
}
