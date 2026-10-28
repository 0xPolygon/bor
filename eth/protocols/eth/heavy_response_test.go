package eth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestHeavyResponseQueryReleasesSlot(t *testing.T) {
	want := []rlp.RawValue{rlp.EmptyList}
	for range cap(heavyResponseServeSlots) + 1 {
		got, served := serveHeavyResponseQuery(&Peer{}, func() []rlp.RawValue {
			require.Len(t, heavyResponseServeSlots, 1)
			return want
		})
		require.True(t, served)
		require.Equal(t, want, got)
		require.Empty(t, heavyResponseServeSlots)
	}
}

type heavyResponseTestRW struct {
	*replyTestRW
	done chan struct{}
}

func (rw *heavyResponseTestRW) Done() <-chan struct{} { return rw.done }

func TestHeavyResponseQueryStopsOnShutdown(t *testing.T) {
	for _, source := range []string{"peer", "transport"} {
		for _, busy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/busy=%v", source, busy), func(t *testing.T) {
				if busy {
					fillHeavyResponseSlots(t)
				}
				rw := &heavyResponseTestRW{done: make(chan struct{})}
				peer := &Peer{rw: rw, term: make(chan struct{})}
				stop := func() { close(peer.term) }
				if source == "transport" {
					stop = func() { close(rw.done) }
				}
				if !busy {
					stop()
				}
				result := make(chan bool, 1)
				go func() {
					_, served := serveHeavyResponseQuery(peer, func() []rlp.RawValue {
						t.Error("a stopped request must not execute a lookup")
						return nil
					})
					result <- served
				}()
				if busy {
					select {
					case <-result:
						t.Fatal("query did not wait for capacity")
					case <-time.After(20 * time.Millisecond):
					}
					stop()
				}
				select {
				case served := <-result:
					require.False(t, served)
				case <-time.After(time.Second):
					t.Fatal("shutdown did not release queued query")
				}
				if busy {
					require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots))
				} else {
					require.Empty(t, heavyResponseServeSlots)
				}
			})
		}
	}
}

// Overload must neither report missing data nor disconnect the requester.
func TestHeavyResponseHandlersWaitForCapacity(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler msgHandler
		packet  any
		want    uint64
		reply   any
	}{
		{"bodies", handleGetBlockBodies, GetBlockBodiesPacket{RequestId: 1, GetBlockBodiesRequest: GetBlockBodiesRequest{{1}}}, BlockBodiesMsg, BlockBodiesRLPPacket{RequestId: 1}},
		{"receipts68", handleGetReceipts68, GetReceiptsPacket{RequestId: 2, GetReceiptsRequest: GetReceiptsRequest{{1}}}, ReceiptsMsg, ReceiptsRLPPacket{RequestId: 2}},
		{"receipts69", handleGetReceipts69, GetReceiptsPacket{RequestId: 3, GetReceiptsRequest: GetReceiptsRequest{{1}}}, ReceiptsMsg, ReceiptsRLPPacket{RequestId: 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rw := &replyTestRW{sent: make(chan p2p.Msg, 1)}
			peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: rw}

			fillHeavyResponseSlots(t)
			payload, err := rlp.EncodeToBytes(test.packet)
			require.NoError(t, err)

			backend := newTestBackend(0)
			defer backend.close()
			done := make(chan error, 1)
			go func() { done <- test.handler(backend, decoder{msg: payload}, peer) }()
			select {
			case <-done:
				t.Fatal("busy handler dropped the request")
			case <-time.After(300 * time.Millisecond):
			}
			require.Empty(t, rw.sent)
			require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots))
			for range cap(heavyResponseServeSlots) {
				<-heavyResponseServeSlots
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("handler did not serve the queued request")
			}
			require.Len(t, rw.sent, 1)
			msg := <-rw.sent
			require.Equal(t, test.want, msg.Code)
			want, err := rlp.EncodeToBytes(test.reply)
			require.NoError(t, err)
			var got rlp.RawValue
			require.NoError(t, msg.Decode(&got))
			require.Equal(t, want, []byte(got))
		})
	}
}

func fillHeavyResponseSlots(t *testing.T) {
	t.Helper()
	require.Empty(t, heavyResponseServeSlots)
	for range cap(heavyResponseServeSlots) {
		heavyResponseServeSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for len(heavyResponseServeSlots) > 0 {
			<-heavyResponseServeSlots
		}
	})
}
