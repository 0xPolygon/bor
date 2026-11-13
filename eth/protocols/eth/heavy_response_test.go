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
	limiter := NewHeavyResponseLimiter()
	want := []rlp.RawValue{rlp.EmptyList}
	for range cap(limiter.slots) + 1 {
		got, served := serveHeavyResponseQuery(&Peer{heavyResponseLimiter: limiter}, func() []rlp.RawValue {
			require.Len(t, limiter.slots, 1)
			return want
		})
		require.True(t, served)
		require.Equal(t, want, got)
		require.Empty(t, limiter.slots)
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
				limiter := NewHeavyResponseLimiter()
				if busy {
					fillHeavyResponseSlots(t, limiter)
				}
				rw := &heavyResponseTestRW{done: make(chan struct{})}
				peer := &Peer{rw: rw, term: make(chan struct{}), heavyResponseLimiter: limiter}
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
					require.Len(t, limiter.slots, cap(limiter.slots))
				} else {
					require.Empty(t, limiter.slots)
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
		for _, limited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/limited=%v", test.name, limited), func(t *testing.T) {
				rw := &replyTestRW{sent: make(chan p2p.Msg, 1)}
				peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: rw}
				limiter := NewHeavyResponseLimiter()
				if limited {
					peer.SetHeavyResponseLimiter(limiter)
				}

				fillHeavyResponseSlots(t, limiter)
				payload, err := rlp.EncodeToBytes(test.packet)
				require.NoError(t, err)

				backend := newTestBackend(0)
				defer backend.close()
				done := make(chan error, 1)
				go func() { done <- test.handler(backend, decoder{msg: payload}, peer) }()
				if limited {
					select {
					case <-done:
						t.Fatal("busy handler dropped the request")
					case <-time.After(300 * time.Millisecond):
					}
					require.Empty(t, rw.sent)
					require.Len(t, limiter.slots, cap(limiter.slots))
					for range cap(limiter.slots) {
						<-limiter.slots
					}
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
				if !limited {
					require.Len(t, limiter.slots, cap(limiter.slots))
				}
			})
		}
	}
}

// A peer on a sidecar-disabled node must not wait on a slot even when every
// slot is occupied, regardless of limited peers elsewhere in the process.
func TestHeavyResponseQuerySkipsLimiterWhenDisabled(t *testing.T) {
	limiter := NewHeavyResponseLimiter()
	fillHeavyResponseSlots(t, limiter)
	want := []rlp.RawValue{rlp.EmptyList}
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, served := serveHeavyResponseQuery(&Peer{}, func() []rlp.RawValue {
			require.Len(t, limiter.slots, cap(limiter.slots))
			return want
		})
		require.True(t, served)
		require.Equal(t, want, got)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled limiter blocked on a full slot channel")
	}
	require.Len(t, limiter.slots, cap(limiter.slots))
}

// An unlimited peer serves immediately while a limited peer waits for capacity,
// in the same process and under the same full slot channel.
func TestHeavyResponseQueryMixedPeers(t *testing.T) {
	limiter := NewHeavyResponseLimiter()
	fillHeavyResponseSlots(t, limiter)
	want := []rlp.RawValue{rlp.EmptyList}

	free := make(chan struct{})
	go func() {
		defer close(free)
		_, served := serveHeavyResponseQuery(&Peer{}, func() []rlp.RawValue { return want })
		require.True(t, served)
	}()
	select {
	case <-free:
	case <-time.After(time.Second):
		t.Fatal("unlimited peer blocked behind a limited peer's full slots")
	}

	limited := &Peer{term: make(chan struct{}), heavyResponseLimiter: limiter}
	blocked := make(chan bool, 1)
	go func() {
		_, served := serveHeavyResponseQuery(limited, func() []rlp.RawValue { return want })
		blocked <- served
	}()
	select {
	case <-blocked:
		t.Fatal("limited peer served without a free slot")
	case <-time.After(50 * time.Millisecond):
	}
	close(limited.term)
	select {
	case served := <-blocked:
		require.False(t, served)
	case <-time.After(time.Second):
		t.Fatal("limited peer did not stop when its lifetime ended")
	}
	require.Len(t, limiter.slots, cap(limiter.slots))
}

func fillHeavyResponseSlots(t *testing.T, limiter *HeavyResponseLimiter) {
	t.Helper()
	require.Empty(t, limiter.slots)
	for range cap(limiter.slots) {
		limiter.slots <- struct{}{}
	}
	t.Cleanup(func() {
		for len(limiter.slots) > 0 {
			<-limiter.slots
		}
	})
}

func TestHeavyResponseLimiterNodeIsolation(t *testing.T) {
	busy := NewHeavyResponseLimiter()
	independent := NewHeavyResponseLimiter()
	fillHeavyResponseSlots(t, busy)
	for _, test := range []struct {
		name    string
		limiter *HeavyResponseLimiter
		blocked bool
	}{
		{"same node", busy, true},
		{"other enabled node", independent, false},
		{"disabled node", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := &Peer{term: make(chan struct{})}
			peer.SetHeavyResponseLimiter(test.limiter)
			defer peer.Close()
			done := make(chan bool, 1)
			go func() {
				_, served := serveHeavyResponseQuery(peer, func() []rlp.RawValue { return []rlp.RawValue{rlp.EmptyList} })
				done <- served
			}()
			if test.blocked {
				select {
				case <-done:
					t.Fatal("peer bypassed its node's full limiter")
				case <-time.After(20 * time.Millisecond):
				}
				<-busy.slots
			}
			select {
			case served := <-done:
				require.True(t, served)
			case <-time.After(time.Second):
				t.Fatal("peer could not use its node's available capacity")
			}
			if test.blocked {
				busy.slots <- struct{}{}
			}
		})
	}
}
