package filters

import (
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	// Estimated memory retained per buffered event on top of its payload, kept
	// deliberately conservative as events are shared between filters.
	logOverhead = 192

	// A hash costs its payload plus the spare capacity of the slice it is appended to,
	// which stays below twice the elements in use.
	hashOverhead = 2 * common.HashLength

	// A transaction retains its fixed fields, which are allocated separately by the decoder,
	// plus the slices it builds for authorizations and access tuples.
	txOverhead = 512
)

// reserve claims a slot for a filter that is about to be created, so concurrent
// creations cannot exceed the limits.
func (api *FilterAPI) reserve() error {
	api.filtersMu.Lock()
	defer api.filtersMu.Unlock()

	if len(api.filters)+api.reserved >= api.maxFilters {
		return errTooManyFilters
	}

	if api.bufferedBytes >= api.maxBufferBytes {
		return errFilterBudgetExhausted
	}

	api.reserved++

	return nil
}

// unreserve gives back a slot claimed by reserve when the filter was not created.
func (api *FilterAPI) unreserve() {
	api.filtersMu.Lock()
	api.reserved--
	api.filtersMu.Unlock()
}

// install registers a filter in the slot claimed by reserve.
func (api *FilterAPI) install(id rpc.ID, f *filter) {
	api.filtersMu.Lock()
	api.reserved--
	api.filters[id] = f
	api.filtersMu.Unlock()
}

// removeLocked deletes a filter and releases its buffered events. The caller must
// hold filtersMu and unsubscribe the filter outside of it.
func (api *FilterAPI) removeLocked(id rpc.ID) *filter {
	f, found := api.filters[id]
	if !found {
		return nil
	}

	api.bufferedBytes -= f.size
	delete(api.filters, id)

	return f
}

// removeFailed deletes a filter whose subscription ended. A filter that overflowed is
// kept until its client polls or it times out, so that the client learns what happened.
func (api *FilterAPI) removeFailed(id rpc.ID) {
	api.filtersMu.Lock()
	defer api.filtersMu.Unlock()

	if f, found := api.filters[id]; found && !f.overflowed {
		api.removeLocked(id)
	}
}

// enqueue buffers a batch of events for a polling filter through add. If the batch
// would exceed the filter's item limit or the shared memory budget, nothing is buffered,
// the filter's buffer is released and the filter is marked as overflowed. It reports
// whether the filter just overflowed, in which case the caller must unsubscribe it.
func (api *FilterAPI) enqueue(id rpc.ID, items int, size uint64, add func(*filter)) bool {
	api.filtersMu.Lock()
	defer api.filtersMu.Unlock()

	f, found := api.filters[id]
	if !found || f.overflowed {
		return false
	}

	if len(f.logs)+len(f.hashes)+len(f.txs)+items > api.maxPendingItems || api.bufferedBytes+size > api.maxBufferBytes {
		log.Debug("Polling filter buffer overflow", "id", id)

		api.bufferedBytes -= f.size
		f.size, f.logs, f.hashes, f.txs, f.overflowed = 0, nil, nil, nil, true

		return true
	}

	add(f)

	f.size += size
	api.bufferedBytes += size

	return false
}

func logsSize(logs []*types.Log) (size uint64) {
	for _, l := range logs {
		size += logOverhead + uint64(len(l.Data)+common.HashLength*len(l.Topics))
	}

	return size
}

func txsSize(txs []*types.Transaction) (size uint64) {
	for _, tx := range txs {
		size += txSize(tx)
	}

	return size
}

// sliceSlots returns the number of elements the decoder may allocate for a slice of n:
// at least 4, and growth of 1.5x.
func sliceSlots(n int) uint64 {
	if n == 0 {
		return 0
	}

	return uint64(max(4, n+n/2))
}

// txSize estimates the memory retained by a transaction: its encoded size plus the in-memory
// size of the slices decoding allocates, including their spare capacity.
func txSize(tx *types.Transaction) uint64 {
	size := txOverhead + tx.Size()
	size += uint64(unsafe.Sizeof(types.SetCodeAuthorization{})) * sliceSlots(len(tx.SetCodeAuthorizations()))

	accessList := tx.AccessList()
	size += uint64(unsafe.Sizeof(types.AccessTuple{})) * sliceSlots(len(accessList))

	for _, tuple := range accessList {
		size += common.HashLength * sliceSlots(len(tuple.StorageKeys))
	}

	return size
}
