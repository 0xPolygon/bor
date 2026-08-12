package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

// witnessRegenRoundTrip replays a block through the production V2 processor
// with witness recording enabled — over the production shared-reader stack,
// with the block author pre-warmed into the shared cache the way a flat
// reader serves it — then proves the regenerated witness is equivalent to
// the original: a stateless replay from each must produce identical gas,
// receipt root and state root.
func witnessRegenRoundTrip(pb *preparedBlock, diskdb ethdb.Database, config *params.ChainConfig, engine consensus.Engine) error {
	// Reference replay from the original witness.
	refState, refReceipt, refRes, err := executeStatelessSerial(config, pb.block, pb.witness, &pb.author, engine, diskdb)
	if err != nil {
		return fmt.Errorf("replay from original witness: %w", err)
	}

	// Production-shaped reader stack over the witness-backed state. The
	// prefetch-role read warms the author into the shared cache so fee
	// credits during settle are cache hits, mirroring mainnet where the flat
	// reader serves hot accounts without touching the trie.
	db := state.NewDatabase(pb.tdb, nil)
	prefetchReader, _, parallelReader, err := db.ReadersWithCacheStatsTriple(pb.witness.Root())
	if err != nil {
		return fmt.Errorf("readers: %w", err)
	}
	if _, err := prefetchReader.Account(pb.author); err != nil {
		return fmt.Errorf("warm author: %w", err)
	}
	finalDB, err := state.NewWithReader(pb.witness.Root(), db, parallelReader)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}

	hc := &benchHeaderChain{config: config, chainDb: pb.memdb, headerCache: pb.headerCache, engine: engine}
	w2, err := stateless.NewWitness(pb.block.Header(), hc)
	if err != nil {
		return fmt.Errorf("new witness: %w", err)
	}
	w2.Headers = append([]*types.Header{}, pb.witness.Headers...)
	finalDB.SetWitness(w2)

	// Same construction as processV2, on the reader-stacked statedb.
	bc := &BlockChain{
		chainConfig: config,
		hc:          &HeaderChain{config: config, chainDb: pb.memdb, headerCache: pb.headerCache, engine: engine},
	}
	res, err := NewV2StateProcessor(hc, bc, 4).Process(pb.block, finalDB, benchVMConfig, &pb.author, context.Background())
	if err != nil {
		return fmt.Errorf("v2 process: %w", err)
	}
	if got, want := types.DeriveSha(res.Receipts, trie.NewStackTrie(nil)), refReceipt; got != want {
		return fmt.Errorf("v2 execution diverged from serial: receipt root %x, want %x", got, want)
	}
	// Production always computes the post-state root on the V2 statedb
	// (ProcessBlock → ValidateState → IntermediateRoot); that is also where
	// update- and deletion-path trie nodes get recorded into the witness.
	if got := finalDB.IntermediateRoot(config.IsEIP158(pb.block.Number())); got != refState {
		return fmt.Errorf("v2 execution diverged from serial: state root %x, want %x", got, refState)
	}

	// The regenerated witness must be equivalent to the original.
	gotState, gotReceipt, gotRes, err := executeStatelessSerial(config, pb.block, w2, &pb.author, engine, diskdb)
	if err != nil {
		return fmt.Errorf("replay from regenerated witness: %w", err)
	}
	if gotRes.GasUsed != refRes.GasUsed {
		return fmt.Errorf("regenerated witness incomplete: gas %d, want %d", gotRes.GasUsed, refRes.GasUsed)
	}
	if gotReceipt != refReceipt {
		return fmt.Errorf("regenerated witness incomplete: receipt root %x, want %x", gotReceipt, refReceipt)
	}
	if gotState != refState {
		missing := 0
		for node := range pb.witness.State {
			if _, ok := w2.State[node]; !ok {
				missing++
			}
		}
		return fmt.Errorf("regenerated witness incomplete: state root %x, want %x (original nodes=%d regenerated nodes=%d missing=%d)",
			gotState, refState, len(pb.witness.State), len(w2.State), missing)
	}
	return nil
}

// singleRegenBlockHex is the block whose fixture lives as plain git objects
// under core/testdata/witness_regen (witness, block, and just its code
// blobs), unlike the git-lfs witness set — so this one round trip runs on
// any clone with no lfs pull.
const singleRegenBlockHex = "0x4EC6D13"

// skipUnlessCodesArchive skips a test when the shared codes archive is still
// a git LFS pointer, so a checkout without lfs skips instead of failing on
// missing code.
func skipUnlessCodesArchive(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(witnessDir, "codes.tar.gz"))
	if err == nil && isLFSPointer(data) {
		t.Skipf("testdata not materialized: codes.tar.gz: %v", errLFSPointer)
	}
}

// loadSingleWitnessRegenBlock loads the self-contained fixture. The files are
// regular committed objects, so failures here are real regressions, not a
// matter of un-pulled testdata.
func loadSingleWitnessRegenBlock(t *testing.T, blockHex string) (testBlockData, ethdb.Database) {
	t.Helper()
	dir := filepath.Join("testdata", "witness_regen")
	blockData, err := os.ReadFile(filepath.Join(dir, blockHex+".block"))
	if err != nil {
		t.Fatalf("reading fixture block: %v", err)
	}
	witness, err := loadWitnessFromJSON(filepath.Join(dir, blockHex+".witness.gz"))
	if err != nil {
		t.Fatalf("loading fixture witness: %v", err)
	}
	block, stateRoot, receiptRoot, err := parseBlockFromJSON(blockData)
	if err != nil {
		t.Fatalf("parsing fixture block: %v", err)
	}
	codes, err := os.Open(filepath.Join(dir, "codes.tar.gz"))
	if err != nil {
		t.Fatalf("opening fixture codes: %v", err)
	}
	defer codes.Close()
	diskdb := rawdb.NewMemoryDatabase()
	if err := loadCodesFromTarGz(diskdb, codes); err != nil {
		t.Fatalf("loading fixture codes: %v", err)
	}
	return testBlockData{witness: witness, block: block, stateRoot: stateRoot, receiptRoot: receiptRoot}, diskdb
}

// TestV2WitnessRegenerationSingleBlock runs the round trip on one mainnet
// block and additionally anchors the replays against the fixture's real
// state and receipt roots. Its fixture is committed as plain git objects,
// so it runs everywhere — no git lfs required.
func TestV2WitnessRegenerationSingleBlock(t *testing.T) {
	bd, diskdb := loadSingleWitnessRegenBlock(t, singleRegenBlockHex)
	config := params.BorMainnetChainConfig
	engine := &benchConsensus{}
	pb := prepareBlocks([]testBlockData{bd}, diskdb, config)[0]

	// Anchor: a replay from the original witness must reproduce the real
	// chain's roots for this block.
	stateRoot, receiptRoot, res, err := executeStatelessSerial(config, pb.block, pb.witness, &pb.author, engine, diskdb)
	if err != nil {
		t.Fatalf("replay from fixture witness: %v", err)
	}
	if res.GasUsed != pb.block.GasUsed() {
		t.Fatalf("fixture replay gas %d, want %d", res.GasUsed, pb.block.GasUsed())
	}
	if receiptRoot != pb.receiptRoot {
		t.Fatalf("fixture replay receipt root %x, want %x", receiptRoot, pb.receiptRoot)
	}
	if stateRoot != pb.stateRoot {
		t.Fatalf("fixture replay state root %x, want %x", stateRoot, pb.stateRoot)
	}

	if err := witnessRegenRoundTrip(&pb, diskdb, config, engine); err != nil {
		t.Fatal(err)
	}
}

// loadAllWitnessRegenBlocks enumerates every block/witness pair present in
// the testdata directory, not just the embedded quick set.
func loadAllWitnessRegenBlocks(t *testing.T) ([]testBlockData, ethdb.Database) {
	t.Helper()
	skipUnlessCodesArchive(t)
	entries, err := os.ReadDir(witnessDir)
	if err != nil {
		t.Skipf("witness directory %s not readable: %v", witnessDir, err)
	}
	diskdb := newCodeCachingDB(filepath.Join(witnessDir, "codes"))
	if err := diskdb.loadCodesFromDisk(); err != nil {
		t.Fatalf("loading codes archive: %v", err)
	}

	var blocks []testBlockData
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".witness.gz") {
			continue
		}
		blockHex := strings.TrimSuffix(name, ".witness.gz")
		blockData, err := os.ReadFile(filepath.Join(witnessDir, blockHex+".block"))
		if err != nil {
			continue
		}
		if isLFSPointer(blockData) {
			t.Skipf("testdata not materialized: %s: %v", blockHex, errLFSPointer)
		}
		witness, err := loadWitnessFromJSON(filepath.Join(witnessDir, name))
		if err != nil {
			if errors.Is(err, errLFSPointer) {
				t.Skipf("testdata not materialized: %v", err)
			}
			t.Fatalf("loading witness %s: %v", blockHex, err)
		}
		block, stateRoot, receiptRoot, err := parseBlockFromJSON(blockData)
		if err != nil {
			t.Fatalf("parsing block %s: %v", blockHex, err)
		}
		blocks = append(blocks, testBlockData{
			witness: witness, block: block,
			stateRoot: stateRoot, receiptRoot: receiptRoot,
		})
	}
	if len(blocks) == 0 {
		t.Skip("no witness blocks found in testdata")
	}
	t.Logf("loaded %d blocks from %s", len(blocks), witnessDir)
	return blocks, diskdb
}

// TestV2WitnessRegenerationAllBlocks runs the round trip over every witness
// block in testdata. Heavier than the single-block variant; skipped in -short.
func TestV2WitnessRegenerationAllBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full witness regeneration sweep in short mode")
	}
	blocks, diskdb := loadAllWitnessRegenBlocks(t)
	config := params.BorMainnetChainConfig
	engine := &benchConsensus{}

	failed := 0
	for i := range blocks {
		pb := prepareBlocks(blocks[i:i+1], diskdb, config)[0]
		if err := witnessRegenRoundTrip(&pb, diskdb, config, engine); err != nil {
			failed++
			t.Errorf("block %d (%d txs, %d gas): %v",
				pb.block.NumberU64(), len(pb.block.Transactions()), pb.block.GasUsed(), err)
		}
	}
	t.Logf("witness regeneration round trip: %d/%d blocks ok", len(blocks)-failed, len(blocks))
}
