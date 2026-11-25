package shadowtrace

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ApplyInfo is everything the builder knows about one apply of a tx.
type ApplyInfo struct {
	Start      time.Time
	Dur        time.Duration
	Prefetched bool // in prefetchedTxHashes at apply time
	GasUsed    uint64
	Result     string // ok / reverted / error
	Err        error

	From     common.Address
	To       *common.Address
	Selector string
	TipGwei  float64
	GasLimit uint64

	Index     int    // position in the block (env.tcount before the apply)
	GasBefore uint64 // block gas used before this tx
	GasLeft   uint64 // gas pool left before this tx

	L1Hit, L1Miss   int64         // per-build reader cache, this tx only
	HitFromPrefetch int64         // L1 hits on entries the prefetcher inserted
	ReadDur         time.Duration // StateDB account+storage read time, this tx only
}
