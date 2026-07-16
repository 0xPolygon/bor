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
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>

package pathdb

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

// nodeEntry describes a single trie-node mutation to embed in a diff layer.
// A nil blob encodes a node deletion (trienode.NewDeleted).
type nodeEntry struct {
	owner common.Hash
	path  string
	blob  []byte
}

// makeNodeSet builds a *nodeSetWithOrigin from the provided mutations, grouping
// them by owner exactly as the trie commit path would (owner == common.Hash{}
// is the account trie, any other owner is a storage trie).
func makeNodeSet(entries ...nodeEntry) *nodeSetWithOrigin {
	nodes := make(map[common.Hash]map[string]*trienode.Node)
	for _, e := range entries {
		subset, ok := nodes[e.owner]
		if !ok {
			subset = make(map[string]*trienode.Node)
			nodes[e.owner] = subset
		}
		if e.blob == nil {
			subset[e.path] = trienode.NewDeleted()
		} else {
			subset[e.path] = trienode.New(crypto.Keccak256Hash(e.blob), e.blob)
		}
	}
	return NewNodeSetWithOrigin(nodes, nil)
}

func acct(path string, blob []byte) nodeEntry {
	return nodeEntry{owner: common.Hash{}, path: path, blob: blob}
}

func slot(owner common.Hash, path string, blob []byte) nodeEntry {
	return nodeEntry{owner: owner, path: path, blob: blob}
}

// TestNodeLookup mirrors TestAccountLookup/TestStorageLookup, but for the trie
// node lookup index. It hand-verifies that lookupNode resolves the layer that is
// guaranteed to contain the most-recent version of a node at a given state,
// across the top/middle/bottom of the diff stack and after a cap.
func TestNodeLookup(t *testing.T) {
	var (
		owner = common.Hash{0xaa} // one storage trie owner exercised alongside the account trie
		blob  = []byte{0xff}      // node payload is irrelevant to layer resolution
	)
	// Chain:
	//   C1->C2->C3->C4 (HEAD)
	// account nodes:  a@{2,4}   b@{3}      c@{4}
	// storage nodes:  s@{2,4}              (owner 0xaa)
	tr := newTestLayerTree() // base = 0x1
	tr.add(common.Hash{0x2}, common.Hash{0x1}, 1, makeNodeSet(acct("a", blob), slot(owner, "s", blob)), emptyStateSet())
	tr.add(common.Hash{0x3}, common.Hash{0x2}, 2, makeNodeSet(acct("b", blob)), emptyStateSet())
	tr.add(common.Hash{0x4}, common.Hash{0x3}, 3, makeNodeSet(acct("a", blob), acct("c", blob), slot(owner, "s", blob)), emptyStateSet())

	type tc struct {
		owner  common.Hash
		path   string
		state  common.Hash
		expect common.Hash
	}
	cases := []tc{
		// unknown node -> disk base
		{common.Hash{}, "d", common.Hash{0x4}, common.Hash{0x1}},
		// lookup from the top (HEAD = C4)
		{common.Hash{}, "a", common.Hash{0x4}, common.Hash{0x4}},
		{common.Hash{}, "b", common.Hash{0x4}, common.Hash{0x3}},
		{common.Hash{}, "c", common.Hash{0x4}, common.Hash{0x4}},
		{owner, "s", common.Hash{0x4}, common.Hash{0x4}},
		// lookup from the middle (C3)
		{common.Hash{}, "a", common.Hash{0x3}, common.Hash{0x2}},
		{common.Hash{}, "b", common.Hash{0x3}, common.Hash{0x3}},
		{common.Hash{}, "c", common.Hash{0x3}, common.Hash{0x1}}, // not found -> base
		{owner, "s", common.Hash{0x3}, common.Hash{0x2}},
		// lookup from C2
		{common.Hash{}, "a", common.Hash{0x2}, common.Hash{0x2}},
		{common.Hash{}, "b", common.Hash{0x2}, common.Hash{0x1}}, // not found -> base
		{owner, "s", common.Hash{0x2}, common.Hash{0x2}},
		// lookup from the bottom (disk base)
		{common.Hash{}, "a", common.Hash{0x1}, common.Hash{0x1}}, // not found -> base
		{owner, "s", common.Hash{0x1}, common.Hash{0x1}},         // not found -> base
	}
	for i, c := range cases {
		l, err := tr.lookupNode(c.owner, []byte(c.path), c.state)
		if err != nil {
			t.Fatalf("case %d: unexpected error: %v", i, err)
		}
		if l.rootHash() != c.expect {
			t.Errorf("case %d: unexpected tip, want %x, got %x", i, c.expect, l.rootHash())
		}
	}

	// Chain:
	//   C3->C4 (HEAD)  -- flatten C1,C2 into the new disk layer C3
	tr.cap(common.Hash{0x4}, 1)

	cases2 := []struct {
		owner     common.Hash
		path      string
		state     common.Hash
		expect    common.Hash
		expectErr error
	}{
		{common.Hash{}, "d", common.Hash{0x4}, common.Hash{0x3}, nil}, // unknown -> new base
		{common.Hash{}, "a", common.Hash{0x4}, common.Hash{0x4}, nil},
		{common.Hash{}, "b", common.Hash{0x4}, common.Hash{0x3}, nil}, // b folded into base C3
		{common.Hash{}, "c", common.Hash{0x4}, common.Hash{0x4}, nil},
		{owner, "s", common.Hash{0x4}, common.Hash{0x4}, nil},
		{common.Hash{}, "a", common.Hash{0x3}, common.Hash{0x3}, nil}, // base fallback
		{owner, "s", common.Hash{0x3}, common.Hash{0x3}, nil},         // base fallback
		// stale states (flattened away)
		{common.Hash{}, "a", common.Hash{0x2}, common.Hash{}, errSnapshotStale},
		{owner, "s", common.Hash{0x2}, common.Hash{}, errSnapshotStale},
		{common.Hash{}, "a", common.Hash{0x1}, common.Hash{}, errSnapshotStale},
	}
	for i, c := range cases2 {
		l, err := tr.lookupNode(c.owner, []byte(c.path), c.state)
		if c.expectErr != nil {
			if !errors.Is(err, c.expectErr) {
				t.Fatalf("case2 %d: unexpected error, want %v, got %v", i, c.expectErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case2 %d: unexpected error: %v", i, err)
		}
		if l.rootHash() != c.expect {
			t.Errorf("case2 %d: unexpected tip, want %x, got %x", i, c.expect, l.rootHash())
		}
	}
}

// TestNodeLookupEquivalence is the core correctness proof: for every observable
// (owner, path, state) the index-based resolution (lookupNode -> l.node) must
// return byte-identical data to the legacy newest->oldest diff-layer walk
// (entryLayer.node). The synthetic stack exercises re-writes at the same path,
// deletions, a resurrected path, account and storage tries, and a side branch.
func TestNodeLookupEquivalence(t *testing.T) {
	var (
		o1 = common.Hash{0xa1} // storage owner 1
		o2 = common.Hash{0xa2} // storage owner 2
	)
	b := func(tag ...byte) []byte { return append([]byte{0x9e}, tag...) }

	tr := newTestLayerTree() // base = 0x1

	// Main chain: C1->C2->C3->C4 (HEAD)
	// Side branch off C2: C3'->C4'
	//
	//   C1(disk) -> C2 -> C3  -> C4
	//                  \-> C3' -> C4'
	tr.add(common.Hash{0x2}, common.Hash{0x1}, 1, makeNodeSet(
		acct("p1", b(0x2)),     // p1 written
		acct("p2", b(0x2)),     // p2 written
		slot(o1, "s1", b(0x2)), // o1/s1 written
	), emptyStateSet())
	tr.add(common.Hash{0x3}, common.Hash{0x2}, 2, makeNodeSet(
		acct("p2", nil),        // p2 deleted
		slot(o2, "s2", b(0x3)), // o2/s2 written
	), emptyStateSet())
	tr.add(common.Hash{0x4}, common.Hash{0x3}, 3, makeNodeSet(
		acct("p1", b(0x4)),     // p1 rewritten
		acct("p2", b(0x4)),     // p2 resurrected
		slot(o1, "s1", b(0x4)), // o1/s1 rewritten
	), emptyStateSet())
	// Side branch
	tr.add(common.Hash{0x30}, common.Hash{0x2}, 2, makeNodeSet(
		acct("p1", b(0x30)),     // divergent p1
		slot(o2, "s2", b(0x30)), // divergent o2/s2
	), emptyStateSet())
	tr.add(common.Hash{0x40}, common.Hash{0x30}, 3, makeNodeSet(
		acct("p3", b(0x40)), // p3 only on side branch
	), emptyStateSet())

	// Every (owner, path) that appears anywhere, plus deliberately-absent probes.
	probes := []struct {
		owner common.Hash
		path  string
	}{
		{common.Hash{}, "p1"},
		{common.Hash{}, "p2"},
		{common.Hash{}, "p3"},
		{common.Hash{}, "absent"},
		{o1, "s1"},
		{o1, "absent"},
		{o2, "s2"},
		{o2, "absent"},
		{common.Hash{0xff}, "s1"}, // unknown owner
	}

	verify := func(t *testing.T, tr *layerTree) {
		t.Helper()
		// Only states currently present in the tree are reachable by a reader
		// (NodeReader resolves via tree.get(root)); enumerate those.
		tr.lock.RLock()
		states := make([]common.Hash, 0, len(tr.layers))
		for root := range tr.layers {
			states = append(states, root)
		}
		tr.lock.RUnlock()

		for _, st := range states {
			entry := tr.get(st)
			if entry == nil {
				t.Fatalf("state %x missing from tree", st)
			}
			// Skip dangling states: after a cap flattens a fork point, the
			// orphaned side-branch layers still chain into the now-stale old
			// disk layer (they are removed "later" by the caller). A canonical
			// reader never targets such a state, and the lookup index resolves
			// them via the descendants map + base fallback rather than the
			// legacy walk — the same intentional divergence that lookupAccount
			// and lookupStorage already exhibit. Restrict the walk-vs-index
			// equivalence to live states (parent chain reaches the live base).
			if !liveState(tr, st) {
				continue
			}
			for _, p := range probes {
				// Legacy path: walk newest->oldest from the entry layer.
				wBlob, wHash, _, wErr := entry.node(p.owner, []byte(p.path), 0)

				// Index path: resolve the owning layer, then read at depth 0.
				l, err := tr.lookupNode(p.owner, []byte(p.path), st)
				if err != nil {
					t.Fatalf("state %x (%x,%s): lookupNode failed for a present state: %v",
						st, p.owner, p.path, err)
				}
				iBlob, iHash, _, iErr := l.node(p.owner, []byte(p.path), 0)

				if (wErr == nil) != (iErr == nil) {
					t.Fatalf("state %x (%x,%s): err mismatch, walk=%v index=%v",
						st, p.owner, p.path, wErr, iErr)
				}
				if !bytes.Equal(wBlob, iBlob) {
					t.Fatalf("state %x (%x,%s): blob mismatch, walk=%x index=%x",
						st, p.owner, p.path, wBlob, iBlob)
				}
				if wHash != iHash {
					t.Fatalf("state %x (%x,%s): hash mismatch, walk=%x index=%x",
						st, p.owner, p.path, wHash, iHash)
				}
			}
		}
	}

	// Full stack.
	verify(t, tr)

	// After a partial cap (flatten the two bottom layers), the invariant must
	// still hold for every surviving state.
	tr.cap(common.Hash{0x4}, 2)
	verify(t, tr)
}

// TestNodeLookupConcurrent exercises the BlockSTM-v2 hazard: many goroutines
// resolving trie nodes through the index while the layer stack is being mutated
// (add + cap). Intended to run under -race; correctness is asserted by the race
// detector plus the invariant that a resolved layer actually serves the node.
func TestNodeLookupConcurrent(t *testing.T) {
	const (
		total   = 160 // diff layers to add
		keep    = 32  // cap target
		readers = 8
	)
	blob := []byte{0x77}
	root := func(i int) common.Hash {
		// Distinct from the base root {0x1}; 0xdead prefix avoids collisions.
		return common.BytesToHash([]byte{0xde, 0xad, byte(i >> 8), byte(i)})
	}
	paths := []string{"a", "b", "c", "d", "e"}

	tr := newTestLayerTree() // base = 0x1

	// Pre-generate the full root sequence so readers can index it without
	// racing on test-owned state (roots[0] is the disk base).
	roots := make([]common.Hash, total+1)
	roots[0] = common.Hash{0x1}
	for i := 1; i <= total; i++ {
		roots[i] = root(i)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: continuously resolve nodes at arbitrary (possibly stale or
	// not-yet-created) states. Staleness/missing states are expected and
	// tolerated; a resolved diff layer must actually contain the node.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			n := seed
			for {
				select {
				case <-stop:
					return
				default:
				}
				n = (n*1103515245 + 12345) & 0x7fffffff
				st := roots[n%len(roots)]
				path := paths[n%len(paths)]
				owner := common.Hash{}
				if n%3 == 0 {
					owner = common.Hash{0xaa}
				}
				l, err := tr.lookupNode(owner, []byte(path), st)
				if err != nil {
					continue // stale/missing state: expected under concurrent cap
				}
				if _, _, _, err := l.node(owner, []byte(path), 0); err != nil &&
					!errors.Is(err, errSnapshotStale) {
					t.Errorf("resolved layer failed to serve node: %v", err)
					return
				}
			}
		}(r + 1)
	}

	// Writer: extend the chain, rotating which node paths each layer touches,
	// and cap periodically to force flatten/removeLayer under concurrent reads.
	for i := 1; i <= total; i++ {
		path := paths[i%len(paths)]
		owner := common.Hash{}
		if i%3 == 0 {
			owner = common.Hash{0xaa}
		}
		if err := tr.add(roots[i], roots[i-1], uint64(i), makeNodeSet(nodeEntry{owner: owner, path: path, blob: blob}), emptyStateSet()); err != nil {
			t.Fatalf("add layer %d: %v", i, err)
		}
		if i%keep == 0 {
			if err := tr.cap(roots[i], keep); err != nil {
				t.Fatalf("cap at %d: %v", i, err)
			}
		}
	}
	close(stop)
	wg.Wait()
}

// emptyStateSet returns a state set with no flat-state mutations, isolating the
// trie-node index under test from the account/storage index.
func emptyStateSet() *StateSetWithOrigin {
	return NewStateSetWithOrigin(nil, nil, nil, nil, false)
}

// liveState reports whether the layer at root is on the live chain, i.e. its
// parent chain terminates at the tree's current base disk layer. Dangling
// side-branch layers left behind by a cap chain into a stale old disk layer
// instead and are not reachable by a canonical reader.
func liveState(tr *layerTree, root common.Hash) bool {
	base := tr.bottom().rootHash()
	for l := tr.get(root); l != nil; l = l.parentLayer() {
		if _, ok := l.(*diskLayer); ok {
			return l.rootHash() == base
		}
	}
	return false
}

// --- Steady-state evidence -------------------------------------------------
//
// BenchmarkNodeResolve compares the legacy newest->oldest diff-layer walk with
// the O(1) index resolution over a full 128-layer stack. Unlike the in-campaign
// catch-up profile (deep walks, layers saturated), these measure the per-read
// cost at a representative tip depth so the PR can quote a steady-state number.

func buildBenchTree(b *testing.B, layers int) (*layerTree, common.Hash, string, common.Hash) {
	b.Helper()
	blob := []byte{0x42}
	tr := newTestLayerTree() // base = 0x1
	parent := common.Hash{0x1}
	root := func(i int) common.Hash { return common.BytesToHash([]byte{0xbe, 0xef, byte(i >> 8), byte(i)}) }

	// The probed node is written only in the bottom-most diff layer, so the
	// legacy walk pays the full depth while the index stays O(1).
	deepPath := "deep"
	for i := 1; i <= layers; i++ {
		entries := []nodeEntry{acct(fmt.Sprintf("p%d", i), blob)}
		if i == 1 {
			entries = append(entries, acct(deepPath, blob))
		}
		r := root(i)
		if err := tr.add(r, parent, uint64(i), makeNodeSet(entries...), emptyStateSet()); err != nil {
			b.Fatalf("add %d: %v", i, err)
		}
		parent = r
	}
	return tr, parent, deepPath, root(1)
}

func BenchmarkNodeResolveWalkDeep(b *testing.B) {
	tr, head, deepPath, _ := buildBenchTree(b, 128)
	entry := tr.get(head)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := entry.node(common.Hash{}, []byte(deepPath), 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNodeResolveIndexDeep(b *testing.B) {
	tr, head, deepPath, _ := buildBenchTree(b, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l, err := tr.lookupNode(common.Hash{}, []byte(deepPath), head)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := l.node(common.Hash{}, []byte(deepPath), 0); err != nil {
			b.Fatal(err)
		}
	}
}

// The parallel variants evidence the BlockSTM-v2 amplifier: the legacy walk
// acquires dl.lock.RLock() at every one of the (up to 128) hops, so many
// concurrent EVM goroutines resolving nodes bounce those RWMutex cache lines.
// The index takes a single tree.lock.RLock() plus one diff-layer RLock, so it
// degrades far less as reader parallelism rises. Run e.g. -cpu 1,8,16.

func BenchmarkNodeResolveWalkDeepParallel(b *testing.B) {
	tr, head, deepPath, _ := buildBenchTree(b, 128)
	entry := tr.get(head)
	path := []byte(deepPath)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, _, err := entry.node(common.Hash{}, path, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkNodeResolveIndexDeepParallel(b *testing.B) {
	tr, head, deepPath, _ := buildBenchTree(b, 128)
	path := []byte(deepPath)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l, err := tr.lookupNode(common.Hash{}, path, head)
			if err != nil {
				b.Fatal(err)
			}
			if _, _, _, err := l.node(common.Hash{}, path, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
}
