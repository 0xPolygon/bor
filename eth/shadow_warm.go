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

import (
	"math"
	"math/big"
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
	warmWorkers = 4
	warmQueue   = 256
)

type shadowWarmer struct {
	eth *Ethereum
	ch  chan *types.Transaction
}

func newShadowWarmer(e *Ethereum) *shadowWarmer {
	w := &shadowWarmer{eth: e, ch: make(chan *types.Transaction, warmQueue)}
	for i := 0; i < warmWorkers; i++ {
		go w.loop()
	}

	return w
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
	if arm != "A" || !shadowtrace.XBWarm() {
		return
	}

	select {
	case w.ch <- tx:
	default:
		log.Info("SHADOW warm-dropped", "hash", tx.Hash().Hex(), "reason", "queue-full")
	}
}

func (w *shadowWarmer) loop() {
	for tx := range w.ch {
		w.warm(tx)
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
	head := bc.CurrentBlock()
	headNum := head.Number.Uint64()

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

// watchHeads logs every new chain head (end of import) for the trace.
func (s *Ethereum) shadowWatchHeads() {
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
				}
				shadowtrace.HeadSeen(h.Number.Uint64(), now, hashes, h.GasUsed, len(hashes))
			case <-sub.Err():
				return
			}
		}
	}()
}
