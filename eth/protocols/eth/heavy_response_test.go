package eth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestHeavyResponseQueryReleasesSlot(t *testing.T) {
	want := []rlp.RawValue{rlp.EmptyList}
	for range cap(heavyResponseServeSlots) + 1 {
		got := serveHeavyResponseQuery(func() []rlp.RawValue {
			require.Len(t, heavyResponseServeSlots, 1)
			return want
		})
		require.Equal(t, want, got)
		require.Empty(t, heavyResponseServeSlots)
	}
}

func TestHeavyResponseQuerySkipsPastDeadline(t *testing.T) {
	fillHeavyResponseSlots(t)

	want := []rlp.RawValue{rlp.EmptyList}
	called := false
	got := serveHeavyResponseQuery(func() []rlp.RawValue {
		called = true
		return want
	})

	require.False(t, called, "timed-out queries must not execute without a slot")
	require.Empty(t, got)
	require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots),
		"a timed-out query must not release another worker's slot")
}

// The handlers must reply rather than error when the slots are full; a non-nil
// return here tears the connection down.
func TestHeavyResponseHandlersSurviveOverload(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler msgHandler
		packet  any
		want    uint64
		reply   any
	}{
		{"bodies", handleGetBlockBodies, GetBlockBodiesPacket{RequestId: 1}, BlockBodiesMsg, BlockBodiesRLPPacket{RequestId: 1}},
		{"receipts68", handleGetReceipts68, GetReceiptsPacket{RequestId: 2}, ReceiptsMsg, ReceiptsRLPPacket{RequestId: 2}},
		{"receipts69", handleGetReceipts69, GetReceiptsPacket{RequestId: 3}, ReceiptsMsg, ReceiptsRLPPacket{RequestId: 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, net := p2p.MsgPipe()
			defer app.Close()
			defer net.Close()

			fillHeavyResponseSlots(t)
			payload, err := rlp.EncodeToBytes(test.packet)
			require.NoError(t, err)

			replied := make(chan error, 1)
			go func() {
				// A nil backend ensures the rejected query cannot perform a lookup.
				replied <- test.handler(nil, decoder{msg: payload}, &Peer{rw: net})
			}()

			require.NoError(t, p2p.ExpectMsg(app, test.want, test.reply))
			require.NoError(t, <-replied)
			require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots))
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
