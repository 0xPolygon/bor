package p2p

import (
	"bytes"
	"io"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
	"github.com/stretchr/testify/require"
)

type statsMsgReadWriter struct {
	MsgReadWriter
	stats *bulkSidecarStatsBook
}

func (rw *statsMsgReadWriter) bulkStats() *bulkSidecarStatsBook { return rw.stats }

func (rw *statsMsgReadWriter) Close() error {
	if closer, ok := rw.MsgReadWriter.(io.Closer); ok {
		return closer.Close()
	}
	if closer, ok := rw.MsgReadWriter.(interface{ Close() }); ok {
		closer.Close()
	}
	return nil
}

func TestBulkSidecarStatusIsolation(t *testing.T) {
	idle := newTestBulkServer(t)
	t.Cleanup(idle.close)
	idle.bulk.stats.setSocketBuffers(BulkSidecarSocketBuffers{ConfiguredReadBuffer: 1234})
	before := idle.bulk.Status()
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
	lrw, err := left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	rrw, err := right.bulk.OpenChannel(rp, "eth-bulk")
	require.NoError(t, err)
	primary := newStressMsgRW()
	t.Cleanup(primary.Close)
	routed := NewChannelRoutedMsgReadWriter(primary, lrw, "eth-bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
	sent := make(chan error, 1)
	go func() { sent <- SendItems(routed, 2, uint64(22)) }()
	require.NoError(t, ExpectMsg(rrw, 2, []uint64{22}))
	require.NoError(t, <-sent)
	require.True(t, routed.buffers.writePeer.TryAcquire(bulkPeerBufferLimit/2))
	require.NoError(t, SendItems(routed, 2, uint64(33)))
	routed.buffers.writePeer.Release(bulkPeerBufferLimit / 2)
	for _, server := range []*testBulkServer{left, right} {
		status := server.bulk.Status()
		require.Equal(t, uint64(1), status.Counters.SessionsEstablished)
		require.Equal(t, uint64(1), status.Counters.Channels["eth-bulk"].Opened)
		require.Positive(t, status.Wire.PacketsSent)
		require.Positive(t, status.Wire.PacketsReceived)
	}
	require.Equal(t, BulkSidecarChannelCounters{Opened: 1, WriteMessages: 1, WriteFallback: 1}, left.bulk.Status().Counters.Channels["eth-bulk"])
	require.Equal(t, BulkSidecarChannelCounters{Opened: 1, ReadMessages: 1}, right.bulk.Status().Counters.Channels["eth-bulk"])
	require.Equal(t, before, idle.bulk.Status())
	fresh := newTestBulkServer(t)
	t.Cleanup(fresh.close)
	require.Zero(t, fresh.bulk.Status().Counters.SessionsEstablished)
	require.Empty(t, fresh.bulk.Status().Counters.Channels)
	require.Equal(t, BulkSidecarWireCounters{}, fresh.bulk.Status().Wire)
}

func TestBulkSidecarWireStatusIsolation(t *testing.T) {
	left, right := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(left.close)
	t.Cleanup(right.close)
	before := right.bulk.Status()
	recorder := left.bulk.config.Tracer(t.Context(), false, quic.ConnectionID{}).AddProducer()
	t.Cleanup(func() { require.NoError(t, recorder.Close()) })
	recorder.RecordEvent(qlog.PacketSent{Raw: qlog.RawInfo{Length: 100}})
	recorder.RecordEvent(qlog.PacketReceived{Raw: qlog.RawInfo{Length: 200}})
	left.bulk.transport.Tracer.RecordEvent(qlog.PacketDropped{Raw: qlog.RawInfo{Length: 300}})
	require.Equal(t, BulkSidecarWireCounters{
		PacketsSent: 1, PacketsReceived: 1, PacketsDropped: 1,
		BytesSent: 100, BytesReceived: 200, BytesDropped: 300,
	}, left.bulk.Status().Wire)
	require.Positive(t, left.bulk.Status().SocketBuffers.ConfiguredReadBuffer)
	require.Positive(t, left.bulk.Status().SocketBuffers.ConfiguredWriteBuffer)
	require.Equal(t, before, right.bulk.Status())
}

func TestBulkSidecarChannelReplacementStatusIsolation(t *testing.T) {
	left, right := newHelloTestSession(), newHelloTestSession()
	first, second := newStressMsgRW(), newStressMsgRW()
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	require.NoError(t, left.storeChannel(nil, "eth-bulk", first))
	require.NoError(t, left.storeChannel(nil, "eth-bulk", second))
	active, _, _ := left.sidecar.stats.snapshot()
	idle, _, _ := right.sidecar.stats.snapshot()
	require.Equal(t, BulkSidecarChannelCounters{Opened: 1, Replaced: 1}, active.Channels["eth-bulk"])
	require.Empty(t, idle.Channels)
}

func TestRoutedBulkStatusIsolation(t *testing.T) {
	for _, reason := range []string{"oversize", "buffer-full", "write-error"} {
		t.Run(reason, func(t *testing.T) {
			left, right := new(bulkSidecarStatsBook), new(bulkSidecarStatsBook)
			primary := newStressMsgRW()
			t.Cleanup(primary.Close)
			bulk := &partialFailMsgRW{closed: make(chan struct{}), err: &bulkWriteError{err: errPartialPayload}}
			t.Cleanup(bulk.Close)
			routed := NewChannelRoutedMsgReadWriter(primary, &statsMsgReadWriter{MsgReadWriter: bulk, stats: left}, "eth-bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
			msg := Msg{Code: 2, Size: 1, Payload: bytes.NewReader([]byte{22})}
			switch reason {
			case "oversize":
				msg.Size = bulkMaxMessageSize + 1
			case "buffer-full":
				require.True(t, routed.buffers.writePeer.TryAcquire(bulkPeerBufferLimit/2))
				defer routed.buffers.writePeer.Release(bulkPeerBufferLimit / 2)
			}
			require.NoError(t, routed.WriteMsg(msg))
			active, _, _ := left.snapshot()
			idle, _, _ := right.snapshot()
			require.Equal(t, BulkSidecarChannelCounters{WriteFallback: 1}, active.Channels["eth-bulk"])
			require.Empty(t, idle.Channels)
		})
	}
}
