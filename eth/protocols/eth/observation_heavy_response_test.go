package eth

import (
	"testing"

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
			// A nil backend ensures an overloaded request never performs a lookup.
			require.NoError(t, handleGetBlockBodies(nil, decoder{msg: payload}, peer))
			require.Empty(t, rw.sent)
			require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots))
			require.Empty(t, events)
		})
	}
}
