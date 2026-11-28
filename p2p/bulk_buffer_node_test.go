package p2p

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

type bufferRecorder struct {
	mu     sync.Mutex
	byPeer map[enode.ID][]*bulkBufferBudget
}

func (r *bufferRecorder) add(id enode.ID, b *bulkBufferBudget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byPeer[id] = append(r.byPeer[id], b)
}

func (r *bufferRecorder) snapshot(protos int) map[enode.ID][]*bulkBufferBudget {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[enode.ID][]*bulkBufferBudget)
	for id, budgets := range r.byPeer {
		if len(budgets) >= protos {
			out[id] = append([]*bulkBufferBudget(nil), budgets...)
		}
	}
	return out
}

const nodeBufferProtocols = 3

func newBufferServer(t *testing.T, maxPeers int, sidecar bool) (*Server, *bufferRecorder) {
	t.Helper()
	rec := &bufferRecorder{byPeer: make(map[enode.ID][]*bulkBufferBudget)}
	run := func(peer *Peer, rw MsgReadWriter) error {
		rec.add(peer.ID(), bulkBuffersFor(rw))
		<-peer.Done()
		return io.EOF
	}
	cfg := Config{
		PrivateKey: newkey(), MaxPeers: maxPeers, MaxPendingPeers: 8,
		NoDiscovery: true, NoDial: true, ListenAddr: "127.0.0.1:0",
		Protocols: []Protocol{
			{Name: "eth", Version: 68, Length: 17, Run: run},
			{Name: "snap", Version: 1, Length: 8, Run: run},
			{Name: "wit", Version: 1, Length: 8, Run: run},
		},
	}
	if sidecar {
		cfg.EnableBulkSidecar = true
		cfg.BulkListenAddr = "127.0.0.1:0"
	}
	server := &Server{Config: cfg}
	require.NoError(t, server.Start())
	t.Cleanup(server.Stop)
	return server, rec
}

func waitBufferPeers(t *testing.T, rec *bufferRecorder, want int) map[enode.ID][]*bulkBufferBudget {
	t.Helper()
	var snapshot map[enode.ID][]*bulkBufferBudget
	require.Eventually(t, func() bool {
		snapshot = rec.snapshot(nodeBufferProtocols)
		return len(snapshot) == want
	}, 5*time.Second, 5*time.Millisecond, "peers did not report budgets")
	return snapshot
}

func peerBudgets(t *testing.T, snapshot map[enode.ID][]*bulkBufferBudget) []*bulkBufferBudget {
	t.Helper()
	out := make([]*bulkBufferBudget, 0, len(snapshot))
	for _, budgets := range snapshot {
		require.Len(t, budgets, nodeBufferProtocols)
		for _, b := range budgets[1:] {
			require.Same(t, budgets[0], b, "protocols on one peer must share a budget")
		}
		out = append(out, budgets[0])
	}
	return out
}

func TestServerBulkBufferOwnership(t *testing.T) {
	for _, sidecar := range []bool{false, true} {
		name := "sidecar-off"
		if sidecar {
			name = "sidecar-on"
		}
		t.Run(name, func(t *testing.T) {
			nodeA, recA := newBufferServer(t, 10, sidecar)
			nodeB, recB := newBufferServer(t, 10, sidecar)
			remoteA1, _ := newBufferServer(t, 10, sidecar)
			remoteA2, _ := newBufferServer(t, 10, sidecar)
			remoteB1, _ := newBufferServer(t, 10, sidecar)

			connectBulkLivePeers(t, nodeA, remoteA1, false)
			connectBulkLivePeers(t, nodeA, remoteA2, false)
			connectBulkLivePeers(t, nodeB, remoteB1, false)

			aBudgets := peerBudgets(t, waitBufferPeers(t, recA, 2))
			bBudgets := peerBudgets(t, waitBufferPeers(t, recB, 1))
			require.Len(t, aBudgets, 2)
			require.Len(t, bBudgets, 1)

			// Both peers of node A share A's aggregate semaphores but keep
			// independent per-peer caps.
			a0, a1 := aBudgets[0], aBudgets[1]
			require.Same(t, a0.global, a1.global)
			require.Same(t, a0.writeGlobal, a1.writeGlobal)
			require.NotSame(t, a0.peer, a1.peer)

			// Node B's budget is backed by a different pool: no cross-node
			// sharing of the 128MiB allowance.
			b0 := bBudgets[0]
			require.NotSame(t, a0.global, b0.global)
			require.NotSame(t, a0.writeGlobal, b0.writeGlobal)

			// Exhausting node A's aggregate read allowance through one peer
			// blocks A's other peer but leaves node B free to reserve.
			require.True(t, a0.global.TryAcquire(bulkNodeBufferLimit))
			require.False(t, a1.tryAcquire(1), "second peer must share the drained node budget")
			require.True(t, b0.tryAcquire(1), "a different node must keep its own allowance")
			b0.release(1)
			a0.global.Release(bulkNodeBufferLimit)
			assertBulkBudgetAvailable(t, a1, bulkMaxMessageSize)

			// Exhausting A's aggregate write allowance blocks A's writers but
			// not B's, and receive reservations recover after release.
			require.True(t, a0.writeGlobal.TryAcquire(bulkNodeBufferLimit/2))
			require.False(t, a1.tryAcquireWrite(1), "writers share the node write budget")
			require.True(t, b0.tryAcquireWrite(1), "a different node keeps its write allowance")
			b0.releaseWrite(1)
			a0.writeGlobal.Release(bulkNodeBufferLimit / 2)
			assertBulkBudgetAvailable(t, a1, bulkMaxMessageSize)
		})
	}
}
