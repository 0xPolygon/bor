package eth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestHeavyResponseSmallQueries(t *testing.T) {
	for _, count := range []int{0, 1, 16} {
		hashes := make([]common.Hash, count)
		for _, test := range []struct {
			name     string
			handler  msgHandler
			packet   any
			observed bool
			code     uint64
		}{
			{"bodies", handleGetBlockBodies, GetBlockBodiesPacket{RequestId: 7, GetBlockBodiesRequest: hashes}, false, BlockBodiesMsg},
			{"observed bodies", handleGetBlockBodies, GetBlockBodiesPacket{RequestId: 7, GetBlockBodiesRequest: hashes}, true, BlockBodiesMsg},
			{"receipts68", handleGetReceipts68, GetReceiptsPacket{RequestId: 7, GetReceiptsRequest: hashes}, false, ReceiptsMsg},
			{"receipts69", handleGetReceipts69, GetReceiptsPacket{RequestId: 7, GetReceiptsRequest: hashes}, false, ReceiptsMsg},
		} {
			t.Run(fmt.Sprintf("%s/hashes=%d", test.name, count), func(t *testing.T) {
				msg := serveSmallResponseWithBusyLimiter(t, test.handler, test.packet, test.observed)
				require.Equal(t, test.code, msg.Code)
				var reply struct {
					RequestID uint64
					Data      []rlp.RawValue
				}
				require.NoError(t, msg.Decode(&reply))
				require.EqualValues(t, 7, reply.RequestID)
				require.Empty(t, reply.Data)
			})
		}
	}
}

func serveSmallResponseWithBusyLimiter(t *testing.T, handler msgHandler, packet any, observed bool) p2p.Msg {
	t.Helper()
	backend := newTestBackend(0)
	defer backend.close()
	limiter := NewHeavyResponseLimiter()
	fillHeavyResponseSlots(t, limiter)
	rw := &replyTestRW{sent: make(chan p2p.Msg, 1)}
	peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: rw, term: make(chan struct{}), heavyResponseLimiter: limiter}
	defer peer.Close()
	if observed {
		peer.SetObserver(func(peerpolicy.Evidence) {})
	}
	payload, err := rlp.EncodeToBytes(packet)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- handler(backend, decoder{msg: payload}, peer) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("small request waited behind the bulk lookup queue")
	}
	require.Len(t, limiter.slots, cap(limiter.slots), "small requests must not consume bulk lookup slots")
	require.Len(t, rw.sent, 1)
	return <-rw.sent
}
