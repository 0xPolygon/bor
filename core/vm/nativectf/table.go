package nativectf

import (
	"bytes"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// ladderTables caches the per-code ladder analysis keyed by code hash, mirroring
// bor's jumpdest-analysis caching. A nil/empty map means "no ladders in this code"
// and is cached too, so a contract is analyzed at most once.
var ladderTables sync.Map // common.Hash -> map[uint64]LadderMeta

// LadderTableFor returns the ladder table for the given code, building and caching
// it on first use. It is gated by a cheap whole-code pre-filter: code that does not
// even contain the universal ladder body marker gets an empty table without the
// (relatively expensive) full JUMPDEST scan. Callers must only invoke this when the
// native-CTF feature is enabled, so disabled nodes do no work at all.
func LadderTableFor(codeHash common.Hash, code []byte) map[uint64]LadderMeta {
	if v, ok := ladderTables.Load(codeHash); ok {
		return v.(map[uint64]LadderMeta)
	}
	table := buildIfMarked(code)
	// Only cache for real, content-addressed code (non-zero hash); transient
	// initcode with a zero hash is built fresh each time (matches jumpdest cache).
	if codeHash != (common.Hash{}) {
		actual, _ := ladderTables.LoadOrStore(codeHash, table)
		return actual.(map[uint64]LadderMeta)
	}
	return table
}

// buildIfMarked runs the full scan only when the cheap marker pre-filter hits.
func buildIfMarked(code []byte) map[uint64]LadderMeta {
	if !bytes.Contains(code, universalBodyMarker) {
		return map[uint64]LadderMeta{} // non-nil empty: "analyzed, no ladders"
	}
	if t := BuildLadderTable(code); t != nil {
		return t
	}
	return map[uint64]LadderMeta{}
}
