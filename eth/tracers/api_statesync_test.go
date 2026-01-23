package tracers

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// borTestBackend extends testBackend with Bor-specific configuration for testing
// state sync transaction handling across the Madhugiri hardfork.
type borTestBackend struct {
	testBackend
}

// newBorChainConfig creates a chain config suitable for Bor state sync testing.
// Includes all Bor-specific hardfork blocks from mainnet.
// Note: Shanghai/Cancun/Prague omitted as ethash.NewFaker() doesn't support them.
func newBorChainConfig(madhugiriBlock *big.Int) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:             big.NewInt(137),
		HomesteadBlock:      big.NewInt(0),
		DAOForkBlock:        nil,
		DAOForkSupport:      true,
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		MuirGlacierBlock:    big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			JaipurBlock:       big.NewInt(0),
			DelhiBlock:        big.NewInt(0),
			IndoreBlock:       big.NewInt(0),
			AhmedabadBlock:    big.NewInt(0),
			BhilaiBlock:       big.NewInt(0),
			RioBlock:          big.NewInt(0),
			MadhugiriBlock:    madhugiriBlock,
			MadhugiriProBlock: madhugiriBlock,
			DandeliBlock:      big.NewInt(0),
			Period: map[string]uint64{
				"0": 2,
			},
			ProducerDelay: map[string]uint64{
				"0": 2,
			},
			Sprint: map[string]uint64{
				"0": 16,
			},
			BackupMultiplier: map[string]uint64{
				"0": 2,
			},
			ValidatorContract:     "0x0000000000000000000000000000000000001000",
			StateReceiverContract: "0x0000000000000000000000000000000000001001",
			BurntContract: map[string]string{
				"0": "0x000000000000000000000000000000000000dead",
			},
			Coinbase: map[string]string{
				"0": "0x0000000000000000000000000000000000000000",
			},
		},
	}
}

// newBorTestBackend creates a test backend with Bor consensus config.
// madhugiriBlock specifies when the Madhugiri fork activates (nil = no fork).
func newBorTestBackend(t *testing.T, n int, gspec *core.Genesis, madhugiriBlock *big.Int, generator func(i int, b *core.BlockGen)) *borTestBackend {
	// Create a proper Bor chain config
	chainConfig := newBorChainConfig(madhugiriBlock)
	gspec.Config = chainConfig

	backend := &borTestBackend{
		testBackend: testBackend{
			chainConfig: chainConfig,
			engine:      ethash.NewFaker(),
			chaindb:     rawdb.NewMemoryDatabase(),
		},
	}

	// Generate blocks
	_, blocks, _ := core.GenerateChainWithGenesis(gspec, backend.engine, n, generator)

	// Import the canonical chain
	options := &core.BlockChainConfig{
		TrieCleanLimit: 256,
		TrieDirtyLimit: 256,
		TrieTimeLimit:  5 * time.Minute,
		SnapshotLimit:  0,
		ArchiveMode:    true,
	}
	chain, err := core.NewBlockChain(backend.chaindb, gspec, backend.engine, options)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}

	if len(blocks) > 0 {
		if n, err := chain.InsertChain(blocks, false); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", n, err)
		}
	}

	backend.chain = chain
	return backend
}

// createStateSyncTx creates a state sync transaction for testing.
func createStateSyncTx(id uint64) *types.Transaction {
	return types.NewTx(&types.StateSyncTx{
		StateSyncData: []*types.StateSyncData{
			{
				ID:       id,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001001"),
				Data:     []byte{0x01, 0x02, 0x03},
				TxHash:   common.HexToHash("0x0000dead"),
			},
		},
	})
}

// TestTraceBlockStateSyncPostMadhugiri verifies that post-Madhugiri blocks
// include state sync transactions in trace results regardless of BorTraceEnabled.
// This is the core fix for the tx count mismatch bug (PIP-74).
func TestTraceBlockStateSyncPostMadhugiri(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	// Madhugiri at block 5 - test block 10 which is post-fork
	madhugiriBlock := big.NewInt(5)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		// Add state sync tx to post-Madhugiri blocks (block 6+)
		// In real Bor, this happens during block finalization
		if i >= 5 {
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	// Test post-Madhugiri block (block 10) with BorTraceEnabled=false (default)
	t.Run("PostMadhugiri_BorTraceDisabled", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(10))
		if block == nil {
			t.Fatal("block 10 not found")
		}

		expectedTxCount := len(block.Transactions())
		if expectedTxCount != 2 {
			t.Fatalf("expected 2 txs in block (1 regular + 1 state sync), got %d", expectedTxCount)
		}

		// Verify last tx is state sync
		lastTx := block.Transactions()[expectedTxCount-1]
		if lastTx.Type() != types.StateSyncTxType {
			t.Fatalf("expected last tx to be StateSyncTxType, got %d", lastTx.Type())
		}

		// Trace with default config (BorTraceEnabled=false)
		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(10), nil)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		// Post-Madhugiri: trace count MUST match block tx count
		if len(results) != expectedTxCount {
			t.Errorf("tx count mismatch: block has %d txs, trace returned %d results (expected match post-Madhugiri)",
				expectedTxCount, len(results))
		}
	})

	// Test post-Madhugiri block with BorTraceEnabled=true
	t.Run("PostMadhugiri_BorTraceEnabled", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(10))
		expectedTxCount := len(block.Transactions())

		borTraceEnabled := true
		config := &TraceConfig{BorTraceEnabled: &borTraceEnabled}

		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(10), config)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		if len(results) != expectedTxCount {
			t.Errorf("tx count mismatch with BorTraceEnabled=true: block has %d txs, trace returned %d results",
				expectedTxCount, len(results))
		}
	})
}

// TestTraceBlockStateSyncPreMadhugiri verifies legacy behavior is preserved
// for pre-Madhugiri blocks where state sync was not canonical.
func TestTraceBlockStateSyncPreMadhugiri(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	// Madhugiri at block 100 - all test blocks are pre-fork
	madhugiriBlock := big.NewInt(100)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	// Pre-Madhugiri block without state sync tx in body
	t.Run("PreMadhugiri_NoStateSyncInBody", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(5))
		if block == nil {
			t.Fatal("block 5 not found")
		}

		blockTxCount := len(block.Transactions())
		if blockTxCount != 1 {
			t.Fatalf("expected 1 tx in pre-Madhugiri block, got %d", blockTxCount)
		}

		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(5), nil)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		// Pre-Madhugiri without state sync: trace count should match block body
		if len(results) != blockTxCount {
			t.Errorf("expected %d trace results, got %d", blockTxCount, len(results))
		}
	})
}

// TestTraceBlockMadhugiriForkBoundary tests edge cases at the exact fork boundary.
func TestTraceBlockMadhugiriForkBoundary(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	// Madhugiri at block 5
	madhugiriBlock := big.NewInt(5)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		// Add state sync tx starting at Madhugiri block
		if i >= 4 { // Block 5 is index 4 (0-indexed)
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	// Test block just before Madhugiri (block 4)
	t.Run("BlockBeforeFork", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(4))
		if block == nil {
			t.Fatal("block 4 not found")
		}

		blockTxCount := len(block.Transactions())
		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(4), nil)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		// Block 4 is pre-Madhugiri, no state sync in body
		if len(results) != blockTxCount {
			t.Errorf("block 4 (pre-fork): expected %d results, got %d", blockTxCount, len(results))
		}
	})

	// Test exact Madhugiri fork block (block 5)
	t.Run("ExactForkBlock", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(5))
		if block == nil {
			t.Fatal("block 5 not found")
		}

		blockTxCount := len(block.Transactions())
		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(5), nil)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		// Block 5 is Madhugiri block - state sync should be included
		if len(results) != blockTxCount {
			t.Errorf("block 5 (fork block): expected %d results, got %d", blockTxCount, len(results))
		}
	})

	// Test block just after Madhugiri (block 6)
	t.Run("BlockAfterFork", func(t *testing.T) {
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(6))
		if block == nil {
			t.Fatal("block 6 not found")
		}

		blockTxCount := len(block.Transactions())
		results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(6), nil)
		if err != nil {
			t.Fatalf("TraceBlockByNumber failed: %v", err)
		}

		// Block 6 is post-Madhugiri - state sync should be included
		if len(results) != blockTxCount {
			t.Errorf("block 6 (post-fork): expected %d results, got %d", blockTxCount, len(results))
		}
	})
}

// TestTraceBlockByHashStateSyncPostMadhugiri tests TraceBlockByHash endpoint.
func TestTraceBlockByHashStateSyncPostMadhugiri(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	madhugiriBlock := big.NewInt(5)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		if i >= 5 {
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(10))
	if block == nil {
		t.Fatal("block 10 not found")
	}

	expectedTxCount := len(block.Transactions())

	results, err := api.TraceBlockByHash(context.Background(), block.Hash(), nil)
	if err != nil {
		t.Fatalf("TraceBlockByHash failed: %v", err)
	}

	if len(results) != expectedTxCount {
		t.Errorf("TraceBlockByHash: tx count mismatch: block has %d txs, trace returned %d results",
			expectedTxCount, len(results))
	}
}

// TestTraceChainStateSyncPostMadhugiri tests traceChain across fork boundary.
func TestTraceChainStateSyncPostMadhugiri(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	madhugiriBlock := big.NewInt(5)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		if i >= 5 {
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	// Use internal traceChain method (like existing tests do)
	from, _ := api.blockByNumber(context.Background(), rpc.BlockNumber(4))
	to, _ := api.blockByNumber(context.Background(), rpc.BlockNumber(7))
	resCh := api.traceChain(from, to, nil, nil)

	// Verify trace count for each block
	nextBlock := uint64(5) // traceChain starts from start+1
	for result := range resCh {
		blockNum := uint64(result.Block)
		if blockNum != nextBlock {
			t.Errorf("unexpected block number: got %d, want %d", blockNum, nextBlock)
		}

		// Get expected tx count from actual block
		block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(blockNum))
		if block == nil {
			t.Errorf("block %d not found", blockNum)
			nextBlock++
			continue
		}

		expectedTxCount := len(block.Transactions())
		if len(result.Traces) != expectedTxCount {
			t.Errorf("block %d: trace count mismatch: expected %d, got %d",
				blockNum, expectedTxCount, len(result.Traces))
		}

		nextBlock++
	}

	// Verify we processed all expected blocks
	if nextBlock != 8 { // Should have processed blocks 5, 6, 7
		t.Errorf("did not process all blocks: stopped at %d", nextBlock)
	}
}

// TestIntermediateRootsStateSyncPostMadhugiri tests IntermediateRoots endpoint.
func TestIntermediateRootsStateSyncPostMadhugiri(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	madhugiriBlock := big.NewInt(5)
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		if i >= 5 {
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(10))
	if block == nil {
		t.Fatal("block 10 not found")
	}

	expectedTxCount := len(block.Transactions())

	roots, err := api.IntermediateRoots(context.Background(), block.Hash(), nil)
	if err != nil {
		t.Fatalf("IntermediateRoots failed: %v", err)
	}

	// Each tx should produce an intermediate root
	if len(roots) != expectedTxCount {
		t.Errorf("IntermediateRoots: expected %d roots (one per tx), got %d",
			expectedTxCount, len(roots))
	}
}

// TestStateSyncTxTypeDetection verifies correct detection of state sync tx type.
func TestStateSyncTxTypeDetection(t *testing.T) {
	t.Parallel()

	stateSyncTx := createStateSyncTx(1)

	if stateSyncTx.Type() != types.StateSyncTxType {
		t.Errorf("expected tx type %d (StateSyncTxType), got %d",
			types.StateSyncTxType, stateSyncTx.Type())
	}

	// Verify properties of state sync tx
	if stateSyncTx.Gas() != 0 {
		t.Errorf("state sync tx should have 0 gas, got %d", stateSyncTx.Gas())
	}
	if stateSyncTx.GasPrice().Cmp(big.NewInt(0)) != 0 {
		t.Errorf("state sync tx should have 0 gas price")
	}
	if stateSyncTx.Value().Cmp(big.NewInt(0)) != 0 {
		t.Errorf("state sync tx should have 0 value")
	}
}

// TestNoMadhugiriFork tests behavior when Madhugiri fork is not configured.
func TestNoMadhugiriFork(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	// nil = no Madhugiri fork
	genBlocks := 10

	backend := newBorTestBackend(t, genBlocks, gspec, nil, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(5))
	if block == nil {
		t.Fatal("block 5 not found")
	}

	results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(5), nil)
	if err != nil {
		t.Fatalf("TraceBlockByNumber failed: %v", err)
	}

	// Without Madhugiri fork, trace count should match block body
	expectedCount := len(block.Transactions())
	if len(results) != expectedCount {
		t.Errorf("without Madhugiri fork: expected %d results, got %d", expectedCount, len(results))
	}
}

// TestMadhugiriAtGenesis tests edge case where Madhugiri is active from genesis.
func TestMadhugiriAtGenesis(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	// Madhugiri at block 0 = active from genesis
	madhugiriBlock := big.NewInt(0)
	genBlocks := 5

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		// Use dynamic fee tx with proper fee caps
		baseFee := b.BaseFee()
		tx, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			Nonce:     uint64(i),
			To:        &address,
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: new(big.Int).Mul(baseFee, big.NewInt(2)),
			GasTipCap: big.NewInt(1),
		}), b.Signer(), key)
		b.AddTx(tx)

		// All blocks have state sync (Madhugiri active from genesis)
		stateSyncTx := createStateSyncTx(uint64(i))
		b.AddUncheckedTx(stateSyncTx)
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	// Test block 1 (all blocks are post-Madhugiri)
	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(1))
	if block == nil {
		t.Fatal("block 1 not found")
	}

	expectedTxCount := len(block.Transactions())
	if expectedTxCount != 2 {
		t.Fatalf("expected 2 txs, got %d", expectedTxCount)
	}

	results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(1), nil)
	if err != nil {
		t.Fatalf("TraceBlockByNumber failed: %v", err)
	}

	if len(results) != expectedTxCount {
		t.Errorf("Madhugiri from genesis: expected %d results, got %d", expectedTxCount, len(results))
	}
}

// TestMultipleStateSyncEvents tests blocks with multiple state sync events bundled.
func TestMultipleStateSyncEvents(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		gspec   = &core.Genesis{
			Alloc: types.GenesisAlloc{
				address: {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	madhugiriBlock := big.NewInt(0)
	genBlocks := 5

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)

		// Create state sync tx with multiple events
		stateSyncTx := types.NewTx(&types.StateSyncTx{
			StateSyncData: []*types.StateSyncData{
				{ID: uint64(i*10 + 1), Contract: common.HexToAddress("0x1001"), Data: []byte{0x01}},
				{ID: uint64(i*10 + 2), Contract: common.HexToAddress("0x1001"), Data: []byte{0x02}},
				{ID: uint64(i*10 + 3), Contract: common.HexToAddress("0x1001"), Data: []byte{0x03}},
			},
		})
		b.AddUncheckedTx(stateSyncTx)
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(3))
	if block == nil {
		t.Fatal("block 3 not found")
	}

	// Should have 2 txs: 1 regular + 1 state sync (with 3 events bundled)
	expectedTxCount := len(block.Transactions())
	if expectedTxCount != 2 {
		t.Fatalf("expected 2 txs, got %d", expectedTxCount)
	}

	results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(3), nil)
	if err != nil {
		t.Fatalf("TraceBlockByNumber failed: %v", err)
	}

	// Multiple events in one state sync tx = still 1 tx in trace results
	if len(results) != expectedTxCount {
		t.Errorf("multiple state sync events: expected %d results, got %d", expectedTxCount, len(results))
	}
}

// TestEmptyBlockWithStateSyncOnly tests blocks containing only state sync tx.
func TestEmptyBlockWithStateSyncOnly(t *testing.T) {
	t.Parallel()

	var (
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		gspec  = &core.Genesis{
			Alloc: types.GenesisAlloc{
				crypto.PubkeyToAddress(key.PublicKey): {Balance: big.NewInt(params.Ether)},
			},
		}
	)

	madhugiriBlock := big.NewInt(0)
	genBlocks := 5

	backend := newBorTestBackend(t, genBlocks, gspec, madhugiriBlock, func(i int, b *core.BlockGen) {
		// Only add state sync tx, no regular txs
		if i == 2 {
			stateSyncTx := createStateSyncTx(uint64(i))
			b.AddUncheckedTx(stateSyncTx)
		}
	})
	defer backend.chain.Stop()

	api := NewAPI(backend)

	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(3))
	if block == nil {
		t.Fatal("block 3 not found")
	}

	expectedTxCount := len(block.Transactions())

	results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(3), nil)
	if err != nil {
		t.Fatalf("TraceBlockByNumber failed: %v", err)
	}

	if len(results) != expectedTxCount {
		t.Errorf("block with only state sync: expected %d results, got %d", expectedTxCount, len(results))
	}
}
