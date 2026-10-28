package p2p

import (
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBulkSidecarOpenValidation(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	for _, peer := range []*Peer{nil, newTestTrackedPeer(nil)} {
		_, err := server.bulk.OpenChannelContext(nil, peer, "eth-bulk")
		require.ErrorIs(t, err, errBulkSidecarNoPeer)
	}
	peer := newTestTrackedPeer(server.localnode.Node())
	_, err := server.bulk.OpenChannel(peer, "unknown")
	require.ErrorContains(t, err, "unsupported")
	_, err = server.bulk.dialConn(t.Context(), peer.Node())
	require.ErrorIs(t, err, errBulkSidecarNoQUIC)
	close(peer.closed)
	_, err = server.bulk.OpenChannel(peer, "eth-bulk")
	require.ErrorIs(t, err, errBulkSidecarNoPeer)
	server.bulk.Close()
	_, err = server.bulk.OpenChannel(newTestTrackedPeer(peer.Node()), "eth-bulk")
	require.ErrorIs(t, err, io.EOF)
}

func TestBulkSidecarListenerClose(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	require.NoError(t, server.bulk.listener.Close())
	server.bulk.run()
	require.NoError(t, server.bulk.transport.Conn.Close())
	server.bulk.Close()
}

func TestBulkFrameReadSizeLimit(t *testing.T) {
	stream := &helloTestStream{}
	var header [bulkFrameHeaderSize]byte
	binary.BigEndian.PutUint32(header[8:], bulkMaxMessageSize+1)
	_, err := stream.Buffer.Write(header[:])
	require.NoError(t, err)
	rw := &bulkStreamMsgRW{stream: stream}
	_, err = rw.ReadMsg()
	require.ErrorContains(t, err, "bulk message too large")
}
