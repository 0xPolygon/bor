// Copyright 2026 The go-ethereum Authors
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
	"errors"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// kvIterator yields entries in order. With err set, it fails after failAt of them.
type kvIterator struct {
	entries  []*kv
	failAt   int
	err      error
	pos      int
	failed   bool
	released bool
}

func (it *kvIterator) Next() bool {
	if it.err != nil && it.pos == it.failAt {
		it.failed = true
		return false
	}
	if it.pos == len(it.entries) {
		return false
	}
	it.pos++
	return true
}

func (it *kvIterator) Error() error {
	if it.failed {
		return it.err
	}
	return nil
}

func (it *kvIterator) Hash() common.Hash { return common.BytesToHash(it.entries[it.pos-1].k) }
func (it *kvIterator) Account() []byte   { return it.entries[it.pos-1].v }
func (it *kvIterator) Slot() []byte      { return it.entries[it.pos-1].v }
func (it *kvIterator) Release()          { it.released = true }

var (
	errIteration = errors.New("layer stale")
	rangeKeys    = []common.Hash{common.HexToHash("0x01"), common.HexToHash("0x02"), common.HexToHash("0x03")}
)

// itemSize is the response size of one item of rangeEntries.
const itemSize = common.HashLength + 1

// rangeEntries returns rangeKeys with values {1}, {2}, {3}.
func rangeEntries() []*kv {
	entries := make([]*kv, len(rangeKeys))
	for i, key := range rangeKeys {
		entries[i] = &kv{key.Bytes(), []byte{byte(i + 1)}}
	}
	return entries
}

func TestCollectAccounts(t *testing.T) {
	tests := []struct {
		name     string
		failAt   int
		err      error
		limit    common.Hash
		maxBytes uint64
		want     int
	}{
		{"complete", 0, nil, common.MaxHash, softResponseLimit, 3},
		{"key limit", 0, nil, rangeKeys[1], softResponseLimit, 2},
		{"byte limit", 0, nil, common.MaxHash, itemSize, 2},
		{"byte limit reached", 0, nil, common.MaxHash, 2 * itemSize, 3},
		{"byte limit just below", 0, nil, common.MaxHash, 2*itemSize - 1, 2},
		{"iterator failed midway", 2, errIteration, common.MaxHash, softResponseLimit, 2},
		{"iterator failed at start", 0, errIteration, common.MaxHash, softResponseLimit, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &kvIterator{entries: rangeEntries(), failAt: tt.failAt, err: tt.err}
			accounts, last, err := collectAccounts(it, tt.limit, tt.maxBytes)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error: have %v, want %v", err, tt.err)
			}
			if len(accounts) != tt.want {
				t.Fatalf("accounts: have %d, want %d", len(accounts), tt.want)
			}
			var wantLast common.Hash
			if tt.want > 0 {
				wantLast = rangeKeys[tt.want-1]
			}
			if last != wantLast {
				t.Fatalf("last: have %x, want %x", last, wantLast)
			}
			for i, account := range accounts {
				if account.Hash != rangeKeys[i] || len(account.Body) != 1 || account.Body[0] != byte(i+1) {
					t.Fatalf("account %d: have %x %x", i, account.Hash, account.Body)
				}
			}
		})
	}
}

func TestCollectStorage(t *testing.T) {
	tests := []struct {
		name      string
		failAt    int
		err       error
		limit     common.Hash
		size      uint64
		hardLimit uint64
		want      int
		wantAbort bool
	}{
		{"complete", 0, nil, common.MaxHash, 0, softResponseLimit, 3, false},
		{"key limit", 0, nil, rangeKeys[1], 0, softResponseLimit, 2, false},
		{"hard limit", 0, nil, common.MaxHash, 0, 2 * itemSize, 2, true},
		{"hard limit already reached", 0, nil, common.MaxHash, 10, 10, 0, true},
		{"iterator failed midway", 2, errIteration, common.MaxHash, 0, softResponseLimit, 2, true},
		{"iterator failed at start", 0, errIteration, common.MaxHash, 0, softResponseLimit, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &kvIterator{entries: rangeEntries(), failAt: tt.failAt, err: tt.err}
			size := tt.size
			storage, last, abort, err := collectStorage(it, tt.limit, &size, tt.hardLimit)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error: have %v, want %v", err, tt.err)
			}
			if len(storage) != tt.want || abort != tt.wantAbort {
				t.Fatalf("result: have (%d, %v), want (%d, %v)", len(storage), abort, tt.want, tt.wantAbort)
			}
			var wantLast common.Hash
			if tt.want > 0 {
				wantLast = rangeKeys[tt.want-1]
			}
			if last != wantLast {
				t.Fatalf("last: have %x, want %x", last, wantLast)
			}
			if size != tt.size+uint64(tt.want*itemSize) {
				t.Fatalf("size: have %d, want %d", size, tt.size+uint64(tt.want*itemSize))
			}
			for i, slot := range storage {
				if slot.Hash != rangeKeys[i] || len(slot.Body) != 1 || slot.Body[0] != byte(i+1) {
					t.Fatalf("slot %d: have %x %x", i, slot.Hash, slot.Body)
				}
			}
		})
	}
}

// verifyRange checks a served range the way the requester does: with a proof
// it must be a valid part of the trie at root, without one the whole trie.
func verifyRange(t *testing.T, root common.Hash, origin common.Hash, hashes []common.Hash, values [][]byte, proof [][]byte) bool {
	t.Helper()

	keys := make([][]byte, len(hashes))
	for i, hash := range hashes {
		keys[i] = common.CopyBytes(hash[:])
	}
	if len(proof) == 0 {
		if _, err := trie.VerifyRangeProof(root, nil, keys, values, nil); err != nil {
			t.Fatalf("range without proof rejected: %v", err)
		}
		return false
	}
	nodes := make(trienode.ProofList, len(proof))
	for i, node := range proof {
		nodes[i] = node
	}
	cont, err := trie.VerifyRangeProof(root, origin[:], keys, values, nodes.Set())
	if err != nil {
		t.Fatalf("range with proof rejected: %v", err)
	}
	return cont
}

func TestServeAccountRange(t *testing.T) {
	_, tr, entries := makeAccountTrieNoStorage(100, rawdb.HashScheme)

	tests := []struct {
		name     string
		start    int
		failAt   int
		err      error
		want     int
		wantCont bool
	}{
		{"complete", 0, 0, nil, 100, false},
		{"iterator failed midway", 0, 40, errIteration, 40, true},
		{"iterator failed midway after origin", 10, 30, errIteration, 30, true},
		{"iterator failed at start", 0, 0, errIteration, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var origin common.Hash
			if tt.start > 0 {
				origin = common.BytesToHash(entries[tt.start].k)
			}
			it := &kvIterator{entries: entries[tt.start:], failAt: tt.failAt, err: tt.err}
			req := &GetAccountRangePacket{Root: tr.Hash(), Origin: origin, Limit: common.MaxHash, Bytes: softResponseLimit}

			accounts, proof := serveAccountRange(tr, it, req)
			if !it.released {
				t.Fatal("iterator not released")
			}
			if len(accounts) != tt.want {
				t.Fatalf("accounts: have %d, want %d", len(accounts), tt.want)
			}
			if tt.want == 0 {
				if proof != nil {
					t.Fatal("proof served without accounts")
				}
				return
			}
			hashes := make([]common.Hash, len(accounts))
			values := make([][]byte, len(accounts))
			for i, account := range accounts {
				hashes[i], values[i] = account.Hash, account.Body
			}
			if cont := verifyRange(t, tr.Hash(), origin, hashes, values, proof); cont != tt.wantCont {
				t.Fatalf("more accounts: have %v, want %v", cont, tt.wantCont)
			}
		})
	}
}

// makeStorageRangeState commits the given number of accounts with slots storage
// slots each, returning the database, the state root, the account hashes, and
// each account's storage root and sorted slots.
func makeStorageRangeState(t *testing.T, accounts, slots int) (*triedb.Database, common.Hash, []common.Hash, map[common.Hash]common.Hash, map[common.Hash][]*kv) {
	t.Helper()

	var (
		db       = triedb.NewDatabase(rawdb.NewMemoryDatabase(), newDbConfig(rawdb.HashScheme))
		accTrie  = trie.NewEmpty(db)
		nodes    = trienode.NewMergedNodeSet()
		hashes   []common.Hash
		roots    = make(map[common.Hash]common.Hash)
		contents = make(map[common.Hash][]*kv)
	)
	for i := uint64(1); i <= uint64(accounts); i++ {
		account := common.BytesToHash(key32(i))
		stRoot, stNodes, stEntries := makeStorageTrieWithSeed(account, uint64(slots), 0, db)
		if err := nodes.Merge(stNodes); err != nil {
			t.Fatal(err)
		}
		value, _ := rlp.EncodeToBytes(&types.StateAccount{
			Nonce:    i,
			Balance:  uint256.NewInt(i),
			Root:     stRoot,
			CodeHash: types.EmptyCodeHash.Bytes(),
		})
		accTrie.MustUpdate(account[:], value)
		hashes = append(hashes, account)
		roots[account] = stRoot
		contents[account] = stEntries
	}
	root, set := accTrie.Commit(true)
	if err := nodes.Merge(set); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(root, types.EmptyRootHash, 0, nodes, triedb.NewStateSet()); err != nil {
		t.Fatal(err)
	}
	return db, root, hashes, roots, contents
}

func TestServeStorageRange(t *testing.T) {
	db, root, accounts, roots, contents := makeStorageRangeState(t, 1, 50)
	account := accounts[0]
	entries := contents[account]

	tests := []struct {
		name      string
		account   common.Hash
		start     int
		failAt    int
		err       error
		size      uint64
		want      int
		wantProof bool
		wantCont  bool
		wantDone  bool
		wantErr   bool
	}{
		{name: "complete", account: account, want: 50},
		{name: "from origin", account: account, start: 10, want: 40, wantProof: true, wantDone: true},
		{name: "hard limit", account: account, size: softResponseLimit - 1, want: 1, wantProof: true, wantCont: true, wantDone: true},
		{name: "iterator failed midway", account: account, failAt: 20, err: errIteration, want: 20, wantProof: true, wantCont: true, wantDone: true},
		{name: "iterator failed midway after origin", account: account, start: 10, failAt: 20, err: errIteration, want: 20, wantProof: true, wantCont: true, wantDone: true},
		{name: "iterator failed at start", account: account, err: errIteration, wantDone: true},
		{name: "unknown account", account: common.HexToHash("0xff"), failAt: 20, err: errIteration, want: 20, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var origin common.Hash
			if tt.start > 0 {
				origin = common.BytesToHash(entries[tt.start].k)
			}
			it := &kvIterator{entries: entries[tt.start:], failAt: tt.failAt, err: tt.err}
			size := tt.size

			storage, proof, done, err := serveStorageRange(db, root, tt.account, it, origin, common.MaxHash, &size, softResponseLimit)
			if !it.released {
				t.Fatal("iterator not released")
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error: have %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(storage) != tt.want || done != tt.wantDone || (proof != nil) != tt.wantProof {
				t.Fatalf("result: have (%d slots, proof %v, done %v), want (%d slots, proof %v, done %v)", len(storage), proof != nil, done, tt.want, tt.wantProof, tt.wantDone)
			}
			if tt.want == 0 {
				return
			}
			hashes := make([]common.Hash, len(storage))
			values := make([][]byte, len(storage))
			for i, slot := range storage {
				hashes[i], values[i] = slot.Hash, slot.Body
			}
			if cont := verifyRange(t, roots[account], origin, hashes, values, proof); cont != tt.wantCont {
				t.Fatalf("more slots: have %v, want %v", cont, tt.wantCont)
			}
		})
	}
}

// nodeDB returns a database holding only the given trie nodes.
func nodeDB(nodes ...[][]byte) *triedb.Database {
	diskdb := rawdb.NewMemoryDatabase()
	for _, set := range nodes {
		for _, blob := range set {
			rawdb.WriteLegacyTrieNode(diskdb, crypto.Keccak256Hash(blob), blob)
		}
	}
	return triedb.NewDatabase(diskdb, newDbConfig(rawdb.HashScheme))
}

// pathNodes returns the trie nodes on the paths to the given keys.
func pathNodes(t *testing.T, tr interface {
	Prove([]byte, ethdb.KeyValueWriter) error
}, keys ...[]byte) [][]byte {
	t.Helper()

	proof := trienode.NewProofSet()
	for _, key := range keys {
		if err := tr.Prove(key, proof); err != nil {
			t.Fatalf("failed to prove %x: %v", key, err)
		}
	}
	return proof.List()
}

// rootNode returns the root node of tr.
func rootNode(t *testing.T, tr *trie.Trie) [][]byte {
	t.Helper()

	for _, blob := range pathNodes(t, tr, common.Hash{}.Bytes()) {
		if crypto.Keccak256Hash(blob) == tr.Hash() {
			return [][]byte{blob}
		}
	}
	t.Fatal("root node not found")
	return nil
}

func TestServeAccountRangeProofFailure(t *testing.T) {
	_, full, entries := makeAccountTrieNoStorage(100, rawdb.HashScheme)

	tests := []struct {
		name  string
		nodes [][]byte
	}{
		{"origin unprovable", rootNode(t, full)},
		{"last unprovable", pathNodes(t, full, common.Hash{}.Bytes())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := trie.New(trie.StateTrieID(full.Hash()), nodeDB(tt.nodes))
			if err != nil {
				t.Fatalf("failed to open trie: %v", err)
			}
			it := &kvIterator{entries: entries}
			req := &GetAccountRangePacket{Root: full.Hash(), Limit: common.MaxHash, Bytes: softResponseLimit}

			if accounts, proof := serveAccountRange(tr, it, req); accounts != nil || proof != nil {
				t.Fatalf("served %d accounts and %d proof nodes from an unprovable range", len(accounts), len(proof))
			}
		})
	}
}

func TestServeStorageRangeProofFailure(t *testing.T) {
	db, root, accounts, roots, contents := makeStorageRangeState(t, 2, 50)
	account := accounts[0]

	accTrie, err := trie.New(trie.StateTrieID(root), db)
	if err != nil {
		t.Fatal(err)
	}
	stTrie, err := trie.New(trie.StorageTrieID(root, account, roots[account]), db)
	if err != nil {
		t.Fatal(err)
	}
	accountNodes := pathNodes(t, accTrie, accounts[0][:], accounts[1][:])

	tests := []struct {
		name string
		db   *triedb.Database
	}{
		{"account trie missing", nodeDB()},
		{"account unreachable", nodeDB(rootNode(t, accTrie))},
		{"storage trie missing", nodeDB(accountNodes)},
		{"origin unprovable", nodeDB(accountNodes, rootNode(t, stTrie))},
		{"last unprovable", nodeDB(accountNodes, pathNodes(t, stTrie, common.Hash{}.Bytes()))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &kvIterator{entries: contents[account], failAt: 20, err: errIteration}
			var size uint64

			if _, _, _, err := serveStorageRange(tt.db, root, account, it, common.Hash{}, common.MaxHash, &size, softResponseLimit); err == nil {
				t.Fatal("unprovable range served")
			}
		})
	}
}

func TestServeStorageRanges(t *testing.T) {
	db, root, accounts, roots, contents := makeStorageRangeState(t, 3, 10)

	var accountSize uint64
	for _, entry := range contents[accounts[0]] {
		accountSize += uint64(common.HashLength + len(entry.v))
	}
	unknown := common.HexToHash("0xff")
	key := func(i int) []byte { return contents[accounts[0]][i].k }

	tests := []struct {
		name      string
		accounts  []common.Hash
		origin    []byte
		limit     []byte
		failOpen  int
		bytes     uint64
		want      []int
		wantProof bool
	}{
		{name: "complete", accounts: accounts, failOpen: -1, want: []int{10, 10, 10}},
		{name: "open failed at first account", accounts: accounts, failOpen: 0},
		{name: "open failed at second account", accounts: accounts, failOpen: 1, want: []int{10}},
		{name: "open failed at last account", accounts: accounts, failOpen: 2, want: []int{10, 10}},
		{name: "first account from origin", accounts: accounts, origin: key(3), failOpen: -1, want: []int{7}, wantProof: true},
		{name: "first account within bounds", accounts: accounts, origin: key(2), limit: key(5), failOpen: -1, want: []int{4}, wantProof: true},
		{name: "byte limit reached", accounts: accounts, failOpen: -1, bytes: accountSize, want: []int{10}},
		{name: "hard limit reached", accounts: accounts, failOpen: -1, bytes: accountSize / 2, want: []int{6}, wantProof: true},
		{name: "account without storage", accounts: []common.Hash{accounts[0], common.HexToHash("0xfe")}, failOpen: -1, want: []int{10}},
		{name: "unprovable range", accounts: []common.Hash{unknown}, origin: key(3), failOpen: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opened []*kvIterator
			open := func(account, origin common.Hash) (snapshot.StorageIterator, error) {
				if len(opened) == tt.failOpen {
					return nil, errIteration
				}
				entries := contents[account]
				if account == unknown {
					entries = contents[accounts[0]]
				}
				start, _ := slices.BinarySearchFunc(entries, origin, func(entry *kv, origin common.Hash) int {
					return bytes.Compare(entry.k, origin[:])
				})
				it := &kvIterator{entries: entries[start:]}
				opened = append(opened, it)
				return it, nil
			}
			bytesLimit := tt.bytes
			if bytesLimit == 0 {
				bytesLimit = softResponseLimit
			}
			req := &GetStorageRangesPacket{Root: root, Accounts: tt.accounts, Origin: tt.origin, Limit: tt.limit, Bytes: bytesLimit}

			slots, proof := serveStorageRanges(db, req, open)
			for _, it := range opened {
				if !it.released {
					t.Fatal("iterator not released")
				}
			}
			have := make([]int, len(slots))
			for i, set := range slots {
				have[i] = len(set)
			}
			if !slices.Equal(have, tt.want) || (proof != nil) != tt.wantProof {
				t.Fatalf("result: have (%v slots, proof %v), want (%v slots, proof %v)", have, proof != nil, tt.want, tt.wantProof)
			}
			for i, set := range slots {
				hashes := make([]common.Hash, len(set))
				values := make([][]byte, len(set))
				for j, slot := range set {
					hashes[j], values[j] = slot.Hash, slot.Body
				}
				var setProof [][]byte
				if i == len(slots)-1 {
					setProof = proof
				}
				verifyRange(t, roots[tt.accounts[i]], common.BytesToHash(tt.origin), hashes, values, setProof)
			}
		})
	}
}
