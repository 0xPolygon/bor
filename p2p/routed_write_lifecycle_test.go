package p2p

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

func TestRoutedWriteFailureStopsPeer(t *testing.T) {
	for _, events := range []bool{false, true} {
		for _, failure := range []error{&bulkWriteError{err: errPartialPayload, committed: true}, errPartialPayload} {
			t.Run(failure.Error(), func(t *testing.T) {
				defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
				written := make(chan error, 1)
				bulk := &partialFailMsgRW{closed: make(chan struct{}), err: failure}
				defer bulk.Close()
				closePeer, _, peer, stopped := testPeer([]Protocol{{Name: "eth", Length: 3, Run: func(peer *Peer, primary MsgReadWriter) error {
					if events {
						primary = newMsgEventer(primary, new(event.Feed), peer.ID(), "eth", "", "")
					}
					rw := NewChannelRoutedMsgReadWriter(primary, bulk, "bulk", func(uint64) bool { return true }).(*routedMsgReadWriter)
					defer rw.Close()
					written <- SendItems(rw, 2, uint64(42))
					<-peer.Done()
					return io.EOF
				}}})
				defer closePeer()
				select {
				case err := <-written:
					require.ErrorIs(t, err, errPartialPayload)
				case <-time.After(time.Second):
					t.Fatal("fatal bulk write did not return")
				}
				waitRoutedClosed(t, peer.Done())
				select {
				case err := <-stopped:
					require.ErrorIs(t, err, errPartialPayload)
				case <-time.After(time.Second):
					t.Fatal("peer did not stop after a broadcast write error")
				}
			})
		}
	}
}

func TestBulkQueuedWritesKeepCommittedFailure(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { require.NoError(t, local.Close()) })
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	rw := &bulkStreamMsgRW{stream: &limitedWriteConn{Conn: local, remaining: 5}, log: log.New()}
	results := make(chan error, 8)
	var writers sync.WaitGroup
	for range cap(results) {
		writers.Go(func() {
			results <- rw.WriteMsg(Msg{Code: 2, Size: 1, Payload: bytes.NewReader([]byte{1})})
		})
	}
	header := make([]byte, 5)
	_, err := io.ReadFull(remote, header)
	require.NoError(t, err)
	writers.Wait()
	for range cap(results) {
		var failure *bulkWriteError
		require.ErrorAs(t, <-results, &failure)
		require.True(t, failure.committed)
		require.ErrorIs(t, failure, errPartialPayload)
	}
}

func TestProtoWriteErrorAfterShutdown(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	rw := &protoRW{closed: closed, werr: make(chan error, 1)}
	rw.werr <- io.EOF
	reported := make(chan struct{})
	go func() {
		rw.reportWriteError(io.ErrUnexpectedEOF)
		close(reported)
	}()
	waitRoutedClosed(t, reported)
}
