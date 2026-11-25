//go:build lls_shadow

package eth

// A/B early warm for the LLS shadow experiment (cross-block prefetch test).
//
// Every pool tx with gas limit >= heavygas is logged on arrival with a fixed
// A/B arm (last bit of its hash). When xbwarm=on, arm-A txs are executed once
// against the current head state, right when they enter the pool. The run is
// thrown away: it only fills the node-wide caches (snapshot / trie / pebble /
// OS page cache) that the next build reads through, never the per-build L1.
// Arm B is the control. Comparing the builder's apply time of A vs B heavy
// txs answers "does warming before the build help heavy txs?".
//
// P5 additions:
//   - warm admission: for every heavy tx, log whether the next block could
//     take it (fee cap >= next base fee + miner min tip). With warmadmit=on,
//     arm-A txs that fail are not warmed.
//   - flood: with flood=N, N synthetic jobs per second go into the SAME
//     worker queue. Each job reads many accounts (sampled from recent blocks)
//     on a fresh head StateDB. Nothing is sent or added to the txpool; it only
//     measures what a full warm queue costs the builder and the real warms.

import (
	"math"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
)

const (
	warmWorkers      = 4
	warmQueue        = 256
	ringSize         = 1 << 20 // addresses sampled from recent blocks (flood test)
	floodReadsPerJob = 1500
)

type warmJob struct {
	tx    *types.Transaction
	synth bool
}

type shadowWarmer struct {
	eth *Ethereum
	ch  chan warmJob

	ringMu  sync.Mutex
	ring    []common.Address
	ringPos int

	floodDone  atomic.Int64
	floodNanos atomic.Int64
	floodReads atomic.Int64
}

func newShadowWarmer(e *Ethereum) *shadowWarmer {
	w := &shadowWarmer{eth: e, ch: make(chan warmJob, warmQueue)}
	for i := 0; i < warmWorkers; i++ {
		go w.loop()
	}

	go w.floodLoop()

	return w
}

// nextHeader returns the head and a header for the next block on it.
func (w *shadowWarmer) nextHeader() (*types.Header, *types.Header) {
	bc := w.eth.blockchain
	head := bc.CurrentBlock()
	cfg := bc.Config()

	header := &types.Header{
		ParentHash: head.Hash(),
		Number:     new(big.Int).Add(head.Number, common.Big1),
		Time:       head.Time + 2,
		GasLimit:   head.GasLimit,
		Difficulty: head.Difficulty,
		Coinbase:   head.Coinbase,
	}
	if cfg.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(cfg, head)
	}

	return head, header
}

func gwei(v *big.Int) float64 {
	if v == nil {
		return 0
	}

	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), big.NewFloat(1e9)).Float64()

	return f
}

// admit decides whether the next block could take tx: the builder's own
// fee filter (fee cap >= next base fee, effective tip >= miner min tip).
func (w *shadowWarmer) admit(tx *types.Transaction, from common.Address) (bool, string, float64, float64, float64, int64) {
	head, header := w.nextHeader()

	minTip := w.eth.config.Miner.GasPrice
	if minTip == nil {
		minTip = big.NewInt(0)
	}

	var nonceGap int64 = -1
	if st, err := w.eth.blockchain.StateAt(head.Root); err == nil {
		nonceGap = int64(tx.Nonce()) - int64(st.GetNonce(from))
	}

	feeCap := gwei(tx.GasFeeCap())
	base := gwei(header.BaseFee)

	if header.BaseFee != nil && tx.GasFeeCap().Cmp(header.BaseFee) < 0 {
		return false, "feecap<base", feeCap, base, 0, nonceGap
	}

	tip, err := tx.EffectiveGasTip(header.BaseFee)
	if err != nil {
		return false, "tip-error", feeCap, base, 0, nonceGap
	}

	if tip.Cmp(minTip) < 0 {
		return false, "tip<min", feeCap, base, gwei(tip), nonceGap
	}

	return true, "ok", feeCap, base, gwei(tip), nonceGap
}

// onPoolTx logs heavy arrivals and queues arm-A ones for warming.
func (w *shadowWarmer) onPoolTx(tx *types.Transaction, at time.Time) {
	if tx.Gas() < shadowtrace.HeavyGas() {
		return
	}

	from, _ := types.Sender(types.LatestSigner(w.eth.blockchain.Config()), tx)

	to := ""
	if tx.To() != nil {
		to = tx.To().Hex()
	}

	sel := ""
	if d := tx.Data(); len(d) >= 4 {
		sel = hexutil.Encode(d[:4])
	}

	arm := shadowtrace.HeavySeen(tx.Hash(), at, tx.Gas(), from.Hex(), to, sel)

	ok, reason, feeCap, base, tip, gap := w.admit(tx, from)
	enforce := shadowtrace.WarmAdmit()
	shadowtrace.WarmAdmitLog(tx.Hash(), arm, ok, reason, feeCap, base, tip, gap, enforce)

	if arm != "A" || !shadowtrace.XBWarm() {
		return
	}

	if enforce && !ok {
		return
	}

	select {
	case w.ch <- warmJob{tx: tx}:
		shadowtrace.WarmQueued(tx.Hash())
	default:
		log.Info("SHADOW warm-dropped", "hash", tx.Hash().Hex(), "reason", "queue-full")
	}
}

func (w *shadowWarmer) loop() {
	for j := range w.ch {
		if j.synth {
			w.synthetic()
		} else {
			w.warm(j.tx)
		}
	}
}

// addAddrs feeds the flood ring from a new block.
func (w *shadowWarmer) addAddrs(b *types.Block) {
	signer := types.LatestSigner(w.eth.blockchain.Config())

	w.ringMu.Lock()
	defer w.ringMu.Unlock()

	add := func(a common.Address) {
		if len(w.ring) < ringSize {
			w.ring = append(w.ring, a)
			return
		}

		w.ring[w.ringPos] = a
		w.ringPos = (w.ringPos + 1) % ringSize
	}

	for _, tx := range b.Transactions() {
		if from, err := types.Sender(signer, tx); err == nil {
			add(from)
		}

		if tx.To() != nil {
			add(*tx.To())
		}
	}
}

func (w *shadowWarmer) sample(n int) []common.Address {
	w.ringMu.Lock()
	defer w.ringMu.Unlock()

	if len(w.ring) == 0 {
		return nil
	}

	out := make([]common.Address, n)
	for i := range out {
		out[i] = w.ring[rand.Intn(len(w.ring))]
	}

	return out
}

// synthetic is one flood job: read many accounts on a fresh head StateDB.
func (w *shadowWarmer) synthetic() {
	start := time.Now()
	head := w.eth.blockchain.CurrentBlock()

	st, err := w.eth.blockchain.StateAt(head.Root)
	if err != nil {
		return
	}

	addrs := w.sample(floodReadsPerJob)
	for _, a := range addrs {
		st.GetBalance(a)
		st.GetCodeSize(a)
	}

	w.floodDone.Add(1)
	w.floodNanos.Add(int64(time.Since(start)))
	w.floodReads.Add(int64(len(addrs)))
}

// floodLoop submits FloodRate() synthetic jobs per second into the shared
// queue and logs one line per second while the flood is on.
func (w *shadowWarmer) floodLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()

	for range t.C {
		rate := shadowtrace.FloodRate()
		if rate == 0 {
			continue
		}

		submitted, dropped := 0, 0
		for i := 0; i < rate; i++ {
			select {
			case w.ch <- warmJob{synth: true}:
				submitted++
			default:
				dropped++
			}
		}

		done := w.floodDone.Swap(0)
		nanos := w.floodNanos.Swap(0)
		reads := w.floodReads.Swap(0)

		var meanMs, meanReads float64
		if done > 0 {
			meanMs = float64(nanos) / float64(done) / 1e6
			meanReads = float64(reads) / float64(done)
		}

		w.ringMu.Lock()
		ring := len(w.ring)
		w.ringMu.Unlock()

		shadowtrace.FloodTick(rate, submitted, dropped, int(done), meanMs, meanReads, ring)
	}
}

// warm runs an arm-A heavy tx twice on the head state, each time on a fresh
// StateDB (so run 2 reads through the node caches exactly like the builder
// would after a perfect early warm, not from run 1's state objects):
//
//	run 1 = cold: the tx's data comes from disk; this is the early warm
//	run 2 = fully warm: the same reads, now served from memory
//
// run 1 vs run 2 is the "how fast if fully prefetched" gain of this tx. Both
// gas values are logged so the analysis keeps only txs whose state did not
// change (gas of both runs == the builder's gasUsed).
func (w *shadowWarmer) warm(tx *types.Transaction) {
	start := shadowtrace.WarmStart(tx.Hash())
	bc := w.eth.blockchain
	head, header := w.nextHeader()
	headNum := head.Number.Uint64()
	cfg := bc.Config()

	signer := types.MakeSigner(cfg, header.Number, header.Time)

	msg, err := core.TransactionToMessage(tx, signer, header.BaseFee)
	if err != nil {
		shadowtrace.WarmEnd(tx.Hash(), start, headNum, "msg-error: "+err.Error(), 0, 0)
		return
	}

	// A warm run must never fail on fee/nonce checks: it only exists to
	// touch the state the tx reads.
	msg.SkipNonceChecks = true
	msg.SkipTransactionChecks = true

	run := func() (string, uint64, time.Duration) {
		st, err := bc.StateAt(head.Root)
		if err != nil {
			return "state-error: " + err.Error(), 0, 0
		}

		st.SetTxContext(tx.Hash(), 0)
		evm := vm.NewEVM(core.NewEVMBlockContext(header, bc, &header.Coinbase), st, cfg, vm.Config{NoBaseFee: true})
		gp := new(core.GasPool).AddGas(math.MaxUint64)

		res, err := core.ApplyMessage(evm, msg, gp)
		reads := st.AccountReads + st.StorageReads

		switch {
		case err != nil:
			return "error: " + err.Error(), 0, reads
		case res.Failed():
			return "reverted", res.UsedGas, reads
		default:
			return "completed", res.UsedGas, reads
		}
	}

	out1, gas1, reads1 := run()
	shadowtrace.WarmEnd(tx.Hash(), start, headNum, out1, gas1, reads1)

	start2 := time.Now()
	out2, gas2, reads2 := run()
	shadowtrace.WarmSecond(tx.Hash(), headNum, out1, gas1, start2.Sub(start), reads1, out2, gas2, time.Since(start2), reads2)
}

// watchHeads logs every new chain head (end of import) for the trace and
// feeds the flood address ring.
func (s *Ethereum) shadowWatchHeads(w *shadowWarmer) {
	ch := make(chan core.ChainHeadEvent, 64)
	sub := s.blockchain.SubscribeChainHeadEvent(ch)

	go func() {
		defer sub.Unsubscribe()

		for {
			select {
			case ev := <-ch:
				now := time.Now()
				h := ev.Header
				var hashes []common.Hash
				if b := s.blockchain.GetBlock(h.Hash(), h.Number.Uint64()); b != nil {
					for _, tx := range b.Transactions() {
						hashes = append(hashes, tx.Hash())
					}
					if w != nil {
						w.addAddrs(b)
					}
				}
				shadowtrace.HeadSeen(h.Number.Uint64(), now, hashes, h.GasUsed, len(hashes))
			case <-sub.Err():
				return
			}
		}
	}()
}
