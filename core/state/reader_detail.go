package state

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Read-detail instrumentation (lab-only). When a *ReadDetail is attached to a
// readerWithCacheStats, every Account/Storage call is timed and every
// cache-miss is logged as an event with its backing-resolution latency. With
// no detail attached (the default) the added cost is a single nil check.

// readDetailMaxMisses bounds the per-block miss event log; overflow is counted.
const readDetailMaxMisses = 4096

// ReadMissEvent is one cache miss with its backing-read resolution latency.
type ReadMissEvent struct {
	Key       uint64 // FNV-1a of addr (accounts) or addr||slot (storage)
	Storage   bool   // false = account, true = storage slot
	LatencyUs int64
}

// ReadDetailStats are cumulative time/count sums split by hit/miss and kind.
type ReadDetailStats struct {
	AccountHitN, AccountMissN   int64
	AccountHitUs, AccountMissUs int64
	StorageHitN, StorageMissN   int64
	StorageHitUs, StorageMissUs int64
}

// ReadDetail collects timing sums and per-miss events for one reader.
type ReadDetail struct {
	// CollectTouched additionally records the set of touched key hashes (hits
	// AND misses) — feeds the re-reference distance ring. Set before attaching.
	CollectTouched bool

	accountHitN, accountMissN   atomic.Int64
	accountHitNs, accountMissNs atomic.Int64
	storageHitN, storageMissN   atomic.Int64
	storageHitNs, storageMissNs atomic.Int64

	mu            sync.Mutex
	misses        []ReadMissEvent
	touched       map[uint64]struct{}
	missesDropped atomic.Int64
}

// Snapshot returns the current cumulative sums (µs).
func (d *ReadDetail) Snapshot() ReadDetailStats {
	if d == nil {
		return ReadDetailStats{}
	}
	return ReadDetailStats{
		AccountHitN: d.accountHitN.Load(), AccountMissN: d.accountMissN.Load(),
		AccountHitUs: d.accountHitNs.Load() / 1e3, AccountMissUs: d.accountMissNs.Load() / 1e3,
		StorageHitN: d.storageHitN.Load(), StorageMissN: d.storageMissN.Load(),
		StorageHitUs: d.storageHitNs.Load() / 1e3, StorageMissUs: d.storageMissNs.Load() / 1e3,
	}
}

// TakeMisses returns the recorded miss events and the overflow-drop count.
func (d *ReadDetail) TakeMisses() ([]ReadMissEvent, int64) {
	if d == nil {
		return nil, 0
	}
	d.mu.Lock()
	out := d.misses
	d.misses = nil
	d.mu.Unlock()
	return out, d.missesDropped.Load()
}

func (d *ReadDetail) recordAccount(addr common.Address, hit bool, dur time.Duration) {
	if hit {
		d.accountHitN.Add(1)
		d.accountHitNs.Add(dur.Nanoseconds())
		if d.CollectTouched {
			d.touch(fnvAddr(addr))
		}
		return
	}
	d.accountMissN.Add(1)
	d.accountMissNs.Add(dur.Nanoseconds())
	key := fnvAddr(addr)
	if d.CollectTouched {
		d.touch(key)
	}
	d.appendMiss(ReadMissEvent{Key: key, Storage: false, LatencyUs: dur.Microseconds()})
}

func (d *ReadDetail) recordStorage(addr common.Address, slot common.Hash, hit bool, dur time.Duration) {
	if hit {
		d.storageHitN.Add(1)
		d.storageHitNs.Add(dur.Nanoseconds())
		if d.CollectTouched {
			d.touch(fnvSlot(addr, slot))
		}
		return
	}
	d.storageMissN.Add(1)
	d.storageMissNs.Add(dur.Nanoseconds())
	key := fnvSlot(addr, slot)
	if d.CollectTouched {
		d.touch(key)
	}
	d.appendMiss(ReadMissEvent{Key: key, Storage: true, LatencyUs: dur.Microseconds()})
}

func (d *ReadDetail) touch(key uint64) {
	d.mu.Lock()
	if d.touched == nil {
		d.touched = make(map[uint64]struct{}, 4096)
	}
	d.touched[key] = struct{}{}
	d.mu.Unlock()
}

// TakeTouched drains and returns the touched-key set (nil unless CollectTouched).
func (d *ReadDetail) TakeTouched() []uint64 {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.touched) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(d.touched))
	for k := range d.touched {
		out = append(out, k)
	}
	d.touched = nil
	return out
}

func (d *ReadDetail) appendMiss(ev ReadMissEvent) {
	d.mu.Lock()
	if len(d.misses) < readDetailMaxMisses {
		d.misses = append(d.misses, ev)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.missesDropped.Add(1)
}

// fnvAddr / fnvSlot: FNV-1a over the raw key bytes — stable 64-bit identity
// for offline joins (re-reference distance, prefetch read-set diffs).
func fnvAddr(addr common.Address) uint64 {
	h := uint64(14695981039346656037)
	for _, b := range addr {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

func fnvSlot(addr common.Address, slot common.Hash) uint64 {
	h := uint64(14695981039346656037)
	for _, b := range addr {
		h = (h ^ uint64(b)) * 1099511628211
	}
	for _, b := range slot {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

// ReaderWithDetail is implemented by readers that support detail attachment.
// Callers type-assert from ReaderWithStats; nil detail detaches.
type ReaderWithDetail interface {
	SetReadDetail(*ReadDetail)
	GetReadDetail() *ReadDetail
}

// SetReadDetail attaches (or detaches, with nil) the detail collector.
func (r *readerWithCacheStats) SetReadDetail(d *ReadDetail) {
	r.detail.Store(d)
}

// GetReadDetail returns the attached detail collector, or nil.
func (r *readerWithCacheStats) GetReadDetail() *ReadDetail {
	return r.detail.Load()
}
