package blockstm

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
)

// writeBloom is a lock-free bloom filter that tracks which keys have been
// written to the MVHashMap. Reads that miss the bloom filter skip the shard
// lock + map lookup entirely. The filter uses 3 hash functions over a 32Kbit
// (4KB) bit array that fits in L1 cache.
//
// False positive rate at typical block sizes:
//   - 500 unique keys:  ~0.01%
//   - 1000 unique keys: ~0.07%
//   - 5000 unique keys: ~5%
const (
	bloomBits  = 1 << 15             // 32768 bits
	bloomWords = bloomBits / 64      // 512 uint64s = 4KB
	bloomMask  = bloomBits - 1       // bitmask for modulo
)

type writeBloom struct {
	bits [bloomWords]uint64
}

// bloomHashes computes 3 independent bit positions from a Key.
// h1 uses address bytes [0:4], h2 uses hash bytes [20:24], h3 mixes [8:12]^[28:32].
// For state keys (address+hash) all three have good entropy. For address-only
// keys (hash bytes are zero) h2 degrades but h1 and h3 still distinguish keys.
func bloomHashes(k Key) (uint, uint, uint) {
	_ = k[31] // bounds check hint
	h1 := uint(k[0]) | uint(k[1])<<8 | uint(k[2])<<16 | uint(k[3])<<24
	h2 := uint(k[20]) | uint(k[21])<<8 | uint(k[22])<<16 | uint(k[23])<<24
	h3 := (uint(k[8]) ^ uint(k[28])) | (uint(k[9])^uint(k[29]))<<8 |
		(uint(k[10])^uint(k[30]))<<16 | (uint(k[11])^uint(k[31]))<<24
	return h1 & bloomMask, h2 & bloomMask, h3 & bloomMask
}

func (b *writeBloom) add(k Key) {
	h1, h2, h3 := bloomHashes(k)
	atomicSetBit(&b.bits[h1/64], h1%64)
	atomicSetBit(&b.bits[h2/64], h2%64)
	atomicSetBit(&b.bits[h3/64], h3%64)
}

func (b *writeBloom) mayContain(k Key) bool {
	h1, h2, h3 := bloomHashes(k)
	return atomic.LoadUint64(&b.bits[h1/64])&(uint64(1)<<(h1%64)) != 0 &&
		atomic.LoadUint64(&b.bits[h2/64])&(uint64(1)<<(h2%64)) != 0 &&
		atomic.LoadUint64(&b.bits[h3/64])&(uint64(1)<<(h3%64)) != 0
}

func atomicSetBit(word *uint64, bit uint) {
	mask := uint64(1) << bit
	for {
		old := atomic.LoadUint64(word)
		if old&mask != 0 {
			return
		}
		if atomic.CompareAndSwapUint64(word, old, old|mask) {
			return
		}
	}
}

const FlagDone = 0
const FlagEstimate = 1

const addressType = 1
const stateType = 2
const subpathType = 3

const KeyLength = common.AddressLength + common.HashLength + 2

type Key [KeyLength]byte

func (k Key) IsAddress() bool {
	return k[KeyLength-1] == addressType
}

func (k Key) IsState() bool {
	return k[KeyLength-1] == stateType
}

func (k Key) IsSubpath() bool {
	return k[KeyLength-1] == subpathType
}

func (k Key) GetAddress() (addr common.Address) {
	copy(addr[:], k[:common.AddressLength])
	return
}

func (k Key) GetStateKey() (hash common.Hash) {
	copy(hash[:], k[common.AddressLength:KeyLength-2])
	return
}

func (k Key) GetSubpath() byte {
	return k[KeyLength-2]
}

func newKey(addr common.Address, hash common.Hash, subpath byte, keyType byte) Key {
	var k Key

	copy(k[:common.AddressLength], addr[:])
	copy(k[common.AddressLength:KeyLength-2], hash[:])
	k[KeyLength-2] = subpath
	k[KeyLength-1] = keyType

	return k
}

func NewAddressKey(addr common.Address) Key {
	var k Key
	copy(k[:common.AddressLength], addr[:])
	k[KeyLength-1] = addressType

	return k
}

func NewStateKey(addr common.Address, hash common.Hash) Key {
	return newKey(addr, hash, 0, stateType)
}

func NewSubpathKey(addr common.Address, subpath byte) Key {
	var k Key
	copy(k[:common.AddressLength], addr[:])
	k[KeyLength-2] = subpath
	k[KeyLength-1] = subpathType

	return k
}

const numShards = 16

type mapShard struct {
	mu sync.RWMutex
	m  map[Key]*TxnIndexCells
}

type MVHashMap struct {
	shards [numShards]mapShard
	bloom  writeBloom
}

func MakeMVHashMap() *MVHashMap {
	mv := &MVHashMap{}
	for i := range mv.shards {
		mv.shards[i].m = make(map[Key]*TxnIndexCells)
	}

	return mv
}

func (mv *MVHashMap) getShard(k Key) *mapShard {
	// Use first bytes of key for shard selection. The key starts with address
	// bytes which have good entropy for distribution.
	h := uint(k[0])<<8 | uint(k[1])
	return &mv.shards[h%numShards]
}

type WriteCell struct {
	flag        uint
	incarnation int
	data        interface{}
}

type txnEntry struct {
	index int
	cell  *WriteCell
}

// TxnIndexCells stores write cells sorted by transaction index.
// Uses a sorted slice for cache-friendly Floor queries on small N.
// Typical per-key writer count is 1-5 in a block, making linear/binary
// search on a contiguous slice faster than tree or bitmap alternatives.
type TxnIndexCells struct {
	rw      sync.RWMutex
	entries []txnEntry
}

type Version struct {
	TxnIndex    int
	Incarnation int
}

func (mv *MVHashMap) getKeyCells(k Key, fNoKey func(kenc Key) *TxnIndexCells) (cells *TxnIndexCells) {
	shard := mv.getShard(k)
	shard.mu.RLock()
	cells, ok := shard.m[k]
	shard.mu.RUnlock()

	if !ok {
		cells = fNoKey(k)
	}

	return
}

// find returns the index in the sorted slice where txIdx is or would be inserted.
func (c *TxnIndexCells) find(txIdx int) (int, bool) {
	i := sort.Search(len(c.entries), func(j int) bool { return c.entries[j].index >= txIdx })
	if i < len(c.entries) && c.entries[i].index == txIdx {
		return i, true
	}

	return i, false
}

// floor returns the entry with the largest index <= txIdx, or nil if none.
func (c *TxnIndexCells) floor(txIdx int) *txnEntry {
	i := sort.Search(len(c.entries), func(j int) bool { return c.entries[j].index > txIdx })
	if i == 0 {
		return nil
	}

	return &c.entries[i-1]
}

func (mv *MVHashMap) Write(k Key, v Version, data interface{}) {
	mv.bloom.add(k)

	cells := mv.getKeyCells(k, func(kenc Key) (cells *TxnIndexCells) {
		shard := mv.getShard(kenc)
		shard.mu.Lock()
		cells, ok := shard.m[kenc]
		if !ok {
			cells = &TxnIndexCells{}
			shard.m[kenc] = cells
		}
		shard.mu.Unlock()

		return
	})

	cells.rw.Lock()
	if pos, found := cells.find(v.TxnIndex); !found {
		// Insert at sorted position
		cells.entries = append(cells.entries, txnEntry{})
		copy(cells.entries[pos+1:], cells.entries[pos:])
		cells.entries[pos] = txnEntry{
			index: v.TxnIndex,
			cell: &WriteCell{
				flag:        FlagDone,
				incarnation: v.Incarnation,
				data:        data,
			},
		}
	} else {
		ci := cells.entries[pos].cell
		if ci.incarnation > v.Incarnation {
			panic(fmt.Errorf("existing transaction value does not have lower incarnation: %v, %v",
				k, v.TxnIndex))
		}
		ci.flag = FlagDone
		ci.incarnation = v.Incarnation
		ci.data = data
	}
	cells.rw.Unlock()
}

func (mv *MVHashMap) MarkEstimate(k Key, txIdx int) {
	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		panic(fmt.Errorf("path must already exist"))
	})

	cells.rw.Lock()
	if pos, found := cells.find(txIdx); !found {
		keys := make([]int, len(cells.entries))
		for i, e := range cells.entries {
			keys[i] = e.index
		}
		panic(fmt.Sprintf("should not happen - cell should be present for path. TxIdx: %v, path, %x, cells keys: %v", txIdx, k, keys))
	} else {
		cells.entries[pos].cell.flag = FlagEstimate
	}
	cells.rw.Unlock()
}

// Delete removes the entry for txIdx.
func (mv *MVHashMap) Delete(k Key, txIdx int) {
	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		panic(fmt.Errorf("path must already exist"))
	})

	cells.rw.Lock()
	defer cells.rw.Unlock()

	if pos, found := cells.find(txIdx); found {
		cells.entries = append(cells.entries[:pos], cells.entries[pos+1:]...)
	}
}

const (
	MVReadResultDone       = 0
	MVReadResultDependency = 1
	MVReadResultNone       = 2
)

type MVReadResult struct {
	depIdx      int
	incarnation int
	value       interface{}
}

func (res *MVReadResult) DepIdx() int {
	return res.depIdx
}

func (res *MVReadResult) Incarnation() int {
	return res.incarnation
}

func (res *MVReadResult) Value() interface{} {
	return res.value
}

func (res MVReadResult) Status() int {
	if res.depIdx != -1 {
		if res.incarnation == -1 {
			return MVReadResultDependency
		} else {
			return MVReadResultDone
		}
	}

	return MVReadResultNone
}

// BloomMayContain returns true if the key might have been written to the MVHashMap.
// A false return guarantees no transaction has written this key.
func (mv *MVHashMap) BloomMayContain(k Key) bool {
	return mv.bloom.mayContain(k)
}

func (mv *MVHashMap) Read(k Key, txIdx int) (res MVReadResult) {
	res.depIdx = -1
	res.incarnation = -1

	// Fast path: if bloom filter says key was never written, skip shard lock + map lookup.
	if !mv.bloom.mayContain(k) {
		return
	}

	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		return nil
	})
	if cells == nil {
		return
	}

	cells.rw.RLock()

	if entry := cells.floor(txIdx - 1); entry != nil {
		c := entry.cell
		switch c.flag {
		case FlagEstimate:
			res.depIdx = entry.index
			res.value = c.data
		case FlagDone:
			res.depIdx = entry.index
			res.incarnation = c.incarnation
			res.value = c.data
		default:
			panic(fmt.Errorf("should not happen - unknown flag value"))
		}
	}

	cells.rw.RUnlock()

	return
}

func (mv *MVHashMap) FlushMVWriteSet(writes []WriteDescriptor) {
	for _, v := range writes {
		mv.Write(v.Path, v.V, v.Val)
	}
}

func ValidateVersion(txIdx int, lastInputOutput *TxnInputOutput, versionedData *MVHashMap) (valid bool) {
	valid = true

	for _, rd := range lastInputOutput.ReadSet(txIdx) {
		mvResult := versionedData.Read(rd.Path, txIdx)
		switch mvResult.Status() {
		case MVReadResultDone:
			valid = rd.Kind == ReadKindMap && rd.V == Version{
				TxnIndex:    mvResult.depIdx,
				Incarnation: mvResult.incarnation,
			}
		case MVReadResultDependency:
			valid = false
		case MVReadResultNone:
			valid = rd.Kind == ReadKindStorage // feels like an assertion?
		default:
			panic(fmt.Errorf("should not happen - undefined mv read status: %ver", mvResult.Status()))
		}

		if !valid {
			break
		}
	}

	return
}
