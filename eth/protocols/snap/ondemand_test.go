// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package snap

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

// serveCodesFromCorpus returns a codeRequestHandler that answers with the
// requested blobs it has in corpus, in request order, omitting any it lacks —
// the well-behaving snap-server behaviour.
func serveCodesFromCorpus(corpus map[common.Hash][]byte) codeHandlerFunc {
	return func(tp *testPeer, id uint64, hashes []common.Hash, _ uint64) error {
		var out [][]byte
		for _, h := range hashes {
			if code, ok := corpus[h]; ok {
				out = append(out, code)
			}
		}
		return tp.remote.OnByteCodes(tp, id, out)
	}
}

// neverAnswer is a codeRequestHandler for a peer that accepts a request and
// never replies, so a fetch against it ends only via timeout or cancellation.
func neverAnswer(*testPeer, uint64, []common.Hash, uint64) error { return nil }

// newCodePeer registers a testPeer on syncer with the given bytecode handler.
func newCodePeer(t *testing.T, syncer *Syncer, id string, handler codeHandlerFunc) *testPeer {
	t.Helper()
	peer := newTestPeer(id, t, func() {})
	peer.remote = syncer
	peer.codeRequestHandler = handler
	if err := syncer.Register(peer); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	return peer
}

// assertNoInflightCodeReqs checks that a returned FetchByteCodes left no
// targeted request registered, whichever path it returned through.
func assertNoInflightCodeReqs(t *testing.T, s *Syncer) {
	t.Helper()
	s.lock.RLock()
	n := len(s.onDemandCodeReqs)
	s.lock.RUnlock()
	if n != 0 {
		t.Fatalf("%d on-demand code request(s) left in flight after the fetch returned", n)
	}
}

func TestFetchByteCodesOnDemand(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x00, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)
	unknown := crypto.Keccak256Hash([]byte("no peer has this"))

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	peer := newCodePeer(t, syncer, "server", serveCodesFromCorpus(map[common.Hash][]byte{hashA: codeA, hashB: codeB}))

	// Both present → both returned and byte-exact, in a single request.
	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hashA, hashB})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) || !bytes.Equal(got[hashB], codeB) {
		t.Fatalf("wrong codes: got[A]=%x got[B]=%x", got[hashA], got[hashB])
	}
	if peer.nBytecodeRequests != 1 {
		t.Fatalf("expected a single request for two servable hashes, got %d", peer.nBytecodeRequests)
	}
	assertNoInflightCodeReqs(t, syncer)

	// A hash no peer serves must not appear in the result (and must not be
	// fabricated); with no verified codes at all, an error is returned.
	got, err = syncer.FetchByteCodes(context.Background(), []common.Hash{unknown})
	if err == nil {
		t.Fatal("expected an error when no connected peer serves the requested code")
	}
	if _, ok := got[unknown]; ok {
		t.Fatalf("unknown hash was served a value: %x", got[unknown])
	}
	assertNoInflightCodeReqs(t, syncer)

	// A partial result — one hash served, one unservable — returns what it has
	// without an error (the served subset is still worth persisting).
	got, err = syncer.FetchByteCodes(context.Background(), []common.Hash{hashA, unknown})
	if err != nil {
		t.Fatalf("partial fetch should not error: %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) {
		t.Fatalf("partial fetch dropped the served code: got %x", got[hashA])
	}
	if _, ok := got[unknown]; ok {
		t.Fatal("unservable hash present in a partial result")
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesEmpty covers the trivial case: with no hashes requested the
// call returns a non-nil empty map and no error, touching no peer.
func TestFetchByteCodesEmpty(t *testing.T) {
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	peer := newCodePeer(t, syncer, "server", serveCodesFromCorpus(nil))

	got, err := syncer.FetchByteCodes(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty request should not error: %v", err)
	}
	if got == nil {
		t.Fatal("empty request should return a non-nil (empty) map")
	}
	if len(got) != 0 {
		t.Fatalf("empty request should return no codes, got %d", len(got))
	}
	if peer.nBytecodeRequests != 0 {
		t.Fatalf("empty request must not touch a peer, got %d request(s)", peer.nBytecodeRequests)
	}
}

// TestFetchByteCodesNoPeers covers the idle-node case: with nobody connected the
// fetch fails cleanly rather than blocking or panicking.
func TestFetchByteCodesNoPeers(t *testing.T) {
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{{0x01}})
	if err == nil {
		t.Fatal("expected an error with no connected peers")
	}
	if len(got) != 0 {
		t.Fatalf("expected no codes with no connected peers, got %d", len(got))
	}
}

// TestPendingByteCodes covers the outstanding-set reduction and the per-request
// cap directly.
func TestPendingByteCodes(t *testing.T) {
	h := func(n byte) common.Hash { return common.Hash{n} }

	// Empty out: everything is still pending.
	if got := pendingByteCodes([]common.Hash{h(1), h(2)}, map[common.Hash][]byte{}); len(got) != 2 {
		t.Fatalf("empty out: want 2 pending, got %d", len(got))
	}
	// Some already present: only the missing ones remain.
	got := pendingByteCodes([]common.Hash{h(1), h(2)}, map[common.Hash][]byte{h(1): {0x1}})
	if len(got) != 1 || got[0] != h(2) {
		t.Fatalf("partial out: want [h2], got %v", got)
	}
	// All present: nothing pending.
	if got := pendingByteCodes([]common.Hash{h(1)}, map[common.Hash][]byte{h(1): {0x1}}); len(got) != 0 {
		t.Fatalf("all present: want 0 pending, got %d", len(got))
	}
	// More than a request's worth: capped at maxCodeRequestCount.
	many := make([]common.Hash, maxCodeRequestCount+3)
	for i := range many {
		many[i] = common.Hash{byte(i), byte(i >> 8)}
	}
	if got := pendingByteCodes(many, map[common.Hash][]byte{}); len(got) != maxCodeRequestCount {
		t.Fatalf("over cap: want %d pending, got %d", maxCodeRequestCount, len(got))
	}
}

// TestPartialOrErr covers the "a partial result suppresses the error" rule.
func TestPartialOrErr(t *testing.T) {
	boom := errors.New("boom")
	if err := partialOrErr(map[common.Hash][]byte{}, boom); !errors.Is(err, boom) {
		t.Fatalf("empty result must surface the error, got %v", err)
	}
	if err := partialOrErr(map[common.Hash][]byte{{0x01}: {0x01}}, boom); err != nil {
		t.Fatalf("partial result must suppress the error, got %v", err)
	}
}

// TestVerifyAndCollectByteCodes covers the content-addressed filter directly:
// only a blob whose keccak is a wanted hash is recorded, under that hash.
func TestVerifyAndCollectByteCodes(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)
	garbage := []byte{0xde, 0xad, 0xbe, 0xef}

	// Wanted A: garbage and an unrequested-but-valid B are ignored, A is kept.
	out := map[common.Hash][]byte{}
	verifyAndCollectByteCodes([]common.Hash{hashA}, [][]byte{garbage, codeB, codeA}, out)
	if len(out) != 1 || !bytes.Equal(out[hashA], codeA) {
		t.Fatalf("want exactly {A: codeA}, got %v", out)
	}
	if _, ok := out[hashB]; ok {
		t.Fatal("unrequested blob was collected")
	}
	if _, ok := out[crypto.Keccak256Hash(garbage)]; ok {
		t.Fatal("garbage blob was collected under its own hash")
	}

	// Nothing wanted matches: nothing recorded.
	out = map[common.Hash][]byte{}
	verifyAndCollectByteCodes([]common.Hash{hashA}, [][]byte{garbage, codeB}, out)
	if len(out) != 0 {
		t.Fatalf("expected no codes collected, got %v", out)
	}

	// Empty delivery: nothing recorded, no panic.
	verifyAndCollectByteCodes([]common.Hash{hashA}, nil, out)
	if len(out) != 0 {
		t.Fatalf("expected no codes from an empty delivery, got %v", out)
	}
}

// TestOnDemandByteCodesRouting covers response routing: an unrequested reqid is
// dropped without panicking, and a matched reqid is delivered once and forgotten.
func TestOnDemandByteCodesRouting(t *testing.T) {
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)

	// Unknown reqid: harmless no-op (must not panic dereferencing a nil req).
	if err := syncer.onDemandByteCodes(onDemandReqidBit|1, [][]byte{{0x01}}); err != nil {
		t.Fatalf("unknown reqid should be dropped: %v", err)
	}

	// Known reqid: delivered to the waiter and removed from the in-flight map.
	reqid, req := syncer.newOnDemandCodeReq()
	if reqid&onDemandReqidBit == 0 {
		t.Fatalf("on-demand reqid %x lacks the top bit", reqid)
	}
	if err := syncer.onDemandByteCodes(reqid, [][]byte{{0xaa}}); err != nil {
		t.Fatalf("known reqid delivery: %v", err)
	}
	select {
	case got := <-req.deliver:
		if len(got) != 1 || got[0][0] != 0xaa {
			t.Fatalf("wrong payload delivered: %v", got)
		}
	default:
		t.Fatal("expected a delivery on the matched reqid")
	}
	assertNoInflightCodeReqs(t, syncer)

	// A duplicate response for the same reqid is dropped (nothing to deliver to).
	if err := syncer.onDemandByteCodes(reqid, [][]byte{{0xbb}}); err != nil {
		t.Fatalf("duplicate response should be dropped: %v", err)
	}
	select {
	case got := <-req.deliver:
		t.Fatalf("duplicate response was delivered: %v", got)
	default:
	}

	// Delivery never blocks the network handler: with the waiter's buffer
	// already full, the response is dropped rather than stalling.
	reqid, req = syncer.newOnDemandCodeReq()
	req.deliver <- [][]byte{{0x01}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := syncer.onDemandByteCodes(reqid, [][]byte{{0xcc}}); err != nil {
			t.Errorf("delivery into a full buffer: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onDemandByteCodes blocked on a full delivery buffer")
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesAcrossPeers proves the fetch loops across peers to gather
// distinct hashes each peer can serve, reducing the pending set between rounds.
func TestFetchByteCodesAcrossPeers(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	newCodePeer(t, syncer, "pA", serveCodesFromCorpus(map[common.Hash][]byte{hashA: codeA}))
	newCodePeer(t, syncer, "pB", serveCodesFromCorpus(map[common.Hash][]byte{hashB: codeB}))

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hashA, hashB})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) || !bytes.Equal(got[hashB], codeB) {
		t.Fatalf("did not gather both codes across peers: A=%x B=%x", got[hashA], got[hashB])
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesStopsWhenComplete proves the fetch stops as soon as every
// hash is verified: with two peers both able to serve, exactly one request is
// made and no peer is ever sent an empty request.
func TestFetchByteCodesStopsWhenComplete(t *testing.T) {
	code := []byte{0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)
	corpus := map[common.Hash][]byte{hash: code}

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	strict := func(tp *testPeer, id uint64, hashes []common.Hash, max uint64) error {
		if len(hashes) == 0 {
			tp.test.Errorf("peer %s was sent an empty bytecode request", tp.id)
		}
		return serveCodesFromCorpus(corpus)(tp, id, hashes, max)
	}
	p1 := newCodePeer(t, syncer, "p1", strict)
	p2 := newCodePeer(t, syncer, "p2", strict)

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hash], code) {
		t.Fatalf("wrong code: got %x want %x", got[hash], code)
	}
	if n := p1.nBytecodeRequests + p2.nBytecodeRequests; n != 1 {
		t.Fatalf("expected exactly one request once the code was served, got %d", n)
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesSkipsStatelessPeers proves a peer flagged as unable to serve
// state is never asked, even when it is the only one connected.
func TestFetchByteCodesSkipsStatelessPeers(t *testing.T) {
	code := []byte{0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	flagged := newCodePeer(t, syncer, "flagged", serveCodesFromCorpus(map[common.Hash][]byte{hash: code}))
	// The flag set is allocated by Sync; an idle syncer has none yet.
	syncer.lock.Lock()
	if syncer.statelessPeers == nil {
		syncer.statelessPeers = make(map[string]struct{})
	}
	syncer.statelessPeers[flagged.id] = struct{}{}
	syncer.lock.Unlock()

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err == nil {
		t.Fatal("expected an error when the only peer is flagged stateless")
	}
	if _, ok := got[hash]; ok {
		t.Fatal("code was served by a peer flagged stateless")
	}
	if flagged.nBytecodeRequests != 0 {
		t.Fatalf("stateless-flagged peer was asked %d time(s)", flagged.nBytecodeRequests)
	}
}

// TestFetchByteCodesFailsOverAndVerifies proves the fetch rejects a peer that
// returns bytes not matching the requested hash (content-addressed check) and
// fails over to a peer that serves the correct blob.
func TestFetchByteCodesFailsOverAndVerifies(t *testing.T) {
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	newCodePeer(t, syncer, "liar", func(tp *testPeer, id uint64, hashes []common.Hash, _ uint64) error {
		// Serve the wrong bytes for every requested hash.
		out := make([][]byte, len(hashes))
		for i := range out {
			out[i] = []byte{0xde, 0xad, 0xbe, 0xef}
		}
		return tp.remote.OnByteCodes(tp, id, out)
	})
	newCodePeer(t, syncer, "honest", serveCodesFromCorpus(map[common.Hash][]byte{hash: code}))

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hash], code) {
		t.Fatalf("failover did not yield the verified code: got %x want %x", got[hash], code)
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesHonorsCancel proves a cancelled context aborts a fetch that
// is waiting on a peer, surfaces as the context error (not a "no peer" error),
// and leaves no request in flight.
func TestFetchByteCodesHonorsCancel(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte{0x60, 0x00, 0xf3})

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	silent := newCodePeer(t, syncer, "silent", neverAnswer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the fetch starts

	got, err := syncer.FetchByteCodes(ctx, []common.Hash{hash})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cancelled fetch returned codes: %v", got)
	}
	if silent.nBytecodeRequests != 1 {
		t.Fatalf("expected the single peer to be asked once, got %d", silent.nBytecodeRequests)
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesPartialSurvivesCancel proves that a cancellation arriving
// after some hashes were already verified returns that partial result without
// an error. Both peers behave identically so the outcome does not depend on
// which one is tried first: the first request (A and B) yields A, the second
// (only B) stalls until the test cancels the fetch.
func TestFetchByteCodesPartialSurvivesCancel(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stalled := make(chan struct{}, 2)
	serveAOrStall := func(tp *testPeer, id uint64, hashes []common.Hash, _ uint64) error {
		for _, h := range hashes {
			if h == hashA {
				return tp.remote.OnByteCodes(tp, id, [][]byte{codeA})
			}
		}
		stalled <- struct{}{}
		return nil
	}
	newCodePeer(t, syncer, "p1", serveAOrStall)
	newCodePeer(t, syncer, "p2", serveAOrStall)
	go func() {
		<-stalled
		cancel()
	}()

	got, err := syncer.FetchByteCodes(ctx, []common.Hash{hashA, hashB})
	if err != nil {
		t.Fatalf("a partial result must suppress the cancellation error, got %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) {
		t.Fatalf("partial result dropped the verified code: got %x", got[hashA])
	}
	if _, ok := got[hashB]; ok {
		t.Fatal("unserved hash present in the partial result")
	}
	assertNoInflightCodeReqs(t, syncer)
}

// TestFetchByteCodesTimesOut proves a peer that never answers is given up on
// after onDemandCodeFetchTimeout (as a plain failure, not a context error), and
// that the fetch then fails over to a peer that does answer.
func TestFetchByteCodesTimesOut(t *testing.T) {
	code := []byte{0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	old := onDemandCodeFetchTimeout
	onDemandCodeFetchTimeout = 50 * time.Millisecond
	t.Cleanup(func() { onDemandCodeFetchTimeout = old })

	// Only a silent peer: the fetch ends via the timeout and reports failure.
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	silent := newCodePeer(t, syncer, "silent", neverAnswer)
	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err == nil {
		t.Fatal("expected an error when the only peer never answers")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a peer timeout must not surface as a context error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("timed-out fetch returned codes: %v", got)
	}
	if silent.nBytecodeRequests != 1 {
		t.Fatalf("expected the silent peer to be asked once, got %d", silent.nBytecodeRequests)
	}
	assertNoInflightCodeReqs(t, syncer)

	// Silent plus honest: whichever is tried first, the code comes back.
	syncer = NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	newCodePeer(t, syncer, "silent", neverAnswer)
	newCodePeer(t, syncer, "honest", serveCodesFromCorpus(map[common.Hash][]byte{hash: code}))
	got, err = syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hash], code) {
		t.Fatalf("failover after timeout did not yield the code: got %x", got[hash])
	}
	assertNoInflightCodeReqs(t, syncer)
}

// sendFailPeer is a SyncPeer whose bytecode requests fail to send, modelling a
// peer that dropped mid-request.
type sendFailPeer struct {
	id       string
	requests int
}

func (p *sendFailPeer) ID() string      { return p.id }
func (p *sendFailPeer) Log() log.Logger { return log.New("id", p.id) }

func (p *sendFailPeer) RequestAccountRange(id uint64, root, origin, limit common.Hash, bytes uint64) error {
	return nil
}

func (p *sendFailPeer) RequestStorageRanges(id uint64, root common.Hash, accounts []common.Hash, origin, limit []byte, bytes uint64) error {
	return nil
}

func (p *sendFailPeer) RequestTrieNodes(id uint64, root common.Hash, paths []TrieNodePathSet, bytes uint64) error {
	return nil
}

func (p *sendFailPeer) RequestByteCodes(id uint64, hashes []common.Hash, bytes uint64) error {
	p.requests++
	return errors.New("connection closed")
}

// TestFetchByteCodesFailsOverOnSendError proves a request that cannot even be
// sent counts as that peer not serving: alone it yields a failure with no
// request left in flight, and alongside an honest peer the fetch fails over.
func TestFetchByteCodesFailsOverOnSendError(t *testing.T) {
	code := []byte{0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	broken := &sendFailPeer{id: "broken"}
	if err := syncer.Register(broken); err != nil {
		t.Fatalf("register broken: %v", err)
	}
	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err == nil {
		t.Fatal("expected an error when the only peer cannot be sent a request")
	}
	if len(got) != 0 {
		t.Fatalf("failed send returned codes: %v", got)
	}
	if broken.requests != 1 {
		t.Fatalf("expected one send attempt, got %d", broken.requests)
	}
	assertNoInflightCodeReqs(t, syncer)

	newCodePeer(t, syncer, "honest", serveCodesFromCorpus(map[common.Hash][]byte{hash: code}))
	got, err = syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hash], code) {
		t.Fatalf("failover after a send error did not yield the code: got %x", got[hash])
	}
	assertNoInflightCodeReqs(t, syncer)
}
