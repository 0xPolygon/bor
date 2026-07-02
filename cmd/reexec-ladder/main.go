// Command reexec-ladder re-executes a range of historical bor blocks through the
// EVM in a strictly serial, single-frame-at-a-time manner (GOMAXPROCS=1) with the
// native-CTF ladder instrumentation in shadow mode.
//
// Purpose: measure the BN254 modular-sqrt ladder's share of EVM interpreter
// execution time (interp_ns / total_interp_ns) under CLEAN serial conditions,
// where wall-clock ~= CPU time so the ratio is meaningful. The original 9.8%/11.1%
// figures were gathered under bor's parallel EVM, where a long pure-CPU ladder
// block and an I/O-mixed depth==1 frame inflate their wall clocks by different
// factors, corrupting the ratio.
//
// It re-executes each block's user transactions against the parent's state
// (reconstructed via pathdb state history, requires the range to be within
// history.state of the DB head) and reports, in aggregate and per-block:
//   share A = interp_ns / total_interp_ns   (ladder % of EVM interpreter time)
//   share B = interp_ns / process_ns        (ladder % of serial block-processing time)
//   savings = (interp_ns - native_ns) / total_interp_ns
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

// chainCtx is a minimal core.ChainContext: enough for NewEVMBlockContext (GetHeader
// backs the BLOCKHASH opcode). We always pass an explicit author, so Engine() is
// never invoked and can be nil.
type chainCtx struct {
	db  ethdb.Database
	cfg *params.ChainConfig
}

func (c *chainCtx) Engine() consensus.Engine    { return nil }
func (c *chainCtx) Config() *params.ChainConfig  { return c.cfg }
func (c *chainCtx) CurrentHeader() *types.Header { return nil }
func (c *chainCtx) GetTd(common.Hash, uint64) *big.Int { return nil }
func (c *chainCtx) GetHeader(h common.Hash, n uint64) *types.Header {
	return rawdb.ReadHeader(c.db, h, n)
}
func (c *chainCtx) GetHeaderByNumber(n uint64) *types.Header {
	h := rawdb.ReadCanonicalHash(c.db, n)
	if h == (common.Hash{}) {
		return nil
	}
	return rawdb.ReadHeader(c.db, h, n)
}
func (c *chainCtx) GetHeaderByHash(h common.Hash) *types.Header {
	n, ok := rawdb.ReadHeaderNumber(c.db, h)
	if !ok {
		return nil
	}
	return rawdb.ReadHeader(c.db, h, n)
}

func main() {
	var (
		datadir = flag.String("chaindata", "/var/lib/bor/data/bor/chaindata", "path to bor chaindata dir")
		start   = flag.Uint64("start", 0, "start block (inclusive)")
		end     = flag.Uint64("end", 0, "end block (inclusive)")
		stride  = flag.Uint64("stride", 1, "process every Nth block in [start,end]")
		outCSV  = flag.String("out", "", "optional per-block CSV output path")
		info    = flag.Bool("info", false, "print DB head block and exit")
	)
	flag.Parse()

	// Strictly serial: one execution goroutine, wall-clock ~= CPU time.
	runtime.GOMAXPROCS(1)
	debug.SetGCPercent(400) // reduce GC-pause noise in the wall timers

	kv, err := pebble.New(*datadir, 2048, 524288, "", true)
	if err != nil {
		fatal("open pebble: %v", err)
	}
	db, err := rawdb.Open(kv, rawdb.OpenOptions{
		Ancient:  filepath.Join(*datadir, "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		fatal("rawdb open: %v", err)
	}
	defer db.Close()

	headHash := rawdb.ReadHeadHeaderHash(db)
	headNum, ok := rawdb.ReadHeaderNumber(db, headHash)
	if !ok {
		fatal("cannot read head header number")
	}
	fmt.Printf("DB head block: %d\n", headNum)
	if *info {
		return
	}
	if *end == 0 || *end > headNum {
		*end = headNum
	}
	if *start == 0 {
		fatal("must pass -start (and optionally -end); head is %d", headNum)
	}

	genesisHash := rawdb.ReadCanonicalHash(db, 0)
	cfg := rawdb.ReadChainConfig(db, genesisHash)
	if cfg == nil {
		fatal("no chain config in DB")
	}
	fmt.Printf("chain id %v, London=%v, Bor=%v\n", cfg.ChainID, cfg.LondonBlock, cfg.Bor != nil)

	tdb := triedb.NewDatabase(db, &triedb.Config{PathDB: pathdb.ReadOnly})
	defer tdb.Close()
	sdb := state.NewDatabase(tdb, nil)
	cc := &chainCtx{db: db, cfg: cfg}
	vmcfg := vm.Config{NativeCTF: vm.NativeCTFShadow}

	var w *csv.Writer
	if *outCSV != "" {
		f, err := os.Create(*outCSV)
		if err != nil {
			fatal("create csv: %v", err)
		}
		defer f.Close()
		w = csv.NewWriter(f)
		defer w.Flush()
		w.Write([]string{"block", "ntx", "gas_used", "hdr_gas", "interp_ns", "native_ns", "total_interp_ns", "process_ns", "ladders_delta"})
	}

	var (
		nBlocks, nSkipped, nTxErr int64
		sumProcNs                 int64
		prev                      = vm.LadderMetricsSnapshot()
		startWall                 = time.Now()
	)

	for n := *start; n <= *end; n += *stride {
		hash := rawdb.ReadCanonicalHash(db, n)
		if hash == (common.Hash{}) {
			nSkipped++
			continue
		}
		block := rawdb.ReadBlock(db, hash, n)
		if block == nil {
			nSkipped++
			continue
		}
		parent := cc.GetHeaderByNumber(n - 1)
		if parent == nil {
			nSkipped++
			continue
		}
		statedb, err := state.New(parent.Root, sdb)
		if err != nil {
			// state root not reconstructable (outside history.state window)
			nSkipped++
			if nSkipped <= 3 {
				fmt.Printf("  skip block %d: state.New(%x): %v\n", n, parent.Root[:6], err)
			}
			continue
		}

		author := block.Coinbase()
		blockCtx := core.NewEVMBlockContext(block.Header(), cc, &author)
		evm := vm.NewEVM(blockCtx, statedb, cfg, vmcfg)
		gp := new(core.GasPool).AddGas(block.GasLimit())
		var usedGas uint64

		t0 := time.Now()
		for i, tx := range block.Transactions() {
			statedb.SetTxContext(tx.Hash(), i)
			if _, err := core.ApplyTransaction(evm, gp, statedb, block.Header(), tx, &usedGas); err != nil {
				nTxErr++
			}
		}
		statedb.IntermediateRoot(cfg.IsEIP158(block.Number()))
		procNs := time.Since(t0).Nanoseconds()
		sumProcNs += procNs
		nBlocks++

		if w != nil {
			cur := vm.LadderMetricsSnapshot()
			w.Write([]string{
				strconv.FormatUint(n, 10),
				strconv.Itoa(len(block.Transactions())),
				strconv.FormatUint(usedGas, 10),
				strconv.FormatUint(block.GasUsed(), 10),
				strconv.FormatInt(cur.InterpNs-prev.InterpNs, 10),
				strconv.FormatInt(cur.NativeNs-prev.NativeNs, 10),
				strconv.FormatInt(cur.TotalNs-prev.TotalNs, 10),
				strconv.FormatInt(procNs, 10),
				strconv.FormatInt((cur.Match+cur.Mismatch)-(prev.Match+prev.Mismatch), 10),
			})
			prev = cur
		}

		if nBlocks%500 == 0 {
			cur := vm.LadderMetricsSnapshot()
			shareA := ratio(cur.InterpNs, cur.TotalNs)
			fmt.Printf("  %d blocks (head-%d..%d), shareA=%.3f%% interp_ns=%d total=%d skipped=%d elapsed=%s\n",
				nBlocks, *start, n, shareA*100, cur.InterpNs, cur.TotalNs, nSkipped, time.Since(startWall).Round(time.Second))
		}
	}

	m := vm.LadderMetricsSnapshot()
	fmt.Println("\n================ RESULT (serial, GOMAXPROCS=1) ================")
	fmt.Printf("range: %d..%d stride=%d  blocks_processed=%d skipped=%d tx_errors=%d\n", *start, *end, *stride, nBlocks, nSkipped, nTxErr)
	fmt.Printf("shadow: match=%d mismatch=%d active=%d\n", m.Match, m.Mismatch, m.Active)
	fmt.Printf("ladders (match+mismatch): %d   (~%.1f per processed block)\n", m.Match+m.Mismatch, float64(m.Match+m.Mismatch)/float64(max64(nBlocks, 1)))
	fmt.Printf("interp_ns       = %d  (%.3f s)\n", m.InterpNs, float64(m.InterpNs)/1e9)
	fmt.Printf("native_ns       = %d  (%.3f s)\n", m.NativeNs, float64(m.NativeNs)/1e9)
	fmt.Printf("total_interp_ns = %d  (%.3f s)   [denominator A: EVM interp time]\n", m.TotalNs, float64(m.TotalNs)/1e9)
	fmt.Printf("process_ns(sum) = %d  (%.3f s)   [denominator B: serial block-processing time]\n", sumProcNs, float64(sumProcNs)/1e9)
	fmt.Println("--------------------------------------------------------------")
	fmt.Printf("SHARE A  interp_ns / total_interp_ns = %.4f%%   <-- ladder %% of EVM interpreter time (headline)\n", ratio(m.InterpNs, m.TotalNs)*100)
	fmt.Printf("SHARE B  interp_ns / process_ns      = %.4f%%   <-- ladder %% of serial block-processing time\n", ratio(m.InterpNs, sumProcNs)*100)
	fmt.Printf("SAVINGS  (interp-native)/total       = %.4f%%   <-- EVM interp time native eliminates\n", ratio(m.InterpNs-m.NativeNs, m.TotalNs)*100)
	if m.Match+m.Mismatch > 0 {
		fmt.Printf("per-ladder interp = %.0f ns   native = %.0f ns   speedup = %.2fx\n",
			float64(m.InterpNs)/float64(m.Match+m.Mismatch), float64(m.NativeNs)/float64(m.Match+m.Mismatch),
			float64(m.InterpNs)/float64(max64(m.NativeNs, 1)))
	}
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func fatal(f string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+f+"\n", args...)
	os.Exit(1)
}
