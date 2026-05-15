package relay

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	rejectionReportInterval     = time.Minute // how often to log the aggregated rejection summary
	maxRejectionCategories      = 50          // cap on distinct error strings tracked to avoid unbounded map growth
	rejectionOtherCategoryLabel = "<other>"   // overflow bucket once the cap is hit
)

// rejectionTracker aggregates BP rejection errors for a failed tx submission and
// flushes them periodically as a single summary log line. Grouping by error string
// collapses N identical rejections (e.g. "tx fee exceeds the configured cap" when a
// BP has a lower fee cap than the relay) into one line with a count.
type rejectionTracker struct {
	mu     sync.Mutex
	counts map[string]uint64
	total  uint64
}

// record adds an error to the tracker. Callers typically filter out "already known"
// error type while other RPC related errors reach here.
func (t *rejectionTracker) record(err error) {
	if err == nil {
		return
	}
	msg := normalizeRejectionMessage(err.Error())
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = make(map[string]uint64)
	}
	// Cardinality guard: if the map is already at cap and this is a brand-new error
	// string, drop it into the overflow bucket to preserve the total count without
	// letting the map grow unbounded.
	if _, seen := t.counts[msg]; !seen && len(t.counts) >= maxRejectionCategories {
		t.counts[rejectionOtherCategoryLabel]++
	} else {
		t.counts[msg]++
	}
	t.total++
}

// normalizeRejectionMessage strips the dynamic context (nonces, gas prices, balances)
// from a rejection error so semantically identical rejections collapse into one bucket.
// e.g. "nonce too low: next nonce 67693, tx nonce 67692" and "nonce too low: next nonce
// 80001, tx nonce 80000" both normalize to "nonce too low". Messages without a colon
// (plain sentinels, transport errors like "EOF") are returned unchanged.
func normalizeRejectionMessage(msg string) string {
	if i := strings.IndexByte(msg, ':'); i > 0 {
		return msg[:i]
	}
	return msg
}

// flush returns the accumulated counts and total, then resets the tracker.
func (t *rejectionTracker) flush() (uint64, map[string]uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	total, counts := t.total, t.counts
	t.total, t.counts = 0, nil
	return total, counts
}

// formatRejectionCounts renders the map as a single compact string, sorted by count
// desc so the most frequent error appears first. Entries with zero count are skipped.
//
// Example output: "nonce too low: 10, invalid sender: 3, pool full: 2"
func formatRejectionCounts(counts map[string]uint64) string {
	if len(counts) == 0 {
		return ""
	}
	type pair struct {
		msg string
		n   uint64
	}
	pairs := make([]pair, 0, len(counts))
	for msg, n := range counts {
		if n == 0 {
			continue
		}
		pairs = append(pairs, pair{msg, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].msg < pairs[j].msg
	})
	var sb strings.Builder
	for i, p := range pairs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s: %d", p.msg, p.n)
	}
	return sb.String()
}
