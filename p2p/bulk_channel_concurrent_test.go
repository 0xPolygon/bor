package p2p

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBulkChannelPendingAckDoesNotBlockNext(t *testing.T) {
	left, right, lp, rp := channelReadyTestServers(t)
	sender := left.bulk.peerSession(lp.Node(), lp)
	conn, err := sender.ensureConn(t.Context())
	require.NoError(t, err)
	stream, err := conn.OpenStreamSync(t.Context())
	require.NoError(t, err)
	require.NoError(t, writeBulkControl(stream, bulkChannelHello{Version: bulkSidecarVersion, Channel: "eth-bulk"}))
	receiver := right.bulk.peerSession(rp.Node(), rp)
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("eth-bulk")
		return exists
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		_, err := left.bulk.OpenChannelContext(ctx, lp, "eth-control")
		opened <- err
	}()
	_, err = right.bulk.OpenChannelContext(ctx, rp, "eth-control")
	require.NoError(t, err, "a pending acknowledgement must not block another lane")
	require.NoError(t, <-opened)
	stream.CancelRead(quic.StreamErrorCode(bulkSidecarCloseErrorCode))
	stream.CancelWrite(quic.StreamErrorCode(bulkSidecarCloseErrorCode))
	require.Eventually(t, func() bool {
		_, exists := receiver.getChannel("eth-bulk")
		return !exists
	}, time.Second, time.Millisecond)
	require.NoError(t, conn.Context().Err())
}

func TestBulkChannelConcurrentRegistration(t *testing.T) {
	session := newHelloTestSession()
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- session.storeChannel(nil, "eth-bulk", &closeTrackingRW{closed: make(chan struct{})}, false)
		}()
	}
	workers.Wait()
	first, second := <-results, <-results
	if first == nil {
		require.ErrorContains(t, second, "duplicate bulk channel")
	} else {
		require.ErrorContains(t, first, "duplicate bulk channel")
		require.NoError(t, second)
	}
	require.Len(t, session.channels, 1)
}
