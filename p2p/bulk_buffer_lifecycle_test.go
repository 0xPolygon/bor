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

	"github.com/ethereum/go-ethereum/log"
)

func TestBulkBufferPeerProtocols(t *testing.T) {
	budgets := make(chan *bulkBufferBudget, 3)
	run := func(peer *Peer, rw MsgReadWriter) error {
		budgets <- bulkBuffersFor(rw)
		<-peer.Done()
		return io.EOF
	}
	closePeer, _, _, result := testPeer([]Protocol{
		{Name: "eth", Length: 1, Run: run},
		{Name: "snap", Length: 1, Run: run},
		{Name: "wit", Length: 1, Run: run},
	})
	defer closePeer()
	var first *bulkBufferBudget
	for range 3 {
		select {
		case budget := <-budgets:
			if first == nil {
				first = budget
			}
			require.Same(t, first, budget)
		case <-time.After(time.Second):
			t.Fatal("protocol did not start")
		}
	}
	closePeer()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("peer did not stop")
	}
}

func TestRoutedBufferReplacementPreservesDeliveredFrame(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	budget := testBulkBudget(4, 4)
	rw, old := newBudgetRoutedRW(t, budget)
	defer rw.Close()
	old.results <- scriptedResult{msg: Msg{Size: 4, Payload: bytes.NewReader([]byte{1, 2, 3, 4})}}
	msg := readRoutedTestMessage(t, rw)
	rw.AttachBulkChannel("bulk", newCountedRoutedRW())
	waitRoutedClosed(t, old.closed)
	require.False(t, budget.tryAcquire(1))
	payload, err := io.ReadAll(msg.Payload)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2, 3, 4}, payload)
	assertBulkBudgetAvailable(t, budget, 4)
	require.True(t, rw.HasBulkChannel("bulk"))
}

func TestRoutedBufferReadFailureRelease(t *testing.T) {
	for _, size := range []uint32{4, bulkMaxMessageSize + 1} {
		budget := testBulkBudget(4, 4)
		rw, bulk := newBudgetRoutedRW(t, budget)
		bulk.results <- scriptedResult{msg: Msg{Size: size, Payload: bytes.NewReader([]byte{1})}}
		waitRoutedClosed(t, bulk.closed)
		require.False(t, rw.HasBulk())
		assertBulkBudgetAvailable(t, budget, 4)
	}
}

func TestRoutedBufferMaximumFrame(t *testing.T) {
	budget := testBulkBudget(bulkMaxMessageSize, bulkMaxMessageSize)
	rw, bulk := newBudgetRoutedRW(t, budget)
	payload := bytes.Repeat([]byte{0x42}, bulkMaxMessageSize)
	bulk.results <- scriptedResult{msg: Msg{Size: bulkMaxMessageSize, Payload: bytes.NewReader(payload)}}
	msg := readRoutedTestMessage(t, rw)
	got, err := io.ReadAll(msg.Payload)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	assertBulkBudgetAvailable(t, budget, bulkMaxMessageSize)
}

func TestRoutedBufferAdmissionCancellation(t *testing.T) {
	rw := NewMultiChannelRoutedMsgReadWriter(newCountedRoutedRW(), func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
	defer rw.Close()
	rw.buffers = testBulkBudget(4, 4)
	require.True(t, rw.buffers.tryAcquire(4))
	defer rw.buffers.release(4)
	ctx, cancel := context.WithCancel(t.Context())
	bulk := newCountedRoutedRW()
	lane := rw.setBulkChannels([]string{"bulk"}, bulk, ctx.Done(), cancel)
	bulk.PushMsg(1)
	cancel()
	require.ErrorIs(t, rw.forwardBulkMsg(ctx, lane), context.Canceled)
}

func TestRoutedBufferWriteRelease(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  []byte
		deadline error
		wantErr  bool
	}{
		{"success", []byte{1, 2, 3, 4}, nil, false},
		{"short payload", []byte{1}, nil, true},
		{"fallback", []byte{1, 2, 3, 4}, io.ErrClosedPipe, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := testBulkBudget(4, 4)
			primary := &recordingPrimary{stressMsgRW: newStressMsgRW()}
			defer primary.Close()
			rw := NewMultiChannelRoutedMsgReadWriter(primary, func(uint64) string { return "bulk" }).(*routedMsgReadWriter)
			defer rw.Close()
			rw.buffers = budget
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream := &writeTestStream{deadlineErr: test.deadline}
			rw.setBulkChannels([]string{"bulk"}, &bulkStreamMsgRW{stream: stream, log: log.New()}, ctx.Done(), cancel)
			err := rw.WriteMsg(Msg{Size: 4, Payload: bytes.NewReader(test.payload)})
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assertBulkBudgetAvailable(t, budget, 4)
		})
	}
}

func TestBulkBufferedPayloadConcurrentRelease(t *testing.T) {
	budget := testBulkBudget(4, 4)
	require.True(t, budget.tryAcquire(4))
	payload := &bulkBufferedPayload{reader: *bytes.NewReader([]byte{1, 2, 3, 4}), budget: budget, size: 4}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			buf := make([]byte, 1)
			n, err := payload.Read(buf)
			if err == nil {
				require.Equal(t, 1, n)
			} else {
				require.ErrorIs(t, err, io.EOF)
			}
			payload.release()
		})
	}
	wg.Wait()
	require.Zero(t, payload.reader.Size())
	require.Nil(t, payload.budget)
	assertBulkBudgetAvailable(t, budget, 4)
}
