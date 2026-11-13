package downloader

import (
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
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
func (m *mockPeer) RequestReceipts([]common.Hash, []uint64, []uint64, chan *eth.Response) (*eth.Request, error) {
	m.receiptRequested.Store(true)
	// Return a valid request so concurrentFetch can track it.
	// peer field is nil so Close() is a no-op (test-safe per dispatcher.go).
	return &eth.Request{Peer: m.id, Sent: time.Now()}, nil
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
	scheduleReceiptTasks(d, 1)
}

func scheduleReceiptTasks(d *Downloader, count int) {
	d.queue.Prepare(1, SnapSync)

	var (
		headers []*types.Header
		hashes  []common.Hash
	)
	for i := 1; i <= count; i++ {
		header := &types.Header{
			Number:      big.NewInt(int64(i)),
			ReceiptHash: common.Hash{byte(i)},
		}
		headers = append(headers, header)
		hashes = append(hashes, header.Hash())
	}
	d.queue.Schedule(headers, hashes, 1)
}

// TestConcurrentFetchReceipts_OnlyEth68Peers verifies that concurrentFetch
// returns ErrPeersUnavailable when all peers are below eth/69, since only
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
	if err != ErrPeersUnavailable {
		t.Fatalf("expected ErrPeersUnavailable, got %v", err)
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

func TestConcurrentFetchReceipts_BackedOffPeer(t *testing.T) {
	d := newTestDownloader()
	scheduleReceiptTask(d)

	peer := &mockPeer{id: "peer-eth69", protocol: eth.ETH69}
	pc := newPeerConnection(peer.id, peer.protocol, peer, log.New("peer", peer.id))
	pc.backoffFor(time.Minute)
	if err := d.peers.Register(pc); err != nil {
		t.Fatal(err)
	}

	err := d.concurrentFetch((*receiptQueue)(d), false)
	if err != ErrPeerBackedOff {
		t.Fatalf("expected ErrPeerBackedOff, got %v", err)
	}
	if peer.receiptRequested.Load() {
		t.Fatal("backed-off peer should not receive a receipt request")
	}
}

func TestConcurrentFetchGradesDepartedStalePeer(t *testing.T) {
	d := newTestDownloader()
	d.peers.rates.OverrideTTLLimit = 50 * time.Millisecond
	scheduleReceiptTask(d)

	peer := &mockPeer{id: "peer-eth69", protocol: eth.ETH69}
	pc := newPeerConnection(peer.id, peer.protocol, peer, log.New("peer", peer.id))
	if err := d.peers.Register(pc); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- d.concurrentFetch((*receiptQueue)(d), false)
	}()

	waitFor := func(cond func() bool) bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return cond()
	}

	if !waitFor(func() bool { return peer.receiptRequested.Load() }) {
		close(d.cancelCh)
		<-done
		t.Fatal("receipt request was never dispatched")
	}

	time.Sleep(200 * time.Millisecond)
	if err := d.peers.Unregister(pc.id); err != nil {
		t.Fatalf("failed to unregister peer: %v", err)
	}

	if !waitFor(func() bool { return d.peers.persistentBackoff(peer.id) > 0 }) {
		close(d.cancelCh)
		<-done
		t.Fatal("a peer that left with a stale request should retain its timeout backoff")
	}

	close(d.cancelCh)
	<-done
	if got := softStrikeTally(d.peers, peer.id); got != 0 {
		t.Fatalf("a stale timeout should not record soft strikes, have %d", got)
	}
}

func TestConcurrentFetchMasterTimeoutAborts(t *testing.T) {
	d := newTestDownloader()
	d.peers.rates.OverrideTTLLimit = 50 * time.Millisecond
	scheduleReceiptTask(d)

	peer := &mockPeer{id: "master-eth69", protocol: eth.ETH69}
	pc := newPeerConnection(peer.id, peer.protocol, peer, log.New("peer", peer.id))
	if err := d.peers.Register(pc); err != nil {
		t.Fatal(err)
	}
	d.cancelPeer = peer.id

	if err := d.concurrentFetch((*receiptQueue)(d), false); err != errTimeout {
		t.Fatalf("expected errTimeout when the master peer times out, got %v", err)
	}
	select {
	case <-d.cancelCh:
	default:
		t.Fatal("a master timeout must cancel the sync cycle")
	}
}

type operationalWitnessPeer struct {
	*mockPeer
	fetch func([]common.Hash, chan *eth.Response) (*eth.Request, error)
}

func (p *operationalWitnessPeer) SupportsWitness() bool { return true }

func (p *operationalWitnessPeer) RequestWitnesses(hashes []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
	return p.fetch(hashes, sink)
}

func registerOperationalWitnessPeer(t *testing.T, d *Downloader, id string, fetch func([]common.Hash, chan *eth.Response) (*eth.Request, error)) *peerConnection {
	t.Helper()
	p := newPeerConnection(id, eth.ETH69, &operationalWitnessPeer{mockPeer: &mockPeer{id: id}, fetch: fetch}, log.New())
	if err := d.peers.Register(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConcurrentFetchWitnessRecovery(t *testing.T) {
	for _, name := range []string{"empty", "send failure", "timeout", "master timeout", "late response", "alternate source", "cancel", "persistent unavailability", "persistent send failure", "persistent timeout", "persistent master timeout"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newTestDownloader()
				d.peers.rates.OverrideTTLLimit = 50 * time.Millisecond
				d.queue.Prepare(1, StatelessSync)
				first := &types.Header{Number: big.NewInt(1)}
				second := &types.Header{Number: big.NewInt(2), ParentHash: first.Hash()}
				hashes := []common.Hash{first.Hash(), second.Hash()}
				d.queue.Schedule([]*types.Header{first, second}, hashes, 1)
				d.queue.witnessWakeCh <- false
				var drops, calls int
				var attempts []time.Time
				var requested []common.Hash
				var stale *eth.Request
				d.dropPeer = func(string) { drops++ }
				server := registerOperationalWitnessPeer(t, d, "server", func(hashes []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
					calls++
					attempts = append(attempts, time.Now())
					requested = append(requested, hashes...)
					req := &eth.Request{Peer: "server", Sent: time.Now(), Cancel: make(chan struct{})}
					persistent := name == "persistent unavailability" || name == "persistent send failure" || name == "persistent timeout" || name == "persistent master timeout"
					failed := calls <= 4 || persistent
					if name == "late response" {
						if calls == 1 {
							stale = req
							return req, nil
						}
						failed = false
					}
					if failed {
						switch name {
						case "send failure", "persistent send failure":
							return nil, errors.New("send failed")
						case "timeout", "master timeout", "persistent timeout", "persistent master timeout":
							return req, nil
						}
					}
					var witnesses []*stateless.Witness
					if !failed {
						witnesses = []*stateless.Witness{{}}
					}
					late := stale
					stale = nil
					go func() {
						if late != nil {
							sink <- &eth.Response{Req: late, Res: []*stateless.Witness{}, Done: make(chan error, 1)}
						}
						select {
						case sink <- &eth.Response{Req: req, Res: witnesses, Done: make(chan error, 1)}:
						case <-req.Cancel:
						}
					}()
					return req, nil
				})
				if name == "master timeout" || name == "persistent master timeout" {
					d.cancelPeer = server.id
				}
				done := make(chan error, 1)
				finished := false
				defer func() {
					if !finished {
						select {
						case <-d.cancelCh:
						default:
							close(d.cancelCh)
						}
						<-done
					}
				}()
				start := time.Now()
				go func() { done <- d.concurrentFetch((*witnessQueue)(d), false) }()
				canceled := name == "cancel"
				unavailable := name == "persistent unavailability" || name == "persistent send failure" || name == "persistent timeout" || name == "persistent master timeout"
				if name == "alternate source" || canceled {
					synctest.Wait()
					if calls != 1 || d.queue.PendingWitnesses() != 2 {
						t.Fatalf("empty response did not requeue work: calls=%d pending=%d", calls, d.queue.PendingWitnesses())
					}
					if canceled {
						close(d.cancelCh)
					} else {
						registerOperationalWitnessPeer(t, d, "healthy", func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
							req := &eth.Request{Peer: "healthy", Cancel: make(chan struct{})}
							go func() {
								select {
								case sink <- &eth.Response{Req: req, Res: []*stateless.Witness{{}}, Done: make(chan error, 1)}:
								case <-req.Cancel:
								}
							}()
							return req, nil
						})
					}
				}
				select {
				case err := <-done:
					finished = true
					switch {
					case canceled && err != errCanceled,
						unavailable && !errors.Is(err, ErrWitnessUnavailable),
						!canceled && !unavailable && err != nil:
						t.Fatalf("fetch stopped: %v", err)
					}
				case <-time.After(5 * time.Minute):
					t.Fatal("witness fetch did not recover")
				}
				if drops != 0 || server.backedOff() || d.peers.persistentBackoff(server.id) > 0 || softStrikeTally(d.peers, server.id) != 0 {
					t.Fatal("operational failure penalized the peer")
				}
				for _, hash := range hashes {
					if server.Lacks(hash) {
						t.Fatal("empty witness response marked the peer lacking")
					}
				}
				switch name {
				case "persistent unavailability", "persistent send failure", "persistent timeout", "persistent master timeout":
					if elapsed := time.Since(start); elapsed < witnessNoProgressBudget || elapsed > witnessNoProgressBudget+2*d.peers.rates.TargetTimeout() {
						t.Fatalf("unavailable witnesses were not paced or bounded: calls=%d elapsed=%v", calls, elapsed)
					}
				case "alternate source", "cancel":
					if calls != 1 || time.Since(start) >= time.Second {
						t.Fatal("peer events or cancellation mishandled the witness retry deadline")
					}
				default:
					failures := 4
					if name == "late response" {
						failures = 1
					}
					if calls != failures+2 {
						t.Fatalf("got %d requests, want %d", calls, failures+2)
					}
					for i := 1; i <= failures; i++ {
						if attempts[i].Sub(attempts[i-1]) < time.Second || requested[i] != hashes[0] {
							t.Fatal("witness retry ran early or abandoned the requested hash")
						}
					}
				}
				if !canceled && !unavailable && len(d.queue.witnessTaskPool) != 0 {
					t.Fatal("fetch finished without delivering every witness")
				}
			})
		})
	}
}

func scheduleWitnessTasks(d *Downloader, count int) {
	var (
		headers []*types.Header
		hashes  []common.Hash
		parent  common.Hash
	)
	for i := 1; i <= count; i++ {
		header := &types.Header{Number: big.NewInt(int64(i)), ParentHash: parent}
		parent = header.Hash()
		headers = append(headers, header)
		hashes = append(hashes, parent)
	}
	d.queue.Schedule(headers, hashes, 1)
}

func runWitnessFetch(t *testing.T, d *Downloader, probe func(wait func() error)) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- d.concurrentFetch((*witnessQueue)(d), false) }()
	defer func() {
		select {
		case <-d.cancelCh:
		default:
			close(d.cancelCh)
		}
		<-done
	}()

	probe(func() error {
		select {
		case err := <-done:
			done <- err
			return err
		case <-time.After(5 * time.Minute):
			t.Fatal("witness fetch never finished")
			return nil
		}
	})
}

func TestConcurrentFetchWitnessBudget(t *testing.T) {
	t.Run("ongoing progress", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newTestDownloader()
			d.queue.Prepare(1, StatelessSync)
			scheduleWitnessTasks(d, 3)
			d.queue.witnessWakeCh <- false
			var calls int
			server := registerOperationalWitnessPeer(t, d, "server", func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
				calls++
				req := &eth.Request{Peer: "server", Cancel: make(chan struct{})}
				var witnesses []*stateless.Witness
				if calls%20 == 0 {
					witnesses = []*stateless.Witness{{}}
				}
				go func() {
					select {
					case sink <- &eth.Response{Req: req, Res: witnesses, Done: make(chan error, 1)}:
					case <-req.Cancel:
					}
				}()
				return req, nil
			})
			start := time.Now()
			runWitnessFetch(t, d, func(wait func() error) {
				if err := wait(); err != nil {
					t.Fatalf("a cycle still accepting witnesses ended: %v", err)
				}
				if elapsed := time.Since(start); elapsed <= witnessNoProgressBudget || calls != 60 {
					t.Fatalf("accepted witnesses did not extend the budget: calls=%d elapsed=%v", calls, elapsed)
				}
				if d.peers.persistentBackoff(server.id) > 0 || softStrikeTally(d.peers, server.id) != 0 {
					t.Fatal("paced retries penalized the peer")
				}
			})
		})
	})

	t.Run("starved headers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newTestDownloader()
			d.queue.Prepare(1, StatelessSync)
			var calls int
			registerOperationalWitnessPeer(t, d, "server", func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
				calls++
				req := &eth.Request{Peer: "server", Cancel: make(chan struct{})}
				go func() {
					select {
					case sink <- &eth.Response{Req: req, Res: []*stateless.Witness{{}}, Done: make(chan error, 1)}:
					case <-req.Cancel:
					}
				}()
				return req, nil
			})
			runWitnessFetch(t, d, func(wait func() error) {
				time.Sleep(3 * witnessNoProgressBudget)
				synctest.Wait()
				if calls != 0 {
					t.Fatalf("witnesses were requested without scheduled work: calls=%d", calls)
				}
				scheduleWitnessTasks(d, 2)
				d.queue.witnessWakeCh <- false
				if err := wait(); err != nil {
					t.Fatalf("a cycle waiting for headers ended: %v", err)
				}
				if calls != 2 {
					t.Fatalf("got %d requests, want 2", calls)
				}
			})
		})
	})

	t.Run("throttled consumer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newTestDownloader()
			d.queue.Prepare(1, StatelessSync)
			d.queue.resultCache.SetThrottleThreshold(1)
			scheduleWitnessTasks(d, 2)
			d.queue.witnessWakeCh <- false
			var calls int
			registerOperationalWitnessPeer(t, d, "server", func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
				calls++
				req := &eth.Request{Peer: "server", Cancel: make(chan struct{})}
				go func() {
					select {
					case sink <- &eth.Response{Req: req, Res: []*stateless.Witness{{}}, Done: make(chan error, 1)}:
					case <-req.Cancel:
					}
				}()
				return req, nil
			})
			runWitnessFetch(t, d, func(wait func() error) {
				time.Sleep(3 * witnessNoProgressBudget)
				synctest.Wait()
				if calls != 1 || d.queue.PendingWitnesses() != 1 {
					t.Fatalf("throttled queue kept fetching: calls=%d pending=%d", calls, d.queue.PendingWitnesses())
				}
				d.queue.resultCache.SetThrottleThreshold(2)
				d.queue.witnessWakeCh <- true
				if err := wait(); err != nil {
					t.Fatalf("a cycle throttled by the importer ended: %v", err)
				}
				if calls != 2 {
					t.Fatalf("got %d requests, want 2", calls)
				}
			})
		})
	})
}

func TestConcurrentFetchReceiptsOutlastWitnessBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newTestDownloader()
		d.peers.rates.OverrideTTLLimit = 50 * time.Millisecond
		scheduleReceiptTask(d)

		peer := &mockPeer{id: "peer-eth69", protocol: eth.ETH69}
		if err := d.peers.Register(newPeerConnection(peer.id, peer.protocol, peer, log.New())); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		err := d.concurrentFetch((*receiptQueue)(d), false)
		if elapsed := time.Since(start); err != ErrPeerBackedOff || elapsed <= witnessNoProgressBudget {
			t.Fatalf("receipt stalling changed: err=%v elapsed=%v", err, elapsed)
		}
	})
}

func TestDeliverWitnessFailureLabels(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		d := newTestDownloader()
		d.queue.Prepare(1, StatelessSync)
		header := &types.Header{Number: big.NewInt(1)}
		d.queue.Schedule([]*types.Header{header}, []common.Hash{header.Hash()}, 1)
		peer := registerOperationalWitnessPeer(t, d, "server", nil)
		if req, _, _ := d.queue.ReserveWitnesses(peer, 1); req == nil {
			t.Fatal("missing reservation")
		}
		var cause error
		if invalid {
			cause = errors.Join(stateless.ErrInvalidWitness, errors.New("invalid encoding"))
		}
		if n, err := d.queue.DeliverWitnesses(peer.id, nil, cause); n != 0 || err != nil {
			t.Fatalf("delivery: %d, %v", n, err)
		}
		if peer.Lacks(header.Hash()) != invalid || d.queue.PendingWitnesses() != 1 {
			t.Fatalf("wrong treatment for invalid=%v", invalid)
		}
	}
}

func TestConcurrentFetchWitnessStaggeredTimeoutBudget(t *testing.T) {
	for _, tc := range []struct {
		ttl        time.Duration
		throttled  bool
		disconnect bool
	}{
		{10 * time.Second, false, false},
		{10 * time.Second, true, false},
		{60 * time.Second, false, false},
		{60 * time.Second, true, false},
		{60 * time.Second, false, true},
	} {
		t.Run(fmt.Sprintf("ttl=%s/throttled=%t/disconnect=%t", tc.ttl, tc.throttled, tc.disconnect), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newTestDownloader()
				d.peers.rates.OverrideTTLLimit = tc.ttl
				d.queue.Prepare(1, StatelessSync)
				first := &types.Header{Number: big.NewInt(1)}
				second := &types.Header{Number: big.NewInt(2), ParentHash: first.Hash()}
				hashes := []common.Hash{first.Hash(), second.Hash()}
				d.queue.Schedule([]*types.Header{first, second}, hashes, 1)
				if tc.throttled {
					d.queue.resultCache.SetThrottleThreshold(2)
					third := &types.Header{Number: big.NewInt(3), ParentHash: second.Hash()}
					d.queue.Schedule([]*types.Header{third}, []common.Hash{third.Hash()}, 3)
				}
				d.queue.witnessWakeCh <- false
				var peers []*peerConnection
				var requests []*eth.Request
				register := func(id string) {
					peers = append(peers, registerOperationalWitnessPeer(t, d, id, func(_ []common.Hash, _ chan *eth.Response) (*eth.Request, error) {
						req := &eth.Request{Peer: id, Sent: time.Now(), Cancel: make(chan struct{})}
						requests = append(requests, req)
						return req, nil
					}))
				}
				register("first")
				ttl := d.peers.rates.TargetTimeout()
				start := time.Now()
				done := make(chan error, 1)
				go func() { done <- d.concurrentFetch((*witnessQueue)(d), false) }()
				finished := false
				defer func() {
					if !finished {
						close(d.cancelCh)
						<-done
					}
				}()
				time.Sleep(ttl / 2)
				register("second")
				firstFailure := ttl
				if tc.disconnect {
					synctest.Wait()
					if err := d.peers.Unregister("first"); err != nil {
						t.Fatal(err)
					}
					register("replacement")
					firstFailure = ttl / 2
				}
				if tc.throttled {
					register("idle")
				}
				select {
				case err := <-done:
					finished = true
					if !errors.Is(err, ErrWitnessUnavailable) {
						t.Fatalf("wrong cycle failure: %v", err)
					}
				case <-time.After(witnessNoProgressBudget + 3*ttl):
					t.Fatal("staggered in-flight requests kept the witness cycle alive beyond its budget")
				}
				if elapsed := time.Since(start); elapsed > firstFailure+witnessNoProgressBudget+ttl {
					t.Fatalf("cycle exceeded its failure budget: %s", elapsed)
				}
				for _, peer := range peers {
					if peer.backedOff() || d.peers.persistentBackoff(peer.id) > 0 || softStrikeTally(d.peers, peer.id) != 0 {
						t.Fatal("cycle budget penalized a peer")
					}
					for _, hash := range hashes {
						if peer.Lacks(hash) {
							t.Fatal("cycle budget marked a witness source lacking")
						}
					}
				}
				for _, req := range requests {
					select {
					case <-req.Cancel:
					default:
						t.Fatal("cycle left a witness request active")
					}
				}
			})
		})
	}
}

func TestConcurrentFetchWitnessTimeoutRecovery(t *testing.T) {
	for _, ttl := range []time.Duration{20 * time.Second, 30 * time.Second, 60 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newTestDownloader()
				d.peers.rates.OverrideTTLLimit = ttl
				d.queue.Prepare(1, StatelessSync)
				scheduleWitnessTasks(d, 1)
				d.queue.witnessWakeCh <- false
				var slowCalls, healthyCalls, drops int
				d.dropPeer = func(string) { drops++ }
				slow := registerOperationalWitnessPeer(t, d, "slow", func(_ []common.Hash, _ chan *eth.Response) (*eth.Request, error) {
					slowCalls++
					return &eth.Request{Peer: "slow", Sent: time.Now(), Cancel: make(chan struct{})}, nil
				})
				if actual := d.peers.rates.TargetTimeout(); actual != ttl {
					t.Fatalf("request timeout = %s, want %s", actual, ttl)
				}
				start := time.Now()
				runWitnessFetch(t, d, func(wait func() error) {
					synctest.Wait()
					if slowCalls != 1 {
						t.Fatalf("first peer calls = %d, want 1", slowCalls)
					}
					registerOperationalWitnessPeer(t, d, "healthy", func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
						healthyCalls++
						req := &eth.Request{Peer: "healthy", Sent: time.Now(), Cancel: make(chan struct{})}
						go func() {
							select {
							case sink <- &eth.Response{Req: req, Res: []*stateless.Witness{{}}, Done: make(chan error, 1)}:
							case <-req.Cancel:
							}
						}()
						return req, nil
					})
					err := wait()
					t.Logf("elapsed=%s slow requests=%d healthy requests=%d result=%v", time.Since(start), slowCalls, healthyCalls, err)
					if err != nil || healthyCalls == 0 {
						t.Fatalf("healthy peer was not used: err=%v slow=%d healthy=%d", err, slowCalls, healthyCalls)
					}
					if drops != 0 || slow.backedOff() || d.peers.persistentBackoff(slow.id) > 0 || softStrikeTally(d.peers, slow.id) != 0 || len(slow.lacking) != 0 {
						t.Fatal("timeout recovery penalized the original source")
					}
				})
			})
		})
	}
}

func TestConcurrentFetchWitnessWaitsForSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newTestDownloader()
		d.queue.Prepare(1, StatelessSync)
		scheduleWitnessTasks(d, 1)
		d.queue.witnessWakeCh <- false
		start := time.Now()
		err := d.concurrentFetch((*witnessQueue)(d), true)
		if !errors.Is(err, ErrWitnessUnavailable) || time.Since(start) != witnessNoProgressBudget {
			t.Fatalf("queued witnesses did not wait for a source: err=%v elapsed=%s", err, time.Since(start))
		}
	})
}
