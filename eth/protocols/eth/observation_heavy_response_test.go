package eth

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyBodyServingWorkerLimit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := map[bool]string{false: "unobserved", true: "observed"}[enabled]
		t.Run(name, func(t *testing.T) {
			fillHeavyResponseSlots(t)
			rw := &replyTestRW{sent: make(chan p2p.Msg, 1)}
			peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: rw}
			var events []peerpolicy.Evidence
			if enabled {
				peer.SetObserver(func(event peerpolicy.Evidence) { events = append(events, event) })
			}
			query := GetBlockBodiesPacket{RequestId: 7, GetBlockBodiesRequest: GetBlockBodiesRequest{{1}}}
			payload, err := rlp.EncodeToBytes(query)
			require.NoError(t, err)
			backend := newTestBackend(0)
			defer backend.close()
			done := make(chan error, 1)
			go func() { done <- handleGetBlockBodies(backend, decoder{msg: payload}, peer) }()
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
			if enabled {
				require.Len(t, events, 1)
				require.Equal(t, peerpolicy.BodyReplies, events[0].Family)
				require.Zero(t, events[0].Items)
			} else {
				require.Empty(t, events)
			}
		})
	}
}
