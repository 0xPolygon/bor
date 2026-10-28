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
			require.Len(t, rw.sent, 1)
			msg := <-rw.sent
			require.Equal(t, uint64(BlockBodiesMsg), msg.Code)
			var reply BlockBodiesRLPPacket
			require.NoError(t, msg.Decode(&reply))
			require.Equal(t, query.RequestId, reply.RequestId)
			require.Empty(t, reply.BlockBodiesRLPResponse)
			require.Len(t, heavyResponseServeSlots, cap(heavyResponseServeSlots))
			if enabled {
				require.Equal(t, []peerpolicy.Evidence{{
					Family: peerpolicy.BodyReplies,
					Activity: peerpolicy.ActivityEvent{
						ServedResponses: 1, ServedBytes: uint64(msg.Size),
					},
				}}, events)
			} else {
				require.Empty(t, events)
			}
		})
	}
}
