package eth

import (
	"fmt"
	"math/big"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

func TestPeerRequestSessionContribution(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()
	for _, version := range []uint{ETH68, ETH69} {
		for _, excess := range []bool{false, true} {
			t.Run(fmt.Sprintf("eth%d/excess=%t", version, excess), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					checkRequestSessionContribution(t, backend, version, excess)
				})
			})
		}
	}
}

func checkRequestSessionContribution(t *testing.T, backend *testBackend, version uint, excess bool) {
	t.Helper()
	local, remote := p2p.MsgPipe()
	peer := NewPeer(version, p2p.NewPeer(enode.ID{1}, "", nil), local, nil)
	defer peer.Close()
	defer remote.Close()
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	tracker.SetReady(true)
	connection := new(peerpolicy.ConnectionActivity)
	connection.Start(clock.Now())
	tracker.BindConnection(peer.ID(), tracker.Connected(peer.ID()), connection)
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
	done := make(chan error, 1)
	go func() { done <- Handle(backend, peer) }()
	hash := backend.chain.GetBlockByNumber(1).Hash()
	for minute := range 10 {
		connection.Observe(clock.Now(), false)
		requestSessionData(t, remote, uint64(minute), hash, minute%4)
		synctest.Wait()
		clock.Run(time.Minute)
		time.Sleep(time.Minute)
	}
	if excess {
		for i := range peerRequestBurst + 10*peerRequestRate + 1 {
			connection.Observe(clock.Now(), false)
			requestSessionData(t, remote, uint64(i+10), hash, 3)
			synctest.Wait()
		}
	}
	got := tracker.Snapshot(peer.ID())
	_, eligible := tracker.RequestOnlySince(peer.ID(), connection)
	require.Equal(t, excess, eligible)
	require.Equal(t, "request-only", got.Contribution.Status)
	require.GreaterOrEqual(t, got.Contribution.ServedMinutes, uint64(10))
	wantAbuse := uint64(0)
	if excess {
		wantAbuse = 20
	}
	require.Equal(t, wantAbuse, got.AbuseRisk)
	t.Logf("eth/%d excess=%t abuse=%d request-only removal eligible=%t", version, excess, got.AbuseRisk, eligible)
	if excess {
		require.EqualValues(t, 1, got.Windows[peerpolicy.RequestVolume.String()])
		clock.Run(time.Minute)
		time.Sleep(time.Minute)
		require.Zero(t, tracker.Snapshot(peer.ID()).AbuseRisk)
	}
	if !excess {
		connection.Observe(clock.Now(), false)
		require.NoError(t, p2p.Send(remote, NewBlockMsg, &NewBlockPacket{Block: backend.chain.GetBlockByNumber(1), TD: big.NewInt(1)}))
		synctest.Wait()
		got = tracker.Snapshot(peer.ID())
		require.Equal(t, "unknown", got.Contribution.Status)
		require.EqualValues(t, 1, got.Contribution.UnclassifiedMessages)
		require.Zero(t, got.AbuseRisk)
	}
	require.False(t, tracker.RetireRequestOnly(peer.ID(), connection, 0))
	require.NoError(t, remote.Close())
	require.ErrorIs(t, <-done, p2p.ErrPipeClosed)
}

func requestSessionData(t *testing.T, remote *p2p.MsgPipeRW, id uint64, hash common.Hash, family int) {
	t.Helper()
	var code uint64
	var query interface{}
	switch family {
	case 0:
		code, query = GetBlockHeadersMsg, &GetBlockHeadersPacket{RequestId: id, GetBlockHeadersRequest: &GetBlockHeadersRequest{Origin: HashOrNumber{Hash: hash}, Amount: 1}}
	case 1:
		code, query = GetBlockBodiesMsg, &GetBlockBodiesPacket{RequestId: id, GetBlockBodiesRequest: GetBlockBodiesRequest{hash}}
	case 2:
		code, query = GetReceiptsMsg, &GetReceiptsPacket{RequestId: id, GetReceiptsRequest: GetReceiptsRequest{hash}}
	default:
		code, query = GetPooledTransactionsMsg, &GetPooledTransactionsPacket{RequestId: id}
	}
	require.NoError(t, p2p.Send(remote, code, query))
	msg, err := remote.ReadMsg()
	require.NoError(t, err)
	require.Equal(t, code+1, msg.Code)
	var reply struct {
		ID    uint64
		Items []rlp.RawValue
	}
	require.NoError(t, msg.Decode(&reply))
	require.Equal(t, id, reply.ID)
}
