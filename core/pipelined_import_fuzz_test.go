package core

import (
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/trie"
)

// Differential test for pipelined import: a random chain is imported by a
// serial reference node and by pipelined and parallel nodes, which must all
// accept it and agree on receipts and post-state. Under pipelined import,
// block N+1 executes against the last committed root plus block N's FlatDiff
// overlay, so the generator builds blocks that read what the previous block
// created, destroyed, re-funded, delegated or cleared.
//
// Long run: PIPELINED_FUZZ_DURATION=5m go test ./core -run TestPipelinedImportDifferential -v
// One seed: PIPELINED_FUZZ_SEED=<n> go test ./core -run TestPipelinedImportDifferential -v

// pifStateProbe renders every account and slot the generator can touch.
func pifStateProbe(sdb *state.StateDB, err error, created []common.Address) string {
	if err != nil {
		return "state unavailable: " + err.Error()
	}
	addrs := append([]common.Address{pifCoinbase, pifBurnt, pifStore, pifExec, pifFactory}, created...)
	for i := range pifSenders {
		addrs = append(addrs, pifSender(i))
	}
	for i := range 4 {
		addrs = append(addrs, pifFresh(i))
	}
	for k := range 3 {
		for s := range pifSalts {
			addrs = append(addrs, pifChild(k, s))
		}
	}
	var b strings.Builder
	for _, a := range addrs {
		fmt.Fprintf(&b, "%x exist=%v bal=%s nonce=%d code=%x", a[:4], sdb.Exist(a), sdb.GetBalance(a), sdb.GetNonce(a), sdb.GetCodeHash(a).Bytes()[:4])
		for _, slot := range []int64{0, 1, 2, 3, 4, 5, 6, 7, 1000, 1001, 1002} {
			if v := sdb.GetState(a, common.BigToHash(big.NewInt(slot))); v != (common.Hash{}) {
				fmt.Fprintf(&b, " [%d]=%x", slot, v.Bytes())
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type pifNodeResult struct {
	name      string
	imported  int
	err       error
	head      common.Hash
	receipts  string
	state     string // read via StateAt (FlatDiff overlay on pipelined nodes)
	committed string // read from the committed state, bypassing the overlay
}

// pifStoredReceipts renders each block's stored receipts root and log
// positions, and collects created contract addresses: top-level creations
// from the receipt, inner CREATE/CREATE2 results from the logged topics
// (values above 2^64 that fit in 20 bytes; extra non-accounts are harmless).
func pifStoredReceipts(bc *BlockChain, blocks []*types.Block) (string, []common.Address) {
	var (
		b       strings.Builder
		created []common.Address
	)
	for _, blk := range blocks {
		rs := bc.GetReceiptsByHash(blk.Hash())
		fmt.Fprintf(&b, "%d:%x", blk.NumberU64(), types.DeriveSha(rs, trie.NewStackTrie(nil)).Bytes()[:6])
		for _, rc := range rs {
			if rc.ContractAddress != (common.Address{}) {
				created = append(created, rc.ContractAddress)
			}
			for _, l := range rc.Logs {
				fmt.Fprintf(&b, " %d/%d", l.Index, l.TxIndex)
				for _, topic := range l.Topics {
					if v := topic.Big(); v.BitLen() > 64 && v.BitLen() <= 160 {
						created = append(created, common.BytesToAddress(topic.Bytes()))
					}
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), created
}

func pifImport(t *testing.T, gspec *Genesis, blocks []*types.Block, name, scheme string, pipelined, parallel bool, r *rand.Rand) pifNodeResult {
	cfg := DefaultConfig().WithStateScheme(scheme)
	cfg.EnablePipelinedImportSRC = pipelined
	cfg.PipelinedSRCWarmSnapshot = pipelined && r.Intn(2) == 0
	var (
		bc  *BlockChain
		err error
	)
	if parallel {
		// Enforced, so a parallel failure is not hidden by the serial result.
		bc, err = NewParallelBlockChain(rawdb.NewMemoryDatabase(), gspec, beacon.New(ethash.NewFaker()), cfg, 4, true)
	} else {
		bc, err = NewBlockChain(rawdb.NewMemoryDatabase(), gspec, beacon.New(ethash.NewFaker()), cfg)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer bc.Stop()

	res := pifNodeResult{name: name}
	// Either one batch or several InsertChain calls (pipelining across calls).
	for done := 0; done < len(blocks) && res.err == nil; {
		n := len(blocks) - done
		if r.Intn(2) == 0 {
			n = 1 + r.Intn(n)
		}
		var i int
		i, res.err = bc.InsertChain(blocks[done:done+n], false)
		res.imported = done + i
		done += n
	}
	if res.err == nil {
		res.imported = len(blocks)
	}
	// Settle the last block's pending state root before reading, then read both
	// through StateAt and from the committed state alone.
	if err := bc.flushPendingImportSRC(true); err != nil && res.err == nil {
		res.err = err
	}
	var created []common.Address
	res.receipts, created = pifStoredReceipts(bc, blocks)
	root := bc.CurrentBlock().Root
	res.head = bc.CurrentBlock().Hash()
	viaStateAt, err := bc.StateAt(root)
	res.state = pifStateProbe(viaStateAt, err, created)
	committed, err := state.New(root, bc.statedb)
	res.committed = pifStateProbe(committed, err, created)
	return res
}

// pifCheckSeed returns the number of generated txs.
func pifCheckSeed(t *testing.T, seed int64) int {
	metrics.Enable() // idempotent
	var (
		mismatches     = pipelineImportRootMismatchCounter.Snapshot().Count()
		parallelErrors = blockExecutionParallelErrorCounter.Snapshot().Count()
		parallelBlocks = blockExecutionParallelCounter.Snapshot().Count()
	)
	r := rand.New(rand.NewSource(seed))
	gspec, blocks := pifGenerate(r)

	ref := pifImport(t, gspec, blocks, "serial", rawdb.HashScheme, false, false, r)
	if ref.err != nil {
		t.Fatalf("seed %d: reference rejected block %d: %v", seed, ref.imported+1, ref.err)
	}
	for _, n := range []pifNodeResult{
		pifImport(t, gspec, blocks, "parallel-path", rawdb.PathScheme, false, true, r),
		pifImport(t, gspec, blocks, "pipelined-hash", rawdb.HashScheme, true, false, r),
		pifImport(t, gspec, blocks, "pipelined-path", rawdb.PathScheme, true, false, r),
		pifImport(t, gspec, blocks, "pipelined-parallel-path", rawdb.PathScheme, true, true, r),
	} {
		switch {
		case n.err != nil:
			t.Errorf("seed %d: %s rejected block %d: %v", seed, n.name, n.imported+1, n.err)
		case n.head != ref.head:
			t.Errorf("seed %d: %s head %x, want %x", seed, n.name, n.head, ref.head)
		case n.receipts != ref.receipts:
			t.Errorf("seed %d: %s stored receipts differ\nwant:\n%s\ngot:\n%s", seed, n.name, ref.receipts, n.receipts)
		case n.state != ref.state:
			t.Errorf("seed %d: %s head state differs\nwant:\n%s\ngot:\n%s", seed, n.name, ref.state, n.state)
		case n.committed != ref.state:
			t.Errorf("seed %d: %s committed head state differs\nwant:\n%s\ngot:\n%s", seed, n.name, ref.state, n.committed)
		}
	}
	if d := blockExecutionParallelErrorCounter.Snapshot().Count() - parallelErrors; d > 0 {
		t.Errorf("seed %d: parallel processor failed %d times", seed, d)
	}
	if d := blockExecutionParallelCounter.Snapshot().Count() - parallelBlocks; d < int64(2*len(blocks)) {
		t.Errorf("seed %d: parallel processor ran %d blocks, want %d", seed, d, 2*len(blocks))
	}
	// A wrong pipelined root is rolled back and re-executed, so it can hide
	// behind a successful import.
	if d := pipelineImportRootMismatchCounter.Snapshot().Count() - mismatches; d > 0 {
		t.Errorf("seed %d: pipelined state root mismatch count +%d", seed, d)
	}
	txs := 0
	for _, blk := range blocks {
		txs += len(blk.Transactions())
	}
	return txs
}

func TestPipelinedImportDifferential(t *testing.T) {
	if s := os.Getenv("PIPELINED_FUZZ_SEED"); s != "" {
		seed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		pifCheckSeed(t, seed)
		return
	}
	metrics.Enable()
	pipelined := pipelineImportBlocksCounter.Snapshot().Count()
	seeds := int64(40)
	if testing.Short() {
		seeds = 8
	}
	deadline := time.Time{}
	if s := os.Getenv("PIPELINED_FUZZ_DURATION"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(d)
	}
	txs := 0
	for seed := int64(1); seed <= seeds || time.Now().Before(deadline); seed++ {
		txs += pifCheckSeed(t, seed)
		if t.Failed() {
			return
		}
	}
	// Guard against a generator change that silently stops producing work.
	if txs == 0 || pipelineImportBlocksCounter.Snapshot().Count() == pipelined {
		t.Fatalf("no coverage: txs=%d, pipelined blocks=%d", txs, pipelineImportBlocksCounter.Snapshot().Count()-pipelined)
	}
}

func FuzzPipelinedImportDifferential(f *testing.F) {
	f.Add(int64(1))
	f.Fuzz(func(t *testing.T, seed int64) { pifCheckSeed(t, seed) })
}
