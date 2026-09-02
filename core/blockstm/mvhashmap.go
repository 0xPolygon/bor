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
	bloomBits  = 1 << 15        // 32768 bits
	bloomWords = bloomBits / 64 // 512 uint64s = 4KB
	bloomMask  = bloomBits - 1  // bitmask for modulo
)

type writeBloom struct {
	bits [bloomWords]uint64
}

// bloomHashes computes 3 independent bit positions from a Key.
// Key layout: [0:20] address, [20:52] storage hash, [52] subpath byte,
// [53] type byte. h1/h2 cover the address; h3 mixes address middle with
// the hash tail and subpath/type bytes so all key classes diverge in
// the third dimension. bloomFold mixes the upper bits into the kept
// 15 so terms shifted past bit 14 still contribute.
func bloomHashes(k Key) (uint, uint, uint) {
	_ = k[53] // bounds check hint (covers all reads below)
	h1 := uint(k[0]) | uint(k[1])<<8 | uint(k[2])<<16 | uint(k[3])<<24
	h2 := uint(k[16]) | uint(k[17])<<8 | uint(k[18])<<16 | uint(k[19])<<24
	h3 := (uint(k[8]) ^ uint(k[28]) ^ uint(k[52])) |
		(uint(k[9])^uint(k[29]))<<8 |
		(uint(k[10])^uint(k[30]))<<16 |
		(uint(k[11])^uint(k[31])^uint(k[53]))<<24
	return bloomFold(h1), bloomFold(h2), bloomFold(h3)
}

func bloomFold(h uint) uint {
	return (h ^ (h >> 15) ^ (h >> 30)) & bloomMask
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

// Subpath identifiers — must match core/state constants (BalancePath, NoncePath, etc.)
const SubpathBalance byte = 1
const SubpathNonce byte = 2

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

	// Ablation flags for performance experiments
	SkipSettle   bool
	SkipFinalise bool
	SkipMVRead   bool // flush normally but MVRead always returns None
}

func MakeMVHashMap() *MVHashMap {
	mv := &MVHashMap{}
	for i := range mv.shards {
		mv.shards[i].m = make(map[Key]*TxnIndexCells)
	}

	return mv
}

func (mv *MVHashMap) getShard(k Key) *mapShard {
	return &mv.shards[addrShardIndex(k[:common.AddressLength])%numShards]
}

// addrShardIndex returns an FNV-1a hash of an address (20 bytes), used by
// every sharded store in this package for shard selection. The naive
// `addr[0]<<8 | addr[1]` collapses to `addr[1] % N` whenever the shard
// count divides 256 (any power-of-2 ≤ 256), routing all Polygon system
// contracts at 0x...001000 / 0x...001001 / 0x...001010 — and any other
// addresses sharing the first two bytes — to the same shard.
func addrShardIndex(addr []byte) uint {
	const fnvOffset, fnvPrime = uint64(14695981039346656037), uint64(1099511628211)
	h := fnvOffset
	for i := 0; i < len(addr); i++ {
		h = (h ^ uint64(addr[i])) * fnvPrime
	}
	return uint(h)
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
	n := len(c.entries)
	if n == 0 {
		return nil
	}
	// Fast path for small slices: linear scan from end (common case: 1-5 entries).
	if n <= 8 {
		for i := n - 1; i >= 0; i-- {
			if c.entries[i].index <= txIdx {
				return &c.entries[i]
			}
		}
		return nil
	}
	// Binary search for larger slices.
	i := sort.Search(n, func(j int) bool { return c.entries[j].index > txIdx })
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
	defer cells.rw.Unlock()
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
}

func (mv *MVHashMap) MarkEstimate(k Key, txIdx int) {
	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		panic(fmt.Errorf("path must already exist"))
	})

	cells.rw.Lock()
	defer cells.rw.Unlock()
	if pos, found := cells.find(txIdx); !found {
		keys := make([]int, len(cells.entries))
		for i, e := range cells.entries {
			keys[i] = e.index
		}
		panic(fmt.Sprintf("should not happen - cell should be present for path. TxIdx: %v, path, %x, cells keys: %v", txIdx, k, keys))
	} else {
		cells.entries[pos].cell.flag = FlagEstimate
	}
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

func (mv *MVHashMap) Read(k Key, txIdx int) (res MVReadResult) {
	res.depIdx = -1
	res.incarnation = -1

	// Fast path: if bloom filter says key was never written, skip everything.
	if !mv.bloom.mayContain(k) {
		return
	}

	// Ablation: flush normally but MVRead returns None to isolate cascade cost.
	if mv.SkipMVRead {
		return
	}

	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		return nil
	})
	if cells == nil {
		return
	}

	cells.rw.RLock()
	defer cells.rw.RUnlock()

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
		// V1 is test-only post-PR; skipping address-key reads suppresses
		// false vfails from incidental concurrent account access. V2's
		// validation path doesn't share this short-circuit.
		if rd.Path.IsAddress() {
			continue
		}

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
			valid = rd.Kind == ReadKindStorage
		default:
			panic(fmt.Errorf("should not happen - undefined mv read status: %v", mvResult.Status()))
		}

		if !valid {
			break
		}
	}

	return
}
