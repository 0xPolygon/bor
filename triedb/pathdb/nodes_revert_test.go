package pathdb

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestBufferRevertBufferedNodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"shrink", []byte{1}},
		{"grow", []byte{1, 2, 3, 4}},
		{"delete", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, storage := common.Hash{}, common.Hash{1}
			current := trienode.New(crypto.Keccak256Hash([]byte{2, 3}), []byte{2, 3})
			previous := trienode.New(crypto.Keccak256Hash(tc.blob), tc.blob)
			if tc.blob == nil {
				previous = trienode.NewDeleted()
			}
			nodes := newNodeSet(map[common.Hash]map[string]*trienode.Node{
				account: {"a": current, "b": current},
				storage: {"s": current, "t": current},
			})
			buffer := newBuffer(1024, 80, nodes, nil, 2)
			if err := buffer.revertTo(nil, map[common.Hash]map[string]*trienode.Node{
				account: {"a": previous},
				storage: {"s": previous},
			}, nil, nil); err != nil {
				t.Fatal(err)
			}
			for owner, paths := range map[common.Hash][]string{account: {"a", "b"}, storage: {"s", "t"}} {
				for i, path := range paths {
					want := current
					if i == 0 {
						want = previous
					}
					if got, ok := buffer.node(owner, []byte(path)); !ok || got.Hash != want.Hash || !bytes.Equal(got.Blob, want.Blob) {
						t.Fatalf("node %x/%s = %v, want %v", owner, path, got, want)
					}
				}
			}
			wantSize := uint64(2*common.HashLength + 4 + 2*len(current.Blob) + 2*len(tc.blob))
			if buffer.nodes.size != wantSize || buffer.layers != 1 {
				t.Fatalf("size/layers = %d/%d, want %d/1", buffer.nodes.size, buffer.layers, wantSize)
			}
		})
	}
}

func TestNodeSetRevertPersistedNodes(t *testing.T) {
	for _, owner := range []common.Hash{{}, {1}} {
		t.Run(owner.Hex(), func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			blob, path := []byte{1, 2, 3}, []byte{4}
			if owner == (common.Hash{}) {
				rawdb.WriteAccountTrieNode(db, path, blob)
			} else {
				rawdb.WriteStorageTrieNode(db, owner, path, blob)
			}
			nodes := newNodeSet(map[common.Hash]map[string]*trienode.Node{owner: {}})
			nodes.revertTo(db, map[common.Hash]map[string]*trienode.Node{
				owner: {string(path): trienode.New(crypto.Keccak256Hash(blob), blob)},
			})
			if _, ok := nodes.node(owner, path); ok || nodes.size != 0 {
				t.Fatal("unchanged persisted node was added to the buffer")
			}
		})
	}
}

func TestNodeSetRevertMissingNodes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		owner       common.Hash
		withStorage bool
		wantPanic   string
	}{
		{"account", common.Hash{}, false, "non-existent account node"},
		{"storage owner", common.Hash{1}, false, "non-existent subset"},
		{"storage node", common.Hash{1}, true, "non-existent storage node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			nodes := newNodeSet(nil)
			if tc.withStorage {
				nodes.storageNodes[tc.owner] = make(map[string]*trienode.Node)
			}
			defer func() {
				if got := recover(); got == nil || !strings.Contains(fmt.Sprint(got), tc.wantPanic) {
					t.Fatalf("panic = %v, want %q", got, tc.wantPanic)
				}
			}()
			blob := []byte{1}
			nodes.revertTo(db, map[common.Hash]map[string]*trienode.Node{
				tc.owner: {"a": trienode.New(crypto.Keccak256Hash(blob), blob)},
			})
		})
	}
}
