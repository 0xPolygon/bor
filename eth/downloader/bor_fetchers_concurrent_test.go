package downloader

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

// mockPeer implements the Peer interface for testing peer version filtering
// in concurrentFetch.
type mockPeer struct {
	id               string
	protocol         uint
	receiptRequested atomic.Bool
}

func (m *mockPeer) Head() (common.Hash, *big.Int) { return common.Hash{}, new(big.Int) }
func (m *mockPeer) RequestHeadersByHash(common.Hash, int, int, bool, chan *eth.Response) (*eth.Request, error) {
	return nil, nil
}
func (m *mockPeer) RequestHeadersByNumber(uint64, int, int, bool, chan *eth.Response) (*eth.Request, error) {
	return nil, nil
}
func (m *mockPeer) RequestBodies([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	return nil, nil
}
func (m *mockPeer) RequestReceipts([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	m.receiptRequested.Store(true)
	// Return a valid request so concurrentFetch can track it.
	// peer field is nil so Close() is a no-op (test-safe per dispatcher.go).
	return &eth.Request{Peer: m.id}, nil
}
func (m *mockPeer) RequestWitnesses([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	return nil, nil
}
func (m *mockPeer) SupportsWitness() bool { return false }

// newTestDownloader creates a minimal Downloader with only the fields needed
// by concurrentFetch, avoiding the full New() constructor.
func newTestDownloader() *Downloader {
	return &Downloader{
		queue:    newQueue(blockCacheMaxItems, blockCacheInitialItems, nil),
		peers:    newPeerSet(),
		cancelCh: make(chan struct{}),
		dropPeer: func(string) {},
	}
}

// scheduleReceiptTask sets up the downloader's queue in SnapSync mode and
// schedules a header with non-empty receipts so concurrentFetch enters the
// receipt peer selection path.
func scheduleReceiptTask(d *Downloader) {
	d.queue.Prepare(1, SnapSync)

	header := &types.Header{
		Number:      big.NewInt(1),
		ReceiptHash: common.HexToHash("0x1"), // non-empty so receipts get scheduled
	}
	d.queue.Schedule([]*types.Header{header}, []common.Hash{header.Hash()}, 1)
}

// TestConcurrentFetchReceipts_OnlyEth68Peers verifies that concurrentFetch
// returns errPeersUnavailable when all peers are below eth/69, since only
// eth/69 peers include bor receipts in responses.
func TestConcurrentFetchReceipts_OnlyEth68Peers(t *testing.T) {
	d := newTestDownloader()
	scheduleReceiptTask(d)

	var mockPeers = make([]*mockPeer, 2)
	mockPeers[0] = &mockPeer{id: "peer-a", protocol: eth.ETH68}
	mockPeers[1] = &mockPeer{id: "peer-b", protocol: eth.ETH68}

	for _, peer := range mockPeers {
		pc := newPeerConnection(peer.id, peer.protocol, peer, log.New("peer", peer.id))
		if err := d.peers.Register(pc); err != nil {
			t.Fatal(err)
		}
	}

	if d.queue.PendingReceipts() == 0 {
		t.Fatal("expected pending receipts in queue")
	}

	err := d.concurrentFetch((*receiptQueue)(d), false)
	if err != errPeersUnavailable {
		t.Fatalf("expected errPeersUnavailable, got %v", err)
	}

	for _, peer := range mockPeers {
		if peer.receiptRequested.Load() {
			t.Errorf("peer %s should NOT have received a receipt request", peer.id)
		}
	}
}

// TestConcurrentFetchReceipts_MixedPeers verifies that concurrentFetch
// dispatches receipt requests only to eth/69 peers, skipping eth/68 ones.
func TestConcurrentFetchReceipts_MixedPeers(t *testing.T) {
	d := newTestDownloader()
	scheduleReceiptTask(d)

	var mockPeers = make([]*mockPeer, 2)
	mockPeers[0] = &mockPeer{id: "peer-eth68", protocol: eth.ETH68}
	mockPeers[1] = &mockPeer{id: "peer-eth69", protocol: eth.ETH69}

	for _, peer := range mockPeers {
		pc := newPeerConnection(peer.id, peer.protocol, peer, log.New("peer", peer.id))
		if err := d.peers.Register(pc); err != nil {
			t.Fatal(err)
		}
	}

	// Cancel the downloader after a short delay to allow the receipt request
	// to be dispatched to the eth/69 peer.
	go func() {
		<-time.After(1 * time.Second)
		close(d.cancelCh)
	}()

	err := d.concurrentFetch((*receiptQueue)(d), false)
	if err != errCanceled {
		t.Fatalf("expected errCanceled, got %v", err)
	}

	if mockPeers[0].receiptRequested.Load() {
		t.Error("eth/68 peer should NOT have received a receipt request")
	}

	if !mockPeers[1].receiptRequested.Load() {
		t.Error("eth/69 peer should have received a receipt request")
	}
}
