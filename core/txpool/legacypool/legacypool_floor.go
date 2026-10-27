package legacypool

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// The base fee floor is producer policy, not consensus: block producers hold
// the base fee at a floor and let it rise above it with demand. At the floor
// the base fee repeats exactly block after block, which the EIP-1559 formula
// never does on its own (it only repeats when gas used equals the target), so
// every node can learn the floor from its own headers without being told.
const (
	// floorRunLength is how many consecutive equal base fees confirm a floor.
	floorRunLength = 32

	// floorWindow is how many recent canonical blocks are tracked. It bounds the
	// work on reorgs and gaps, and it is the fallback window when no floor is
	// known: the lowest base fee in it is used instead.
	floorWindow = 1200

	// floorLookback is how far back the startup scan looks for a floor, so a
	// restart during a long fee rise still finds it.
	floorLookback = 57_600
)

var (
	floorGauge          = metrics.NewRegisteredGauge("txpool/floor/basefee", nil)
	floorKnownGauge     = metrics.NewRegisteredGauge("txpool/floor/known", nil)
	floorConfirmMeter   = metrics.NewRegisteredMeter("txpool/floor/confirm", nil)
	floorBelowMeter     = metrics.NewRegisteredMeter("txpool/floor/below", nil)
	floorSweptMeter     = metrics.NewRegisteredMeter("txpool/floor/swept", nil)
	floorSweepableMeter = metrics.NewRegisteredMeter("txpool/floor/sweepable", nil)
)

// floorBlock is the part of a canonical header the floor tracker needs.
type floorBlock struct {
	number  uint64
	hash    common.Hash
	baseFee *big.Int
	gasUsed uint64
}

func newFloorBlock(h *types.Header) floorBlock {
	return floorBlock{number: h.Number.Uint64(), hash: h.Hash(), baseFee: h.BaseFee, gasUsed: h.GasUsed}
}

// headerFn returns the canonical header with the given hash and number, or nil.
type headerFn func(hash common.Hash, number uint64) *types.Header

// floorTracker learns the producers' base fee floor from canonical headers.
type floorTracker struct {
	recent []floorBlock // tracked canonical blocks, oldest first, at most floorWindow
	floor  *big.Int     // last confirmed floor, nil when none was seen
	lowest *big.Int     // lowest base fee in recent, the fallback without a floor

	run          int    // equal base fees in a row ending at the newest block
	runGasUsed   uint64 // gas used by the parent of the first block of the run
	runVaried    bool   // whether gas used changed inside the run
	runConfirmed bool   // whether the run already confirmed the floor
}

// push appends b, a child of the newest tracked block, and reports whether it
// confirmed a floor. A run only counts once gas used varies inside it: equal
// base fees with equal gas used can be the formula hitting its target exactly.
func (t *floorTracker) push(b floorBlock) bool {
	if n := len(t.recent); n > 0 {
		parent := t.recent[n-1]
		switch {
		case b.baseFee.Cmp(parent.baseFee) != 0:
			t.run = 0
		case t.run == 0:
			t.run, t.runGasUsed, t.runVaried, t.runConfirmed = 1, parent.gasUsed, false, false
		default:
			t.run++
			t.runVaried = t.runVaried || parent.gasUsed != t.runGasUsed
		}
	}
	t.recent = append(t.recent, b)
	if len(t.recent) > floorWindow {
		t.recent = t.recent[len(t.recent)-floorWindow:]
	}
	if t.run < floorRunLength || !t.runVaried || t.runConfirmed {
		return false
	}
	t.runConfirmed = true
	t.floor = new(big.Int).Set(b.baseFee)
	return true
}

// update moves the tracker to head, following reorgs and gaps through
// getHeader, and reports whether a floor was confirmed on the way.
func (t *floorTracker) update(head *types.Header, getHeader headerFn) bool {
	if head == nil || head.BaseFee == nil {
		return false
	}
	// Collect the new blocks back to a tracked ancestor, at most one window.
	var (
		fresh    []*types.Header
		ancestor = -1
	)
	for h := head; h != nil && h.BaseFee != nil && len(fresh) < floorWindow; {
		if ancestor = t.indexOf(h); ancestor >= 0 {
			break
		}
		fresh = append(fresh, h)
		if h.Number.Sign() == 0 {
			break
		}
		h = getHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	if ancestor != len(t.recent)-1 {
		// Reorg or gap: keep the common part and rebuild the run state from it.
		kept := t.recent[:ancestor+1]
		t.recent, t.run = nil, 0
		for _, b := range kept {
			t.push(b)
		}
	}
	confirmed := false
	for i := len(fresh) - 1; i >= 0; i-- {
		confirmed = t.push(newFloorBlock(fresh[i])) || confirmed
	}
	t.lowest = nil
	for _, b := range t.recent {
		if t.lowest == nil || b.baseFee.Cmp(t.lowest) < 0 {
			t.lowest = b.baseFee
		}
	}
	return confirmed
}

// indexOf returns the position of h in recent, or -1 when it is not tracked.
func (t *floorTracker) indexOf(h *types.Header) int {
	if len(t.recent) == 0 {
		return -1
	}
	first := t.recent[0].number
	number := h.Number.Uint64()
	if number < first || number-first >= uint64(len(t.recent)) {
		return -1
	}
	if idx := int(number - first); t.recent[idx].hash == h.Hash() {
		return idx
	}
	return -1
}

// init loads the recent window ending at head and, when it holds no floor,
// scans further back for the latest one.
func (t *floorTracker) init(head *types.Header, getHeader headerFn) {
	start := time.Now()
	t.update(head, getHeader)
	if t.floor == nil && len(t.recent) > 0 {
		oldest := t.recent[0]
		t.floor = scanFloor(getHeader, getHeader(oldest.hash, oldest.number), floorLookback)
	}
	log.Debug("Initialized base fee floor tracker", "floor", t.floor, "blocks", len(t.recent), "elapsed", time.Since(start))
}

// scanFloor walks back from h for at most limit blocks and returns the base
// fee of the newest confirmed run, or nil when there is none.
func scanFloor(getHeader headerFn, h *types.Header, limit int) *big.Int {
	var (
		run     int
		gasUsed uint64
		varied  bool
	)
	for i := 0; i < limit && h != nil && h.BaseFee != nil && h.Number.Sign() > 0; i++ {
		parent := getHeader(h.ParentHash, h.Number.Uint64()-1)
		if parent == nil || parent.BaseFee == nil {
			return nil
		}
		switch {
		case h.BaseFee.Cmp(parent.BaseFee) != 0:
			run = 0
		case run == 0:
			run, gasUsed, varied = 1, parent.GasUsed, false
		default:
			run++
			varied = varied || parent.GasUsed != gasUsed
		}
		if run >= floorRunLength && varied {
			return new(big.Int).Set(h.BaseFee)
		}
		h = parent
	}
	return nil
}

// threshold returns the lowest base fee a transaction must pay to be kept: the
// floor, or the head base fee when a producer goes below it (that is the price
// right now). Without a known floor it is the lowest base fee of the window.
func (t *floorTracker) threshold(head *types.Header) *big.Int {
	if t.floor != nil {
		if head != nil && head.BaseFee != nil && head.BaseFee.Cmp(t.floor) < 0 {
			return head.BaseFee
		}
		return t.floor
	}
	return t.lowest
}

// floorHeader reads a canonical header for the floor tracker.
func (pool *LegacyPool) floorHeader(hash common.Hash, number uint64) *types.Header {
	if hr, ok := pool.chain.(interface {
		GetHeader(common.Hash, uint64) *types.Header
	}); ok {
		return hr.GetHeader(hash, number)
	}
	if block := pool.chain.GetBlock(hash, number); block != nil {
		return block.Header()
	}
	return nil
}

// updateFloor moves the floor tracker to head and marks a sweep when a floor
// is confirmed.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) updateFloor(head *types.Header) {
	if pool.floor.update(head, pool.floorHeader) {
		floorConfirmMeter.Mark(1)
		pool.floorSweepDue = true
	}
	if pool.floor.floor != nil {
		floorGauge.Update(pool.floor.floor.Int64())
		floorKnownGauge.Update(1)
	} else {
		floorKnownGauge.Update(0)
	}
}

// floorLimit returns the lowest fee cap the pool keeps: the floor threshold
// plus the minimum tip, or nil when no base fee is known.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) floorLimit() (threshold, limit *big.Int) {
	threshold = pool.floor.threshold(pool.currentHead.Load())
	if threshold == nil {
		return nil, nil
	}
	return threshold, new(big.Int).Add(threshold, pool.gasTip.Load().ToBig())
}

// checkBaseFeeFloor refuses a transaction whose fee cap cannot pay the floor
// plus the minimum tip. With the floor disabled it only counts it.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) checkBaseFeeFloor(tx *types.Transaction) error {
	threshold, limit := pool.floorLimit()
	if limit == nil || tx.GasFeeCapIntCmp(limit) >= 0 {
		return nil
	}
	floorBelowMeter.Mark(1)
	if !pool.config.BaseFeeFloor {
		return nil
	}
	return fmt.Errorf("%w: fee cap %v below base fee floor %v plus minimum tip %v", txpool.ErrUnderpriced, tx.GasFeeCap(), threshold, pool.gasTip.Load())
}

// sweepBelowFloor drops every transaction that cannot pay a newly confirmed
// floor plus the minimum tip. Producers do not include them while they hold
// the floor, and the admission check keeps them out afterwards. With the floor
// disabled it only counts them.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) sweepBelowFloor() {
	_, limit := pool.floorLimit()
	if limit == nil {
		return
	}
	var hashes []common.Hash
	pool.all.Range(func(hash common.Hash, tx *types.Transaction) bool {
		if tx.GasFeeCapIntCmp(limit) < 0 {
			hashes = append(hashes, hash)
		}
		return true
	})
	if !pool.config.BaseFeeFloor {
		floorSweepableMeter.Mark(int64(len(hashes)))
		return
	}
	for _, hash := range hashes {
		pool.removeTx(hash, true, true)
	}
	floorSweptMeter.Mark(int64(len(hashes)))
	if len(hashes) > 0 {
		log.Info("Swept transactions below the base fee floor", "floor", pool.floor.floor, "limit", limit, "count", len(hashes))
	}
}
