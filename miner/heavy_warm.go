package miner

import (
	"container/heap"
	"math/big"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// Heavy-tx warmer.
//
// A heavy transaction (gas limit >= HeavyWarmGas) whose state is cold can
// spend hundreds of milliseconds waiting on disk reads when the builder
// applies it, and run into the build deadline. The warmer executes each
// heavy pool transaction once, as soon as it reaches the pool, on a
// throwaway StateDB at the chain head. The run result is discarded; the
// point is that the node-wide caches (pathdb clean cache, code cache,
// pebble and OS page cache) now hold the state the transaction reads, so
// the builder's later apply is served from memory.
//
// The builder side is shouldDefer: while a transaction's warm is running
// (and has run for less than HeavyWarmDeferMax), the builder skips it for
// the current pass and retries on a later pass. A transaction whose warm is
// only queued, was dropped, or ran too long is applied immediately. The
// builder only defers under continuous building (a sequencer refreshing the
// block from the pool, see worker.heavyDeferAllowed), so a deferred
// transaction normally lands later in the same block; one deferred near the
// end of the block moves to the next block, where it is applied warm.
// Skipping a tx also skips its sender's later nonces for that pass, like any
// other Pop.
//
// Warms only run while the worker is mining, and admission is re-checked
// against the current base fee right before each run.

const (
	// heavyWarmMaxRun cancels a warm run that takes longer than this. The
	// builder stops waiting after HeavyWarmDeferMax anyway; the cap only
	// returns the worker to the queue.
	heavyWarmMaxRun = 2 * time.Second

	heavyWarmTxChanSize = 4096
)

var (
	heavyWarmSeenMeter         = metrics.NewRegisteredMeter("worker/heavywarm/seen", nil)
	heavyWarmQueuedMeter       = metrics.NewRegisteredMeter("worker/heavywarm/queued", nil)
	heavyWarmDropFeeMeter      = metrics.NewRegisteredMeter("worker/heavywarm/dropped/fee", nil)
	heavyWarmDropSenderMeter   = metrics.NewRegisteredMeter("worker/heavywarm/dropped/sender", nil)
	heavyWarmDropFullMeter     = metrics.NewRegisteredMeter("worker/heavywarm/dropped/full", nil)
	heavyWarmEvictedMeter      = metrics.NewRegisteredMeter("worker/heavywarm/dropped/evicted", nil)
	heavyWarmGoneMeter         = metrics.NewRegisteredMeter("worker/heavywarm/skipped/gone", nil)
	heavyWarmErrorMeter        = metrics.NewRegisteredMeter("worker/heavywarm/error", nil)
	heavyWarmCancelledMeter    = metrics.NewRegisteredMeter("worker/heavywarm/cancelled", nil)
	heavyWarmRunTimer          = metrics.NewRegisteredTimer("worker/heavywarm/run", nil)
	heavyWarmQueueWaitTimer    = metrics.NewRegisteredTimer("worker/heavywarm/queuewait", nil)
	heavyWarmQueueGauge        = metrics.NewRegisteredGauge("worker/heavywarm/queue", nil)
	heavyWarmDeferredMeter     = metrics.NewRegisteredMeter("worker/heavywarm/deferred", nil)
	heavyWarmColdAfterMaxMeter = metrics.NewRegisteredMeter("worker/heavywarm/coldaftermax", nil)
)

// warmChain is the part of the blockchain the warmer reads.
type warmChain interface {
	core.ChainContext
	CurrentBlock() *types.Header
	StateAt(root common.Hash) (*state.StateDB, error)
}

// warmPool is the part of the txpool the warmer reads.
type warmPool interface {
	Has(hash common.Hash) bool
	SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription
}

// heavyWarmConfig holds the sanitized warmer settings.
type heavyWarmConfig struct {
	gas       uint64        // minimum gas limit of a heavy tx
	workers   int           // concurrent warm runs
	queue     int           // max queued (not yet running) warms
	perSender int           // max queued + running warms per sender
	deferMax  time.Duration // longest the builder waits on a running warm
	maxRun    time.Duration // a warm run is cancelled after this
}

func heavyWarmConfigFrom(c *Config) heavyWarmConfig {
	hc := heavyWarmConfig{
		gas:       c.HeavyWarmGas,
		workers:   c.HeavyWarmWorkers,
		queue:     c.HeavyWarmQueue,
		perSender: c.HeavyWarmPerSender,
		deferMax:  c.HeavyWarmDeferMax,
		maxRun:    heavyWarmMaxRun,
	}
	if hc.gas == 0 {
		hc.gas = DefaultConfig.HeavyWarmGas
	}
	if hc.workers <= 0 {
		hc.workers = DefaultConfig.HeavyWarmWorkers
	}
	if hc.queue <= 0 {
		hc.queue = DefaultConfig.HeavyWarmQueue
	}
	if hc.perSender <= 0 {
		hc.perSender = DefaultConfig.HeavyWarmPerSender
	}
	if hc.deferMax < 0 {
		hc.deferMax = 0
	}
	return hc
}

// warmItem is one queued warm.
type warmItem struct {
	tx       *types.Transaction
	from     common.Address
	tip      *uint256.Int // effective tip at the next base fee: queue priority
	seq      uint64       // arrival order, tie-break for equal tips
	queuedAt time.Time
	index    int // position in warmHeap
}

// warmHeap is a max-heap on (tip, -seq): highest tip first, then oldest.
type warmHeap []*warmItem

func (h warmHeap) Len() int { return len(h) }

func (h warmHeap) Less(i, j int) bool {
	if c := h[i].tip.Cmp(h[j].tip); c != 0 {
		return c > 0
	}
	return h[i].seq < h[j].seq
}

func (h warmHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *warmHeap) Push(x any) {
	it := x.(*warmItem)
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *warmHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	it.index = -1
	*h = old[:n-1]
	return it
}

// heavyWarmer warms heavy pool transactions on arrival. A nil *heavyWarmer is
// valid and disabled: shouldDefer always returns false.
type heavyWarmer struct {
	cfg         heavyWarmConfig
	chainConfig *params.ChainConfig
	chain       warmChain
	pool        warmPool
	minTip      func() *uint256.Int
	active      func() bool // warm only while true (the worker is mining); nil = always

	mu      sync.Mutex
	cond    *sync.Cond
	closed  bool
	queue   warmHeap
	queued  map[common.Hash]*warmItem
	running map[common.Hash]time.Time
	senders map[common.Address]int // queued + running per sender
	seq     uint64

	txsCh chan core.NewTxsEvent
	sub   event.Subscription
	quit  chan struct{}
	wg    sync.WaitGroup

	onWarmed func(common.Hash) // test hook, called after each warm attempt
}

func newHeavyWarmer(cfg heavyWarmConfig, chainConfig *params.ChainConfig, chain warmChain, pool warmPool, minTip func() *uint256.Int, active func() bool) *heavyWarmer {
	w := &heavyWarmer{
		cfg:         cfg,
		chainConfig: chainConfig,
		chain:       chain,
		pool:        pool,
		minTip:      minTip,
		active:      active,
		queued:      make(map[common.Hash]*warmItem),
		running:     make(map[common.Hash]time.Time),
		senders:     make(map[common.Address]int),
		quit:        make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// start subscribes to new pool transactions and starts the workers.
func (w *heavyWarmer) start() {
	w.txsCh = make(chan core.NewTxsEvent, heavyWarmTxChanSize)
	w.sub = w.pool.SubscribeTransactions(w.txsCh, false)

	w.wg.Add(1 + w.cfg.workers)
	go w.eventLoop()
	for i := 0; i < w.cfg.workers; i++ {
		go w.workLoop()
	}
	log.Info("Heavy-tx warmer started", "gas", w.cfg.gas, "workers", w.cfg.workers,
		"queue", w.cfg.queue, "perSender", w.cfg.perSender, "deferMax", w.cfg.deferMax)
}

// stop ends the subscription and the workers. Running warms finish (each is
// bounded by maxRun).
func (w *heavyWarmer) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()

	close(w.quit)
	w.wg.Wait()
}

func (w *heavyWarmer) eventLoop() {
	defer w.wg.Done()
	defer w.sub.Unsubscribe()

	for {
		select {
		case ev := <-w.txsCh:
			w.offerAll(ev.Txs)
		case <-w.sub.Err():
			return
		case <-w.quit:
			return
		}
	}
}

// nextHeader returns the chain head and a header for the block after it,
// used for the admission fee check and the warm run's block context.
func (w *heavyWarmer) nextHeader() (*types.Header, *types.Header) {
	head := w.chain.CurrentBlock()
	header := &types.Header{
		ParentHash: head.Hash(),
		Number:     new(big.Int).Add(head.Number, common.Big1),
		Time:       head.Time + 1,
		GasLimit:   head.GasLimit,
		Difficulty: head.Difficulty,
		Coinbase:   head.Coinbase,
	}
	if w.chainConfig.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(w.chainConfig, head)
	}
	return head, header
}

// offerAll queues the heavy transactions of one pool event.
func (w *heavyWarmer) offerAll(txs []*types.Transaction) {
	var (
		baseFee *big.Int
		signer  types.Signer
		minTip  *uint256.Int
		ready   bool
	)
	if shadowtrace.HWOff() || (w.active != nil && !w.active()) {
		return
	}
	for _, tx := range txs {
		if tx.Gas() < w.cfg.gas {
			continue
		}
		shadowHeavySeen(tx)
		if !ready {
			_, header := w.nextHeader()
			baseFee = header.BaseFee
			signer = types.MakeSigner(w.chainConfig, header.Number, header.Time)
			minTip = w.minTip()
			ready = true
		}
		w.offer(tx, baseFee, signer, minTip)
	}
}

// offer applies admission and queues tx for a warm.
func (w *heavyWarmer) offer(tx *types.Transaction, baseFee *big.Int, signer types.Signer, minTip *uint256.Int) {
	heavyWarmSeenMeter.Mark(1)

	// Admission: the next block could take the tx (same fee rule as the
	// builder's pending filter). Anything else is not worth a warm now.
	if baseFee != nil && tx.GasFeeCapIntCmp(baseFee) < 0 {
		heavyWarmDropFeeMeter.Mark(1)
		return
	}
	tipBig, err := tx.EffectiveGasTip(baseFee)
	if err != nil {
		heavyWarmDropFeeMeter.Mark(1)
		return
	}
	tip, overflow := uint256.FromBig(tipBig)
	if overflow || (minTip != nil && tip.Lt(minTip)) {
		heavyWarmDropFeeMeter.Mark(1)
		return
	}
	from, err := types.Sender(signer, tx)
	if err != nil {
		heavyWarmDropFeeMeter.Mark(1)
		return
	}

	hash := tx.Hash()

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return
	}
	if _, ok := w.queued[hash]; ok {
		return
	}
	if _, ok := w.running[hash]; ok {
		return
	}
	if w.senders[from] >= w.cfg.perSender {
		heavyWarmDropSenderMeter.Mark(1)
		return
	}
	if len(w.queue) >= w.cfg.queue {
		// Full: evict the lowest-priority item only if tx pays more.
		low := w.lowest()
		if low == nil || low.tip.Cmp(tip) >= 0 {
			heavyWarmDropFullMeter.Mark(1)
			return
		}
		heap.Remove(&w.queue, low.index)
		delete(w.queued, low.tx.Hash())
		w.releaseSender(low.from)
		heavyWarmEvictedMeter.Mark(1)
	}

	w.seq++
	it := &warmItem{tx: tx, from: from, tip: tip, seq: w.seq, queuedAt: time.Now()}
	heap.Push(&w.queue, it)
	w.queued[hash] = it
	w.senders[from]++
	heavyWarmQueuedMeter.Mark(1)
	heavyWarmQueueGauge.Update(int64(len(w.queue)))
	shadowtrace.WarmQueued(hash)
	w.cond.Signal()
}

// lowest returns the queued item with the lowest priority. Linear scan over
// a small bounded queue; called only when the queue is full. Caller holds mu.
func (w *heavyWarmer) lowest() *warmItem {
	var low *warmItem
	for _, it := range w.queue {
		if low == nil || w.queue.Less(low.index, it.index) {
			low = it
		}
	}
	return low
}

// releaseSender drops one queued/running slot of from. Caller holds mu.
func (w *heavyWarmer) releaseSender(from common.Address) {
	if n := w.senders[from]; n <= 1 {
		delete(w.senders, from)
	} else {
		w.senders[from] = n - 1
	}
}

func (w *heavyWarmer) workLoop() {
	defer w.wg.Done()

	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.cond.Wait()
		}
		if w.closed {
			w.mu.Unlock()
			return
		}
		it := heap.Pop(&w.queue).(*warmItem)
		hash := it.tx.Hash()
		delete(w.queued, hash)
		w.running[hash] = time.Now()
		heavyWarmQueueGauge.Update(int64(len(w.queue)))
		w.mu.Unlock()

		heavyWarmQueueWaitTimer.UpdateSince(it.queuedAt)
		w.warm(it.tx)

		w.mu.Lock()
		delete(w.running, hash)
		w.releaseSender(it.from)
		w.mu.Unlock()

		if w.onWarmed != nil {
			w.onWarmed(hash)
		}
	}
}

// warm executes tx once on a fresh StateDB at the head and discards the
// result. Only the caches it fills matter.
func (w *heavyWarmer) warm(tx *types.Transaction) {
	defer func() {
		if r := recover(); r != nil {
			heavyWarmErrorMeter.Mark(1)
			log.Error("Heavy-tx warm panicked", "hash", tx.Hash(), "err", r)
		}
	}()

	// Already included or evicted: nothing left to warm for.
	if !w.pool.Has(tx.Hash()) {
		heavyWarmGoneMeter.Mark(1)
		return
	}

	start := shadowtrace.WarmStart(tx.Hash())
	if start.IsZero() {
		start = time.Now()
	}
	head, header := w.nextHeader()
	outcome := "error"
	var (
		warmGas uint64
		reads   time.Duration
	)
	defer func() {
		shadowtrace.WarmEnd(tx.Hash(), start, head.Number.Uint64(), outcome, warmGas, reads)
	}()

	// The base fee may have risen since admission: a tx the next block
	// cannot take is not worth the disk reads now.
	if header.BaseFee != nil && tx.GasFeeCapIntCmp(header.BaseFee) < 0 {
		heavyWarmDropFeeMeter.Mark(1)
		return
	}

	st, err := w.chain.StateAt(head.Root)
	if err != nil {
		heavyWarmErrorMeter.Mark(1)
		return
	}
	signer := types.MakeSigner(w.chainConfig, header.Number, header.Time)
	msg, err := core.TransactionToMessage(tx, signer, header.BaseFee)
	if err != nil {
		heavyWarmErrorMeter.Mark(1)
		return
	}
	// A warm must touch the state the tx reads, never fail on fee or nonce
	// checks (the pool may hold earlier nonces of the same sender).
	msg.SkipNonceChecks = true
	msg.SkipTransactionChecks = true

	st.SetTxContext(tx.Hash(), 0)
	evm := vm.NewEVM(core.NewEVMBlockContext(header, w.chain, &header.Coinbase), st, w.chainConfig, vm.Config{NoBaseFee: true})
	timer := time.AfterFunc(w.cfg.maxRun, evm.Cancel)
	defer timer.Stop()

	res, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(header.GasLimit))
	heavyWarmRunTimer.UpdateSince(start)
	reads = st.AccountReads + st.StorageReads
	switch {
	case evm.Cancelled():
		outcome = "cancelled"
	case err != nil:
		outcome = "error: " + err.Error()
	case res.Failed():
		outcome, warmGas = "reverted", res.UsedGas
	default:
		outcome, warmGas = "completed", res.UsedGas
	}

	switch {
	case evm.Cancelled():
		heavyWarmCancelledMeter.Mark(1)
	case err != nil:
		// Consensus-level failure (e.g. balance too low for the gas). The
		// reads done before it still warmed the caches.
		heavyWarmErrorMeter.Mark(1)
	}
}

// runningFor reports whether hash has a warm running now, and for how long.
func (w *heavyWarmer) runningFor(hash common.Hash) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	started, ok := w.running[hash]
	if !ok {
		return 0, false
	}
	return time.Since(started), true
}

// shouldDefer is the builder's late-tx rule: skip a heavy tx for this pass
// while its warm is running and has run for less than deferMax. A queued,
// dropped, finished or over-long warm never delays the tx.
func (w *heavyWarmer) shouldDefer(tx *types.Transaction) bool {
	if w == nil || w.cfg.deferMax == 0 || tx.Gas() < w.cfg.gas || shadowtrace.HWOff() {
		return false
	}
	ran, ok := w.runningFor(tx.Hash())
	if !ok {
		return false
	}
	if ran < w.cfg.deferMax {
		heavyWarmDeferredMeter.Mark(1)
		return true
	}
	heavyWarmColdAfterMaxMeter.Mark(1)
	return false
}

// shadowHeavySeen creates the trace record of a heavy tx (idempotent with the
// shadow arrival logger) so the warm hooks below attach to it.
func shadowHeavySeen(tx *types.Transaction) {
	if !shadowtrace.Enabled() {
		return
	}
	from, _ := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	to := ""
	if tx.To() != nil {
		to = tx.To().Hex()
	}
	sel := ""
	if d := tx.Data(); len(d) >= 4 {
		sel = hexutil.Encode(d[:4])
	}
	shadowtrace.HeavySeen(tx.Hash(), time.Now(), tx.Gas(), from.Hex(), to, sel)
}
