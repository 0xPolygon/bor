package eth

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

func TestPeerResponseContribution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        uint64
		empty, full interface{}
		handle      msgHandler
	}{
		{"headers", BlockHeadersMsg, &BlockHeadersPacket{RequestId: 1}, &BlockHeadersPacket{RequestId: 1, BlockHeadersRequest: BlockHeadersRequest{{Number: big.NewInt(1)}}}, handleBlockHeaders},
		{"bodies", BlockBodiesMsg, &BlockBodiesPacket{RequestId: 1}, &BlockBodiesPacket{RequestId: 1, BlockBodiesResponse: BlockBodiesResponse{{}}}, handleBlockBodies},
		{"receipts68", ReceiptsMsg, &ReceiptsPacket[*ReceiptList68]{RequestId: 1}, &ReceiptsPacket[*ReceiptList68]{RequestId: 1, List: []*ReceiptList68{{}}}, handleReceipts[*ReceiptList68]},
		{"receipts69", ReceiptsMsg, &ReceiptsPacket[*ReceiptList69]{RequestId: 1}, &ReceiptsPacket[*ReceiptList69]{RequestId: 1, List: []*ReceiptList69{{}}}, handleReceipts[*ReceiptList69]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, state := range []struct {
				name                        string
				requested, cancelled, empty bool
			}{
				{"unsolicited-empty", false, false, true},
				{"unsolicited-data", false, false, false},
				{"requested-empty", true, false, true},
				{"requested-data", true, false, false},
				{"cancelled-data", true, true, false},
			} {
				t.Run(state.name, func(t *testing.T) {
					packet := tc.full
					if state.empty {
						packet = tc.empty
					}
					checkResponseContribution(t, tc.code, packet, tc.handle, state.requested, state.cancelled, state.requested && !state.cancelled && !state.empty)
				})
			}
		})
	}
}

func checkResponseContribution(t *testing.T, code uint64, packet interface{}, handle msgHandler, requested, cancelled, protected bool) {
	t.Helper()
	peer, _ := newReplyTestPeer(t)
	tracker, connection := requestOnlyResponseTracker(peer.ID())
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
	if requested {
		sink := make(chan *Response)
		req := &Request{id: 1, want: code, code: code - 1, data: []uint64{}, sink: sink}
		require.NoError(t, peer.dispatchRequest(req))
		if cancelled {
			require.NoError(t, req.Close())
		}
		done := make(chan struct{})
		stop := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case response := <-sink:
				response.Done <- nil
			case <-stop:
			}
		}()
		t.Cleanup(func() { close(stop); <-done })
	}
	payload, err := rlp.EncodeToBytes(packet)
	require.NoError(t, err)
	sequence := connection.ObserveData(mclock.AbsTime(10 * time.Minute))
	require.NoError(t, peer.handleObserved(nil, p2p.Msg{Code: code, Size: uint32(len(payload)), Payload: bytes.NewReader(payload), ReceivedSequence: sequence}, handle))
	got := tracker.Snapshot(peer.ID())
	_, eligible := tracker.RequestOnlySince(peer.ID(), connection)
	require.Equal(t, !protected, eligible)
	require.Equal(t, protected, got.Contribution.UnclassifiedMessages > 0)
	require.EqualValues(t, 20, got.AbuseRisk)
	require.Zero(t, got.Contribution.AcceptedTransactions)
	if protected {
		require.EqualValues(t, 30, got.RetentionScore)
	} else {
		require.EqualValues(t, 10, got.RetentionScore)
	}
}

func requestOnlyResponseTracker(id string) (*peerpolicy.Tracker, *peerpolicy.ConnectionActivity) {
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	tracker.SetReady(true)
	connection := new(peerpolicy.ConnectionActivity)
	connection.Start(clock.Now())
	tracker.BindConnection(id, tracker.Connected(id), connection)
	for range 10 {
		sequence := connection.Observe(clock.Now(), false)
		tracker.Observe(id, peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageSequence: sequence}})
		clock.Run(time.Minute)
	}
	tracker.Observe(id, peerpolicy.Evidence{Reason: peerpolicy.ServingVolume})
	return tracker, connection
}

func TestPeerBlockBroadcastContribution(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()
	known := backend.chain.GetBlockByNumber(1)
	header := known.Header()
	header.Extra = []byte{1}
	unseen := types.NewBlockWithHeader(header).WithBody(*known.Body())
	header.ParentHash[0] ^= 1
	unknownParent := types.NewBlockWithHeader(header).WithBody(*known.Body())
	header.TxHash[0] ^= 1
	invalid := types.NewBlockWithHeader(header).WithBody(*known.Body())
	for _, tc := range []struct {
		name      string
		block     *types.Block
		protected bool
		abuse     uint64
	}{
		{"known", known, true, 20},
		{"unseen", unseen, true, 20},
		{"unknown-parent", unknownParent, true, 20},
		{"invalid-body", invalid, false, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, _ := newReplyTestPeer(t)
			tracker, connection := requestOnlyResponseTracker(peer.ID())
			peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
			packet := &NewBlockPacket{Block: tc.block, TD: big.NewInt(1)}
			payload, err := rlp.EncodeToBytes(packet)
			require.NoError(t, err)
			sequence := connection.ObserveData(mclock.AbsTime(10 * time.Minute))
			require.NoError(t, peer.handleObserved(backend, p2p.Msg{Code: NewBlockMsg, Size: uint32(len(payload)), Payload: bytes.NewReader(payload), ReceivedSequence: sequence}, handleNewBlock))
			got := tracker.Snapshot(peer.ID())
			_, eligible := tracker.RequestOnlySince(peer.ID(), connection)
			require.Equal(t, !tc.protected, eligible)
			require.Equal(t, tc.protected, got.Contribution.UnclassifiedMessages > 0)
			require.Equal(t, tc.abuse, got.AbuseRisk)
			require.Nil(t, got.Contribution.LastContributionAgeSeconds)
		})
	}
}

func TestPendingNewBlockKeepsSupplier(t *testing.T) {
	local := newTestBackend(1)
	defer local.close()
	remote := newTestBackend(2)
	defer remote.close()
	block := remote.chain.GetBlockByNumber(2)
	require.Equal(t, local.chain.CurrentBlock().Hash(), block.ParentHash())
	peer, _ := newReplyTestPeer(t)
	tracker, connection := requestOnlyResponseTracker(peer.ID())
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
	backend := &packetCapturingBackend{testBackend: local}
	payload, err := rlp.EncodeToBytes(&NewBlockPacket{Block: block, TD: big.NewInt(1)})
	require.NoError(t, err)
	seq := connection.ObserveData(mclock.AbsTime(10 * time.Minute))
	require.NoError(t, peer.handleObserved(backend, p2p.Msg{Code: NewBlockMsg, Size: uint32(len(payload)), Payload: bytes.NewReader(payload), ReceivedSequence: seq}, handleNewBlock))
	require.NotNil(t, backend.packet)
	require.False(t, local.chain.HasBlock(block.Hash(), block.NumberU64()))
	_, during := tracker.RequestOnlySince(peer.ID(), connection)
	_, err = local.chain.InsertChain(types.Blocks{block}, false)
	require.NoError(t, err)
	peer.ObserveUsefulBlock()
	_, after := tracker.RequestOnlySince(peer.ID(), connection)
	t.Logf("eligible while queued=%t; after validated import=%t", during, after)
	require.False(t, during, "honest block supplier became removable before asynchronous import")
	require.False(t, after)
}

func TestPeerPendingResponseProtectsRequester(t *testing.T) {
	for _, tc := range []struct {
		name              string
		request, response uint64
		packet            interface{}
		handle            msgHandler
	}{
		{"headers", GetBlockHeadersMsg, BlockHeadersMsg, &BlockHeadersPacket{RequestId: 1, BlockHeadersRequest: BlockHeadersRequest{{Number: big.NewInt(1)}}}, handleBlockHeaders},
		{"bodies", GetBlockBodiesMsg, BlockBodiesMsg, &BlockBodiesPacket{RequestId: 1, BlockBodiesResponse: BlockBodiesResponse{{}}}, handleBlockBodies},
		{"receipts68", GetReceiptsMsg, ReceiptsMsg, &ReceiptsPacket[*ReceiptList68]{RequestId: 1, List: []*ReceiptList68{{}}}, handleReceipts[*ReceiptList68]},
		{"receipts69", GetReceiptsMsg, ReceiptsMsg, &ReceiptsPacket[*ReceiptList69]{RequestId: 1, List: []*ReceiptList69{{}}}, handleReceipts[*ReceiptList69]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, _ := newReplyTestPeer(t)
			tracker, connection := requestOnlyResponseTracker(peer.ID())
			peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
			sink := make(chan *Response)
			require.NoError(t, peer.dispatchRequest(&Request{id: 1, want: tc.response, code: tc.request, data: []uint64{}, sink: sink}))
			payload, err := rlp.EncodeToBytes(tc.packet)
			require.NoError(t, err)
			now := mclock.AbsTime(10 * time.Minute)
			var seq uint64
			if canContribute(tc.response) {
				seq = connection.ObserveData(now)
			} else {
				seq = connection.Observe(now, false)
			}
			done := make(chan error, 1)
			go func() {
				done <- peer.handleObserved(nil, p2p.Msg{Code: tc.response, Size: uint32(len(payload)), Payload: bytes.NewReader(payload), ReceivedSequence: seq}, tc.handle)
			}()
			var response *Response
			select {
			case response = <-sink:
			case <-time.After(5 * time.Second):
				t.Fatal("response did not reach consumer")
			}
			_, eligibleWhilePending := tracker.RequestOnlySince(peer.ID(), connection)
			response.Done <- nil
			require.NoError(t, <-done)
			_, eligibleAfter := tracker.RequestOnlySince(peer.ID(), connection)
			t.Logf("eligible during response handling=%t; after handling=%t", eligibleWhilePending, eligibleAfter)
			if eligibleWhilePending {
				t.Fatal("requester remained eligible while handling a correlated response")
			}
			require.False(t, eligibleAfter)
		})
	}
}
