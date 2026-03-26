package miner

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// Top-K slow transaction tracking configuration.
	slowTxTopKSize     = 10
	slowTxWindowPeriod = 10 * time.Minute
)

// txTimingEntry records how long a single transaction took to apply during block building.
type txTimingEntry struct {
	hash     common.Hash
	duration time.Duration
}

type txTimingMinHeap []txTimingEntry

func (h txTimingMinHeap) Len() int { return len(h) }

func (h txTimingMinHeap) Less(i, j int) bool {
	return h[i].duration < h[j].duration
}

func (h txTimingMinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *txTimingMinHeap) Push(x interface{}) {
	*h = append(*h, x.(txTimingEntry))
}

func (h *txTimingMinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// slowTxTopTracker tracks the top K slowest txs using a bounded min-heap.
// Fast path rejects in O(1), accepted updates are O(log K).
type slowTxTopTracker struct {
	data txTimingMinHeap
}

func newSlowTxTopTracker() *slowTxTopTracker {
	return &slowTxTopTracker{
		data: make(txTimingMinHeap, 0, slowTxTopKSize),
	}
}

func (t *slowTxTopTracker) Add(entry txTimingEntry) {
	if len(t.data) < slowTxTopKSize {
		heap.Push(&t.data, entry)
		return
	}
	// O(1) reject for non-qualifying entries.
	if entry.duration <= t.data[0].duration {
		return
	}
	// Replace current minimum and restore heap.
	t.data[0] = entry
	heap.Fix(&t.data, 0)
}

func (t *slowTxTopTracker) SnapshotAndReset() []txTimingEntry {
	if len(t.data) == 0 {
		return nil
	}

	entries := make([]txTimingEntry, len(t.data))
	copy(entries, t.data)
	t.data = t.data[:0]

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].duration > entries[j].duration
	})

	return entries
}

func (t *slowTxTopTracker) Reset() {
	t.data = t.data[:0]
}

// formatSlowTxs returns a compact string of slow txs in order, e.g. "0xabc...(250ms) 0xdef...(100ms)".
func formatSlowTxs(entries []txTimingEntry) string {
	parts := make([]string, 0, len(entries))
	for i := range entries {
		parts = append(parts, fmt.Sprintf("%s(%s)", entries[i].hash.Hex(), common.PrettyDuration(entries[i].duration)))
	}
	return strings.Join(parts, " ")
}

func (w *worker) flushSlowTxWindow(windowEnd time.Time) {
	entries := w.slowTxTracker.SnapshotAndReset()
	if len(entries) == 0 {
		return
	}

	log.Warn("Slow transactions detected in the last 10 minutes",
		"windowStart", common.PrettyTime(windowEnd.Add(-slowTxWindowPeriod)),
		"windowEnd", common.PrettyTime(windowEnd),
		"slowTxCount", len(entries),
		"slowTxs", formatSlowTxs(entries),
	)
}
