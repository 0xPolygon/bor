package eth

import (
	"bytes"
	"errors"
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

type observedWriter struct {
	err       error
	completed bool
}

func (w *observedWriter) ReadMsg() (p2p.Msg, error) { return p2p.Msg{}, errors.New("not used") }
func (w *observedWriter) WriteMsg(msg p2p.Msg) error {
	if w.err != nil {
		return w.err
	}
	if err := msg.Discard(); err != nil {
		return err
	}
	w.completed = true
	return nil
}

func TestPeerPolicyCompletedBodyReply(t *testing.T) {
	for _, failure := range []bool{false, true} {
		w := &observedWriter{}
		if failure {
			w.err = errors.New("write failed")
		}
		calls := 0
		p := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: w, observer: func(e peerpolicy.Evidence) {
			calls++
			if !w.completed || e.Family != peerpolicy.BodyReplies {
				t.Fatal("recorded before completion")
			}
			if e.Activity.ServedResponses != 1 || e.Activity.ServedBytes != 4 {
				t.Fatalf("completed body omitted encoded reply activity: %+v", e)
			}
		}}
		err := p.writeObservedBodies(1, []rlp.RawValue{{0xc0}}, peerpolicy.Evidence{Family: peerpolicy.BodyReplies})
		if !errors.Is(err, w.err) {
			t.Fatalf("changed write error: %v", err)
		}
		wantCalls := 1
		if failure {
			wantCalls = 0
		}
		if calls != wantCalls {
			t.Fatalf("calls=%d want=%d", calls, wantCalls)
		}
	}
}

func TestPeerPolicyBodyManifestSkipsMissingObjects(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	hash := backend.chain.GetBlockByNumber(1).Hash()
	query := GetBlockBodiesRequest{hash, {}}
	var recorded []common.Hash
	var bytes int
	response := serviceGetBlockBodiesQuery(backend.chain, query, func(hash common.Hash, size int) {
		recorded = append(recorded, hash)
		bytes += size
	})
	if len(recorded) != 1 || recorded[0] != hash || len(response) != 1 || bytes != len(response[0]) {
		t.Fatalf("manifest did not match response: %v", recorded)
	}
}

func TestPeerPolicyBodyServingAdapter(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	hash := backend.chain.GetBlockByNumber(1).Hash()
	for _, enabled := range []bool{false, true} {
		w := &observedWriter{}
		p := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: w}
		var got peerpolicy.Evidence
		if enabled {
			p.SetObserver(func(e peerpolicy.Evidence) { got = e })
		}
		query := GetBlockBodiesPacket{RequestId: 7, GetBlockBodiesRequest: GetBlockBodiesRequest{hash, {}}}
		if err := observedBodyReply(backend, p, query); err != nil {
			t.Fatal(err)
		}
		if !w.completed {
			t.Fatal("reply was not written")
		}
		if enabled && (got.Items != 1 || got.Bytes == 0 || len(got.Hashes) != 1 || got.Hashes[0] != hash || got.ObjectBytes[0] != got.Bytes) {
			t.Fatalf("incorrect completed reply observation: %+v", got)
		}
	}
}

func TestPeerPolicyQueueRefusals(t *testing.T) {
	for _, kind := range []string{"reservation", "generic", "body", "closed"} {
		t.Run(kind, func(t *testing.T) {
			p := &Peer{Peer: p2p.NewPeer(enode.ID{1}, "", nil), term: make(chan struct{}), txReplies: newPeerReplies(), blockReplies: newPeerReplies()}
			p.txReplies.bytes = peerByteBurst / 2
			p.blockReplies.bytes = peerByteBurst / 2
			calls := 0
			p.SetObserver(func(e peerpolicy.Evidence) {
				calls++
				if e.Activity != (peerpolicy.ActivityEvent{}) {
					t.Fatal("refused reply counted completed activity")
				}
				if !e.Refused {
					t.Fatal("missing refusal flag")
				}
				if kind == "body" && (len(e.Hashes) != 1 || e.Hashes[0] != (common.Hash{1})) {
					t.Fatal("missing body provenance")
				}
				// The callback must run after releasing the queue mutex.
				if !p.txReplies.mu.TryLock() {
					t.Fatal("transaction queue locked during observation")
				}
				p.txReplies.mu.Unlock()
				if !p.blockReplies.mu.TryLock() {
					t.Fatal("block queue locked during observation")
				}
				p.blockReplies.mu.Unlock()
			})
			var err error
			switch kind {
			case "reservation":
				_, err = p.reservePooledReply()
			case "generic":
				err = p.queueReply(BlockBodiesMsg, []rlp.RawValue{{0xc0}})
			case "body":
				err = p.writeObservedBodies(1, []rlp.RawValue{{0xc0}}, peerpolicy.Evidence{Hashes: []common.Hash{{1}}})
			case "closed":
				close(p.term)
				err = p.queueReply(BlockBodiesMsg, []rlp.RawValue{{0xc0}})
			}
			if kind == "closed" {
				if !errors.Is(err, ErrDisconnected) || calls != 0 {
					t.Fatalf("disconnect treated as refusal: %v %d", err, calls)
				}
			} else if !errors.Is(err, errReplyQueueFull) || calls != 1 {
				t.Fatalf("refusal: %v calls=%d", err, calls)
			}
		})
	}
}

func TestPeerPolicyPermittedBodyRequestsStayNeutral(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	synctest.Test(t, func(t *testing.T) {
		peer, rw := newObservedReplyTestPeer(t)
		clock := new(mclock.Simulated)
		tracker := peerpolicy.New(clock)
		tracker.Connected(peer.ID())
		peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe(peer.ID(), e) })
		query := GetBlockBodiesPacket{RequestId: 1, GetBlockBodiesRequest: make(GetBlockBodiesRequest, maxBodiesServe)}
		for i := range query.GetBlockBodiesRequest {
			query.GetBlockBodiesRequest[i] = backend.chain.GetBlockByNumber(1).Hash()
		}
		encoded, err := rlp.EncodeToBytes(query)
		require.NoError(t, err)
		requests := 0
		request := func() error {
			if requests >= peerRequestBurst {
				time.Sleep(time.Second / peerRequestRate)
				clock.Run(time.Second / peerRequestRate)
			}
			requests++
			rw.msg = p2p.Msg{Code: GetBlockBodiesMsg, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
			return handleMessage(backend, peer)
		}
		const successfulRequests = 512
		require.Greater(t, successfulRequests*len(encoded), 16<<20)
		for range successfulRequests {
			require.NoError(t, request())
			synctest.Wait()
			require.Len(t, rw.sent, 1)
			msg := <-rw.sent
			require.NoError(t, msg.Discard())
		}
		got := tracker.Snapshot(peer.ID())
		require.Zero(t, got.Risk, "successful repeated requests passed native admission")
		require.Empty(t, got.Windows)
		require.Equal(t, uint64(successfulRequests), got.Contribution.ServedResponses)
		require.Positive(t, got.Contribution.ServedBytes)
		peer.blockReplies.mu.Lock()
		peer.blockReplies.bytes = peerByteBurst / 2
		peer.blockReplies.mu.Unlock()
		for range 3 {
			require.ErrorIs(t, request(), errReplyQueueFull)
		}
		got = tracker.Snapshot(peer.ID())
		require.Equal(t, uint64(20), got.Risk)
		require.Equal(t, uint64(1), got.Windows[peerpolicy.ServingRepetition.String()])
		require.Equal(t, uint64(successfulRequests), got.Contribution.ServedResponses)
	})
}

func TestPeerPolicyNativeBodyByteAllowanceStaysNeutral(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	tracker.Connected("peer")
	peer := &Peer{observer: func(e peerpolicy.Evidence) { tracker.Observe("peer", e) }}
	replies := newPeerReplies()
	start := time.Now()
	const responseBytes = 8 << 20
	completed := peer.replyObserver(peerpolicy.Evidence{Family: peerpolicy.BodyReplies, Items: 1, Bytes: responseBytes})
	for second := range 10 {
		budget := peerByteRate
		if second == 0 {
			budget = peerByteBurst
		} else {
			clock.Run(time.Second)
		}
		for range budget / responseBytes {
			require.True(t, replies.limiter.AllowN(start.Add(time.Duration(second)*time.Second), responseBytes))
			completed(responseBytes)
		}
	}
	got := tracker.Snapshot("peer")
	require.Zero(t, got.Risk)
	require.Empty(t, got.Windows)
	require.Equal(t, uint64(26), got.Contribution.ServedResponses)
	require.Equal(t, uint64(208<<20), got.Contribution.ServedBytes)
}

func TestPeerPolicyNativeRequestBurstStaysNeutral(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	peer := limitedTestPeer()
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe("peer", e) })
	const window = 10 * time.Second
	const capacity = peerRequestBurst + peerRequestRate*int(window/time.Second)
	now := time.Now()
	const refill = time.Second / time.Duration(peerRequestRate)
	codes := []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg}
	for i := range capacity - 1 {
		if i >= peerRequestBurst {
			now = now.Add(refill)
			clock.Run(refill)
		}
		require.NoError(t, peer.checkMessageRate(codes[i%len(codes)], 1, now))
		tracker.Observe("peer", peerpolicy.Evidence{Family: peerpolicy.Requests, Items: 1, Bytes: 1})
	}
	require.Less(t, time.Duration(clock.Now()), window)
	require.Zero(t, tracker.Snapshot("peer").Risk, "permitted requests fit within one aligned window")
	tracker.Observe("profile", peerpolicy.Evidence{Family: peerpolicy.Requests, Items: uint64(capacity)})
	require.Zero(t, tracker.Snapshot("profile").Risk, "profile must cover native rate plus burst")
	require.ErrorIs(t, peer.checkMessageRate(GetBlockBodiesMsg, 1, now), ErrPeerRateLimit)
	peer.observeRateLimit(p2p.Msg{Code: GetBlockBodiesMsg, Size: 1})
	got := tracker.Snapshot("peer")
	require.Equal(t, uint64(20), got.Risk)
	require.Equal(t, uint64(1), got.Windows[peerpolicy.RateLimited.String()])
	require.Zero(t, got.Windows[peerpolicy.RequestVolume.String()])
}
