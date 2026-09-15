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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
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

func TestFetchByteCodesOnDemand(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x00, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)
	unknown := crypto.Keccak256Hash([]byte("no peer has this"))

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	peer := newTestPeer("server", t, func() { t.Helper(); t.Fatal("peer terminated") })
	peer.remote = syncer
	peer.codeRequestHandler = serveCodesFromCorpus(map[common.Hash][]byte{hashA: codeA, hashB: codeB})
	if err := syncer.Register(peer); err != nil {
		t.Fatalf("register peer: %v", err)
	}

	// Both present → both returned and byte-exact.
	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hashA, hashB})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) || !bytes.Equal(got[hashB], codeB) {
		t.Fatalf("wrong codes: got[A]=%x got[B]=%x", got[hashA], got[hashB])
	}

	// A hash no peer serves must not appear in the result (and must not be
	// fabricated); with no verified codes at all, an error is returned.
	got, err = syncer.FetchByteCodes(context.Background(), []common.Hash{unknown})
	if err == nil {
		t.Fatal("expected an error when no connected peer serves the requested code")
	}
	if _, ok := got[unknown]; ok {
		t.Fatalf("unknown hash was served a value: %x", got[unknown])
	}

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
}

// TestFetchByteCodesEmpty covers the trivial early return: with no hashes
// requested the call returns a non-nil empty map and no error, touching no peer.
func TestFetchByteCodesEmpty(t *testing.T) {
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
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

// TestOnDemandByteCodesRouting covers response routing: an unrequested reqid is
// dropped without panicking, and a matched reqid is delivered once and forgotten.
func TestOnDemandByteCodesRouting(t *testing.T) {
	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)

	// Unknown reqid: harmless no-op (must not panic dereferencing a nil req).
	if err := syncer.onDemandByteCodes(onDemandReqidBit|1, [][]byte{{0x01}}); err != nil {
		t.Fatalf("unknown reqid should be dropped: %v", err)
	}

	// Known reqid: delivered to the waiter and removed from the in-flight map.
	req := &onDemandCodeReq{deliver: make(chan [][]byte, 1)}
	syncer.onDemandCodeReqs[onDemandReqidBit|2] = req
	if err := syncer.onDemandByteCodes(onDemandReqidBit|2, [][]byte{{0xaa}}); err != nil {
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
	if _, ok := syncer.onDemandCodeReqs[onDemandReqidBit|2]; ok {
		t.Fatal("reqid was not forgotten after delivery")
	}
}

// TestFetchByteCodesAcrossPeers proves the fetch loops across peers to gather
// distinct hashes each peer can serve, reducing the pending set between rounds.
func TestFetchByteCodesAcrossPeers(t *testing.T) {
	codeA := []byte{0x60, 0x00, 0xf3}
	codeB := []byte{0xfe, 0x01}
	hashA := crypto.Keccak256Hash(codeA)
	hashB := crypto.Keccak256Hash(codeB)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	pA := newTestPeer("pA", t, func() {})
	pA.remote = syncer
	pA.codeRequestHandler = serveCodesFromCorpus(map[common.Hash][]byte{hashA: codeA})
	pB := newTestPeer("pB", t, func() {})
	pB.remote = syncer
	pB.codeRequestHandler = serveCodesFromCorpus(map[common.Hash][]byte{hashB: codeB})
	if err := syncer.Register(pA); err != nil {
		t.Fatalf("register pA: %v", err)
	}
	if err := syncer.Register(pB); err != nil {
		t.Fatalf("register pB: %v", err)
	}

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hashA, hashB})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hashA], codeA) || !bytes.Equal(got[hashB], codeB) {
		t.Fatalf("did not gather both codes across peers: A=%x B=%x", got[hashA], got[hashB])
	}
}

// TestFetchByteCodesFailsOverAndVerifies proves the fetch rejects a peer that
// returns bytes not matching the requested hash (content-addressed check) and
// fails over to a peer that serves the correct blob.
func TestFetchByteCodesFailsOverAndVerifies(t *testing.T) {
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	hash := crypto.Keccak256Hash(code)

	syncer := NewSyncer(rawdb.NewMemoryDatabase(), rawdb.HashScheme)

	liar := newTestPeer("liar", t, func() {})
	liar.remote = syncer
	liar.codeRequestHandler = func(tp *testPeer, id uint64, hashes []common.Hash, _ uint64) error {
		// Serve the wrong bytes for every requested hash.
		out := make([][]byte, len(hashes))
		for i := range out {
			out[i] = []byte{0xde, 0xad, 0xbe, 0xef}
		}
		return tp.remote.OnByteCodes(tp, id, out)
	}
	honest := newTestPeer("honest", t, func() {})
	honest.remote = syncer
	honest.codeRequestHandler = serveCodesFromCorpus(map[common.Hash][]byte{hash: code})

	if err := syncer.Register(liar); err != nil {
		t.Fatalf("register liar: %v", err)
	}
	if err := syncer.Register(honest); err != nil {
		t.Fatalf("register honest: %v", err)
	}

	got, err := syncer.FetchByteCodes(context.Background(), []common.Hash{hash})
	if err != nil {
		t.Fatalf("FetchByteCodes: %v", err)
	}
	if !bytes.Equal(got[hash], code) {
		t.Fatalf("failover did not yield the verified code: got %x want %x", got[hash], code)
	}
}
