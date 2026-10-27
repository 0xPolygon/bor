package legacypool

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func floorGwei(v int64) *big.Int { return new(big.Int).Mul(big.NewInt(v), big.NewInt(params.GWei)) }

// floorChain is a canonical header chain the tracker can walk.
type floorChain struct {
	headers []*types.Header
	byHash  map[common.Hash]*types.Header
	salt    byte // makes a fork's blocks differ from the chain it forks off
}

func newFloorChain() *floorChain {
	return &floorChain{byHash: make(map[common.Hash]*types.Header)}
}

func (c *floorChain) header(hash common.Hash, number uint64) *types.Header {
	if h := c.byHash[hash]; h != nil && h.Number.Uint64() == number {
		return h
	}
	return nil
}

func (c *floorChain) head() *types.Header { return c.headers[len(c.headers)-1] }

// extend appends one block with the given base fee. The gas used varies from
// block to block, like real blocks do, unless gasUsed is set.
func (c *floorChain) extend(baseFee *big.Int, gasUsed ...uint64) *types.Header {
	h := &types.Header{Number: new(big.Int), Difficulty: common.Big0, GasLimit: 160_000_000, BaseFee: baseFee, Extra: []byte{c.salt, byte(len(c.headers)), byte(len(c.headers) >> 8)}}
	if n := len(c.headers); n > 0 {
		h.ParentHash = c.headers[n-1].Hash()
		h.Number.SetUint64(uint64(n))
	}
	h.GasUsed = 20_000_000 + uint64(len(c.headers)%7)*1_000_000
	if len(gasUsed) > 0 {
		h.GasUsed = gasUsed[0]
	}
	c.headers = append(c.headers, h)
	c.byHash[h.Hash()] = h
	return h
}

func (c *floorChain) extendN(n int, baseFee *big.Int) {
	for i := 0; i < n; i++ {
		c.extend(baseFee)
	}
}

// extendDrift appends n blocks whose base fee changes every block, like the
// EIP-1559 formula without a floor.
func (c *floorChain) extendDrift(n int, from int64) {
	for i := 0; i < n; i++ {
		c.extend(new(big.Int).Add(floorGwei(from), big.NewInt(int64(i)*1_000_003)))
	}
}

// follow feeds every new head to the tracker, as pool resets do, and returns
// the heights where a floor was confirmed.
func (c *floorChain) follow(t *floorTracker, from int) []int {
	var confirmed []int
	for i := from; i < len(c.headers); i++ {
		if t.update(c.headers[i], c.header) {
			confirmed = append(confirmed, i)
		}
	}
	return confirmed
}

func TestFloorTrackerConfirmsFlatRun(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendDrift(10, 240)
	c.extendN(floorRunLength+5, floorGwei(250))

	var tr floorTracker
	confirmed := c.follow(&tr, 0)

	// The first block at 250 is not flat yet; the next 32 make the run.
	if len(confirmed) != 1 || confirmed[0] != 10+floorRunLength {
		t.Fatalf("confirmed at %v, want [%d]", confirmed, 10+floorRunLength)
	}
	if tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("floor %v, want 250 gwei", tr.floor)
	}
}

func TestFloorTrackerNeedsVaryingGas(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendDrift(3, 240)
	// Equal fees with equal gas used is the formula sitting exactly on its target.
	for i := 0; i < 2*floorRunLength; i++ {
		c.extend(floorGwei(250), 104_000_000)
	}
	var tr floorTracker
	if confirmed := c.follow(&tr, 0); len(confirmed) != 0 || tr.floor != nil {
		t.Fatalf("constant gas confirmed a floor: %v %v", confirmed, tr.floor)
	}
}

func TestFloorTrackerMountain(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+1, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)
	start := len(c.headers)

	// Demand lifts the fee for a while, then it falls back to the floor.
	for _, fee := range []int64{252, 260, 280, 310, 290, 270, 255} {
		c.extend(floorGwei(fee))
		tr.update(c.head(), c.header)
		if got := tr.threshold(c.head()); got.Cmp(floorGwei(250)) != 0 {
			t.Fatalf("threshold %v during the rise, want the 250 gwei floor", got)
		}
	}
	c.extendN(floorRunLength+1, floorGwei(250))
	if confirmed := c.follow(&tr, start+7); len(confirmed) != 1 {
		t.Fatalf("return to the floor confirmed %d times, want once (sweep after the rise)", len(confirmed))
	}
	if tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("floor %v, want 250 gwei", tr.floor)
	}
}

func TestFloorTrackerRaise(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+1, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)
	start := len(c.headers)

	for _, fee := range []int64{262, 275, 289} {
		c.extend(floorGwei(fee))
	}
	c.extendN(floorRunLength, floorGwei(300))
	c.follow(&tr, start)
	if tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("floor moved to %v before the run was complete", tr.floor)
	}
	c.extend(floorGwei(300))
	if !tr.update(c.head(), c.header) || tr.floor.Cmp(floorGwei(300)) != 0 {
		t.Fatalf("floor %v after a full run at 300 gwei", tr.floor)
	}
}

func TestFloorTrackerFollowsDip(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+1, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)

	c.extend(floorGwei(238))
	tr.update(c.head(), c.header)
	if got := tr.threshold(c.head()); got.Cmp(floorGwei(238)) != 0 {
		t.Fatalf("threshold %v, want the 238 gwei head: the producer sets the price", got)
	}
}

func TestFloorTrackerFallback(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendDrift(50, 100)
	var tr floorTracker
	c.follow(&tr, 0)
	if tr.floor != nil {
		t.Fatalf("floor %v without any flat run", tr.floor)
	}
	if got := tr.threshold(c.head()); got.Cmp(floorGwei(100)) != 0 {
		t.Fatalf("fallback %v, want the window low of 100 gwei", got)
	}
	var empty floorTracker
	if got := empty.threshold(c.head()); got != nil {
		t.Fatalf("empty tracker threshold %v, want nil", got)
	}
}

func TestFloorTrackerReorg(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+10, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)

	// Replace the last three blocks with a side chain that leaves the floor.
	fork := newFloorChain()
	fork.salt = 1
	for _, h := range c.headers[:len(c.headers)-3] {
		fork.headers = append(fork.headers, h)
		fork.byHash[h.Hash()] = h
	}
	fork.extend(floorGwei(251))
	fork.extend(floorGwei(252))
	tr.update(fork.head(), fork.header)

	if n := len(tr.recent); n != len(fork.headers) || tr.recent[n-1].hash != fork.head().Hash() {
		t.Fatalf("tracker did not follow the reorg: %d blocks", n)
	}
	if tr.run != 0 {
		t.Fatalf("run %d after leaving the floor, want 0", tr.run)
	}
	if tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("floor %v, want 250 gwei kept across the reorg", tr.floor)
	}
}

// TestFloorTrackerReorgRecountsRun checks a reorg inside a run recounts it from
// the kept blocks instead of adding to the old count.
func TestFloorTrackerReorgRecountsRun(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(21, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)

	fork := newFloorChain()
	fork.salt = 1
	for _, h := range c.headers[:18] {
		fork.headers = append(fork.headers, h)
		fork.byHash[h.Hash()] = h
	}
	fork.extendN(3, floorGwei(250))
	tr.update(fork.head(), fork.header)

	if tr.run != 20 || tr.floor != nil {
		t.Fatalf("run %d floor %v after the reorg, want 20 and none", tr.run, tr.floor)
	}
	fork.extendN(floorRunLength-20, floorGwei(250))
	if confirmed := fork.follow(&tr, 21); len(confirmed) != 1 || confirmed[0] != len(fork.headers)-1 {
		t.Fatalf("confirmed at %v, want only at the 32nd flat block %d", confirmed, len(fork.headers)-1)
	}
}

func TestFloorTrackerGap(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+1, floorGwei(250))
	var tr floorTracker
	c.follow(&tr, 0)

	// The node jumps far ahead in one step, as during sync.
	c.extendDrift(floorWindow+500, 300)
	tr.update(c.head(), c.header)
	if len(tr.recent) != floorWindow || tr.recent[floorWindow-1].hash != c.head().Hash() {
		t.Fatalf("window not rebuilt after the gap: %d blocks", len(tr.recent))
	}
	if tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("floor %v, want 250 gwei kept across the gap", tr.floor)
	}
}

func TestFloorTrackerInitScansBack(t *testing.T) {
	t.Parallel()
	c := newFloorChain()
	c.extendN(floorRunLength+1, floorGwei(250))
	// A long rise: the window alone holds no flat run.
	c.extendDrift(floorWindow+100, 260)

	var tr floorTracker
	tr.init(c.head(), c.header)
	if tr.floor == nil || tr.floor.Cmp(floorGwei(250)) != 0 {
		t.Fatalf("startup scan found floor %v, want 250 gwei", tr.floor)
	}

	var none floorTracker
	drift := newFloorChain()
	drift.extendDrift(100, 200)
	none.init(drift.head(), drift.header)
	if none.floor != nil {
		t.Fatalf("startup scan invented floor %v", none.floor)
	}
}

// setupFloorPool returns an EIP-1559 pool with the mainnet minimum tip and the
// 250 gwei floor already learned, the head at the floor.
func setupFloorPool(t *testing.T, enforce bool) *LegacyPool {
	t.Helper()
	pool, _ := setupPoolWithConfig(eip1559Config, func(pool *LegacyPool) {
		pool.config.BaseFeeFloor = enforce
	})
	t.Cleanup(func() { pool.Close() })
	pool.gasTip.Store(uint256.NewInt(params.BorDefaultTxPoolPriceLimit))

	pool.mu.Lock()
	pool.floor.floor = floorGwei(250)
	pool.mu.Unlock()
	setStrandedBaseFee(pool, floorGwei(250))
	return pool
}

func TestBaseFeeFloorAdmission(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, true)
	key, _ := fundedKey(t, pool)
	tip := floorGwei(30)

	// Floor 250 + minimum tip 25: 274 cannot be mined, 275 can.
	err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, floorGwei(274), tip, key))
	if !errors.Is(err, txpool.ErrUnderpriced) {
		t.Fatalf("fee cap 274 gwei: err %v, want ErrUnderpriced", err)
	}
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, floorGwei(275), tip, key)); err != nil {
		t.Fatalf("fee cap 275 gwei refused: %v", err)
	}
	// Legacy transactions pay their gas price as both cap and tip.
	if err := pool.addRemoteSync(pricedTransaction(1, 100_000, floorGwei(260), key)); !errors.Is(err, txpool.ErrUnderpriced) {
		t.Fatalf("legacy 260 gwei: err %v, want ErrUnderpriced", err)
	}
}

func TestBaseFeeFloorAdmissionFollowsDip(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, true)
	setStrandedBaseFee(pool, floorGwei(200))
	key, _ := fundedKey(t, pool)

	// A producer below the floor sets the price right now: 225 can be mined.
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, floorGwei(225), floorGwei(25), key)); err != nil {
		t.Fatalf("fee cap 225 gwei refused during a dip to 200: %v", err)
	}
}

func TestBaseFeeFloorShadowAdmission(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, false)
	key, _ := fundedKey(t, pool)

	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, floorGwei(100), floorGwei(30), key)); err != nil {
		t.Fatalf("disabled floor refused a transaction: %v", err)
	}
}

// addBelowFloorChain adds nonces 0..2 from key, nonce 1 below the floor.
func addBelowFloorChain(t *testing.T, pool *LegacyPool) common.Address {
	t.Helper()
	key, addr := fundedKey(t, pool)

	// Add before the floor is known, as during a dip.
	pool.mu.Lock()
	learned := pool.floor.floor
	pool.floor.floor = nil
	pool.mu.Unlock()

	tip := floorGwei(30)
	txs := []*types.Transaction{
		dynamicFeeTx(0, 100_000, floorGwei(400), tip, key),
		dynamicFeeTx(1, 100_000, floorGwei(240), tip, key),
		dynamicFeeTx(2, 100_000, floorGwei(400), tip, key),
	}
	for _, err := range pool.addRemotesSync(txs) {
		if err != nil {
			t.Fatalf("failed to add chain: %v", err)
		}
	}
	pool.mu.Lock()
	pool.floor.floor = learned
	pool.mu.Unlock()
	return addr
}

func TestBaseFeeFloorSweep(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, true)
	addr := addBelowFloorChain(t, pool)

	// A confirmed floor sweeps on the next reorg run.
	pool.mu.Lock()
	pool.floorSweepDue = true
	pool.mu.Unlock()
	<-pool.requestReset(nil, nil)

	pending, queued := accountCounts(pool, addr)
	if pending != 1 || queued != 1 {
		t.Fatalf("pending %d queued %d, want 1 and 1: nonce 1 swept, nonce 2 waits for a replacement", pending, queued)
	}
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if pool.floorSweepDue {
		t.Fatal("sweep still due after the reorg run")
	}
}

func TestBaseFeeFloorShadowSweep(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, false)
	addr := addBelowFloorChain(t, pool)

	pool.mu.Lock()
	pool.sweepBelowFloor()
	pool.mu.Unlock()

	if pending, queued := accountCounts(pool, addr); pending != 3 || queued != 0 {
		t.Fatalf("disabled floor swept: pending %d queued %d", pending, queued)
	}
}

// TestBaseFeeFloorStrandedDuringRise checks the stranded eviction does not
// drop transactions that pay the floor while demand lifts the fee above it.
func TestBaseFeeFloorStrandedDuringRise(t *testing.T) {
	t.Parallel()
	pool := setupFloorPool(t, true)
	pool.config.StrandedLifetime = time.Millisecond
	key, addr := fundedKey(t, pool)
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, floorGwei(300), floorGwei(30), key)); err != nil {
		t.Fatalf("failed to add: %v", err)
	}
	setStrandedBaseFee(pool, floorGwei(400))

	pool.mu.Lock()
	now := time.Now()
	pool.evictStranded(now)
	pool.evictStranded(now.Add(time.Hour))
	pool.mu.Unlock()

	if pending, _ := accountCounts(pool, addr); pending != 1 {
		t.Fatalf("pending %d, want 1: a 300 gwei cap pays the 250 floor once the rise ends", pending)
	}
}
