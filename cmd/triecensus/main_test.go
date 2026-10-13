package main

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// writeNodeSet persists a commit's node set through the real rawdb path-scheme
// accessors, exactly as triedb/pathdb/flush.go does. This keeps the fixture
// faithful to the production key/value layout without depending on pathdb
// flush internals (unexported and untestable from package main).
func writeNodeSet(db ethdb.KeyValueWriter, set *trienode.NodeSet) {
	for path, n := range set.Nodes {
		if n.IsDeleted() {
			continue
		}
		rawdb.WriteTrieNode(db, set.Owner, []byte(path), n.Hash, n.Blob, rawdb.PathScheme)
	}
}

// buildState writes a small state through real trie commits: nAccounts
// accounts, each holding nSlots slots, with half the slot values exactly 32
// bytes (the ambiguous leaf shape) and the rest shorter. It returns the
// memory database holding the path-scheme node tables the census scans.
func buildState(t *testing.T, nAccounts, nSlots int) ethdb.Database {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, nil)
	defer tdb.Close()

	accTrie, err := trie.New(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nAccounts; i++ {
		addrHash := common.BytesToHash([]byte{byte(i + 1)})
		stTrie, err := trie.New(trie.StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < nSlots; j++ {
			var val []byte
			if j%2 == 0 {
				// 32-byte value: RLP-identical to an extension
				// child hash unless the child probe is used.
				val = make([]byte, 32)
				val[0] = byte(j + 1)
			} else {
				val = []byte("value-" + string(rune('a'+j%26)))
			}
			if err := stTrie.Update(common.BigToHash(big.NewInt(int64(j))).Bytes(), val); err != nil {
				t.Fatal(err)
			}
		}
		stRoot, stSet := stTrie.Commit(false)
		writeNodeSet(memdb, stSet)

		acct := &types.StateAccount{
			Nonce:   uint64(i),
			Balance: uint256.NewInt(uint64(i) + 1),
			Root:    stRoot,
		}
		acctRlp, err := rlp.EncodeToBytes(acct)
		if err != nil {
			t.Fatal(err)
		}
		if err := accTrie.Update(addrHash.Bytes(), acctRlp); err != nil {
			t.Fatal(err)
		}
	}
	_, accSet := accTrie.Commit(false)
	writeNodeSet(memdb, accSet)
	return memdb
}

func TestCensusMatchesTrie(t *testing.T) {
	const nAccounts, nSlots = 8, 6
	memdb := buildState(t, nAccounts, nSlots)

	var log bytes.Buffer
	c, err := run(memdb, 5, &log)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.accounts.classes[classLeaf].count; got != nAccounts {
		t.Errorf("account leaves = %d, want %d", got, nAccounts)
	}
	if got := c.accounts.classes[classUnknown].count; got != 0 {
		t.Errorf("account unknown nodes = %d, want 0", got)
	}
	// Only slots with 32-byte values become separate leaf rows; the short
	// "value-a"/"value-c"/"value-e" slots are inlined into their branch.
	const storedLeavesPerTrie = 3
	if got := c.storage.classes[classLeaf].count; got != nAccounts*storedLeavesPerTrie {
		t.Errorf("storage leaf rows = %d, want %d", got, nAccounts*storedLeavesPerTrie)
	}
	if got := c.storage.classes[classUnknown].count; got != 0 {
		t.Errorf("storage unknown nodes = %d, want 0", got)
	}
	if c.decodeErrs != 0 {
		t.Errorf("decode errors = %d, want 0", c.decodeErrs)
	}
	if c.accounts.classes[classBranch].count+c.storage.classes[classBranch].count == 0 {
		t.Error("no branch nodes found in either trie")
	}
	if payload := c.storage.classes[classLeaf].valueBytes; payload == 0 {
		t.Error("storage leaf payload bytes = 0, want nonzero")
	}
	t.Logf("account: leaf=%d branch=%d ext=%d; storage: leaf=%d branch=%d ext=%d\nreport:\n%s",
		c.accounts.classes[classLeaf].count, c.accounts.classes[classBranch].count,
		c.accounts.classes[classExtension].count,
		c.storage.classes[classLeaf].count, c.storage.classes[classBranch].count,
		c.storage.classes[classExtension].count, log.String())
}

// TestShortNodeClassification pins the structural rule: the compact key's
// terminator flag separates leaves from extensions, including a storage leaf
// whose value is exactly 32 bytes (RLP-identical to an extension's child
// hash apart from the flag).
func TestShortNodeClassification(t *testing.T) {
	val32 := make([]byte, 32)
	val32[0] = 0xaa
	leafKey := []byte{0x30} // odd, terminator: hex key [0, 16]
	extKey := []byte{0x10}  // odd, no terminator: hex key [0]

	blob := func(key []byte, val []byte) []byte {
		b, err := rlp.EncodeToBytes([][]byte{key, val})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if class, vlen, err := classify(blob(leafKey, val32)); err != nil ||
		class != classLeaf || vlen != 32 {
		t.Fatalf("32-byte-valued leaf: got class=%d vlen=%d err=%v, want leaf/32", class, vlen, err)
	}
	if class, _, err := classify(blob(extKey, val32)); err != nil || class != classExtension {
		t.Fatalf("extension: got class=%d err=%v, want extension", class, err)
	}
	if class, _, err := classify(blob(leafKey, []byte("tiny"))); err != nil || class != classLeaf {
		t.Fatalf("small-valued leaf: got class=%d err=%v, want leaf", class, err)
	}
	// A branch with an inlined leaf child (small slot value embedded in
	// the parent blob) is still a branch row.
	acctRlp, _ := rlp.EncodeToBytes(&types.StateAccount{})
	leafBlob := blob([]byte{0x20}, acctRlp)
	branch := make([][]byte, 17)
	branch[0] = leafBlob
	branchBlob, err := rlp.EncodeToBytes(branch)
	if err != nil {
		t.Fatal(err)
	}
	if class, _, err := classify(branchBlob); err != nil || class != classBranch {
		t.Fatalf("branch with inlined leaf: got class=%d err=%v, want branch", class, err)
	}
	// An account leaf (list payload, big enough to be a stored row).
	accKey := append([]byte{0x30}, make([]byte, 31)...)
	if class, vlen, err := classify(blob(accKey, acctRlp)); err != nil ||
		class != classLeaf || vlen != len(acctRlp) {
		t.Fatalf("account leaf: got class=%d vlen=%d err=%v, want leaf/%d", class, vlen, err, len(acctRlp))
	}
}
