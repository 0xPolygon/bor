package core

// Per-transaction execution-time instrumentation for the *import* path (serial
// StateProcessor.Process). This mirrors the block-production slow-tx tracker in
// miner/slow_tx_tracker.go, but runs while the node executes/re-executes blocks
// during import. It exists to answer: as we natively accelerate the BN254
// getCollectionId ladder, which transactions are now the slowest — still the
// CTF split/merge/redeem calls, or has a different pattern surfaced?
//
// The tracker keeps the top-K slowest txs over a rolling window and emits one
// log.Warn line per window. Each entry carries the to-address and 4-byte
// selector so the window log can be classified without RPC round-trips.

import (
	"container/heap"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

const (
	importSlowTxTopKSize     = 10
	importSlowTxWindowPeriod = 10 * time.Minute
)

// importTxTimingEntry records how long a single transaction took to apply
// during block import, plus enough identity to classify it later.
type importTxTimingEntry struct {
	blockNumber uint64
	txIndex     int
	hash        common.Hash
	to          *common.Address
	selector    string // "0x12345678", or "" for txs with <4 bytes of input
	gasUsed     uint64
	duration    time.Duration
}

type importTxTimingMinHeap []importTxTimingEntry

func (h importTxTimingMinHeap) Len() int           { return len(h) }
func (h importTxTimingMinHeap) Less(i, j int) bool { return h[i].duration < h[j].duration }
func (h importTxTimingMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *importTxTimingMinHeap) Push(x interface{}) {
	*h = append(*h, x.(importTxTimingEntry))
}

func (h *importTxTimingMinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// importSlowTxTopTracker keeps the top-K slowest txs of the current window in a
// bounded min-heap (O(1) reject, O(log K) accept), and flushes on a rolling
// wall-clock window driven by block-import calls (no background goroutine).
type importSlowTxTopTracker struct {
	mu          sync.Mutex
	data        importTxTimingMinHeap
	windowStart time.Time // zero until the first recorded tx
}

var importSlowTxTracker = &importSlowTxTopTracker{
	data: make(importTxTimingMinHeap, 0, importSlowTxTopKSize),
}

// record adds one applied transaction's timing and, if the current window has
// elapsed, flushes the top-K as a single log line and starts a new window.
func (t *importSlowTxTopTracker) record(blockNumber uint64, txIndex int, tx *types.Transaction, gasUsed uint64, d time.Duration, now time.Time) {
	entry := importTxTimingEntry{
		blockNumber: blockNumber,
		txIndex:     txIndex,
		hash:        tx.Hash(),
		to:          tx.To(),
		selector:    selectorOf(tx),
		gasUsed:     gasUsed,
		duration:    d,
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.windowStart.IsZero() {
		t.windowStart = now
	}

	if len(t.data) < importSlowTxTopKSize {
		heap.Push(&t.data, entry)
	} else if entry.duration > t.data[0].duration {
		t.data[0] = entry
		heap.Fix(&t.data, 0)
	}

	if now.Sub(t.windowStart) >= importSlowTxWindowPeriod {
		t.flushLocked(now)
	}
}

// flushLocked emits the current window's top-K and resets. Caller holds t.mu.
func (t *importSlowTxTopTracker) flushLocked(now time.Time) {
	windowStart := t.windowStart
	t.windowStart = now

	if len(t.data) == 0 {
		return
	}

	entries := make([]importTxTimingEntry, len(t.data))
	copy(entries, t.data)
	t.data = t.data[:0]

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].duration > entries[j].duration
	})

	// One short line per entry: geth's log handler truncates a single very long
	// record, so we emit the window header plus one line per slow tx. Grep
	// "slow import tx" to collect entries; the windowEnd field groups them.
	log.Warn("Slowest import txs window",
		"windowStart", windowStart.Format("15:04:05"),
		"windowEnd", now.Format("15:04:05"),
		"window", common.PrettyDuration(now.Sub(windowStart)),
		"count", len(entries),
	)
	windowTag := now.Format("15:04:05")
	for i := range entries {
		to := "contract-creation"
		if entries[i].to != nil {
			to = entries[i].to.Hex()
		}
		sel := entries[i].selector
		if sel == "" {
			sel = "-"
		}
		log.Warn("slow import tx",
			"win", windowTag,
			"rank", i+1,
			"dur", common.PrettyDuration(entries[i].duration),
			"blk", entries[i].blockNumber,
			"idx", entries[i].txIndex,
			"to", to,
			"sel", sel,
			"gas", entries[i].gasUsed,
			"hash", entries[i].hash.Hex(),
		)
	}
}

// selectorOf returns the 4-byte function selector as "0x........", or "" when
// the tx carries fewer than 4 bytes of input (plain value transfer).
func selectorOf(tx *types.Transaction) string {
	d := tx.Data()
	if len(d) < 4 {
		return ""
	}
	return fmt.Sprintf("0x%x", d[:4])
}

