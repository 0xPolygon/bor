package bor

import (
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// rawdbChain is a minimal test implementation of consensus.ChainHeaderReader
// backed by rawdb, with small diffs to simulate reorgs mid-GetHeaderByNumber.
type rawdbChain struct {
	db      ethdb.Database
	config  *params.ChainConfig
	current *types.Header

	chainALabel map[uint64]common.Hash
	chainBLabel map[uint64]common.Hash

	mu        sync.Mutex
	readCount int
	plan      *reorgPlan
}

type reorgPlan struct {
	triggerAfterReads int
	performed         bool
}

func newRawDBChain(db ethdb.Database, cfg *params.ChainConfig, current *types.Header,
	chainALabel, chainBLabel map[uint64]common.Hash,
) *rawdbChain {
	return &rawdbChain{
		db:          db,
		config:      cfg,
		current:     current,
		chainALabel: chainALabel,
		chainBLabel: chainBLabel,
	}
}

func (c *rawdbChain) Config() *params.ChainConfig {
	return c.config
}

func (c *rawdbChain) CurrentHeader() *types.Header {
	return c.current
}

func (c *rawdbChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	return rawdb.ReadHeader(c.db, hash, number)
}

func (c *rawdbChain) GetHeaderByNumber(number uint64) *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readCount++

	// trigger a reorg to chain B mid-flight.
	if c.plan != nil && !c.plan.performed && c.readCount > c.plan.triggerAfterReads {
		fmt.Printf("[rawdbChain] Triggering reorg to chain B before serving header #%d (readCount=%d)\n",
			number, c.readCount)
		c.plan.performed = true
		c.reorgToChainBLocked()
	}

	hash := rawdb.ReadCanonicalHash(c.db, number)
	if hash == (common.Hash{}) {
		fmt.Printf("[rawdbChain] GetHeaderByNumber(%d) -> <nil> (no canonical hash)\n", number)
		return nil
	}

	origin := "unknown"
	if h, ok := c.chainALabel[number]; ok && h == hash {
		origin = "A"
	}
	if h, ok := c.chainBLabel[number]; ok && h == hash {
		origin = "B"
	}

	fmt.Printf("[rawdbChain] GetHeaderByNumber(%d) -> hash=%s (origin chain %s)\n", number, hash, origin)

	return rawdb.ReadHeader(c.db, hash, number)
}

func (c *rawdbChain) GetHeaderByHash(hash common.Hash) *types.Header {
	number := rawdb.ReadHeaderNumber(c.db, hash)
	if number == nil {
		return nil
	}
	return rawdb.ReadHeader(c.db, hash, *number)
}

func (c *rawdbChain) GetTd(hash common.Hash, number uint64) *big.Int {
	return rawdb.ReadTd(c.db, hash, number)
}

func (c *rawdbChain) reorgToChainBLocked() {
	for number, hash := range c.chainBLabel {
		rawdb.WriteCanonicalHash(c.db, hash, number)
	}
	if hashB, ok := c.chainBLabel[4]; ok {
		rawdb.WriteHeadHeaderHash(c.db, hashB)
		c.current = rawdb.ReadHeader(c.db, hashB, 4)
	}
	fmt.Println("[rawdbChain] Canonical mapping switched to chain B for blocks 2-4")
}

func (c *rawdbChain) reorgToChainB() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reorgToChainBLocked()
	c.readCount = 0
	if c.plan != nil {
		c.plan.performed = false
	}
}

func (c *rawdbChain) reorgToChainA() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for number, hash := range c.chainALabel {
		rawdb.WriteCanonicalHash(c.db, hash, number)
	}

	if hashA, ok := c.chainALabel[4]; ok {
		rawdb.WriteHeadHeaderHash(c.db, hashA)
		c.current = rawdb.ReadHeader(c.db, hashA, 4)
	}

	c.readCount = 0
	if c.plan != nil {
		c.plan.performed = false
	}

	fmt.Println("[rawdbChain] Canonical mapping switched back to chain A for blocks 1-4")
}

func (c *rawdbChain) setReorgPlan(triggerAfterReads int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readCount = 0
	c.plan = &reorgPlan{
		triggerAfterReads: triggerAfterReads,
		performed:         false,
	}

	fmt.Printf("[rawdbChain] Configured in-flight reorg plan: reorg to B after %d header reads\n",
		triggerAfterReads)
}

func (c *rawdbChain) clearReorgPlan() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readCount = 0
	c.plan = nil
}

func TestGetRootHash_ReorgDuringCall_ReturnsError(t *testing.T) {
	t.Parallel()

	chain := newTestRawDBChain(t)

	fmt.Println("=== PoC: GetRootHash with in-flight reorg must fail with ErrReorgDuringRootComputation ===")

	api := &API{chain: chain}

	// Configure a reorg plan: after 2 header reads, flip to chain B.
	chain.setReorgPlan(2)

	root, err := api.GetRootHash(1, 4)
	require.Error(t, err)
	require.ErrorIs(t, err, errReorgDuringRootComputation)
	require.Equal(t, "", root, "root must be empty when a reorg is detected in-flight")

	// Clean up reorg plan so it doesn't affect other tests
	chain.clearReorgPlan()
}

func TestGetRootHash_PureChainsStable(t *testing.T) {
	t.Parallel()

	chain := newTestRawDBChain(t)

	// Start on chain A
	chain.reorgToChainA()

	fmt.Println("=== Baseline: computing root over pure chain A ===")
	api := &API{chain: chain}
	rootA, err := api.GetRootHash(1, 4)
	require.NoError(t, err)
	fmt.Printf("rootA (all A headers): %s\n\n", rootA)

	// Switch to pure chain B and recompute
	chain.reorgToChainB()

	fmt.Println("=== Baseline: computing root over pure chain B ===")
	rootB, err := api.GetRootHash(1, 4)
	require.NoError(t, err)
	fmt.Printf("rootB (all B headers): %s\n\n", rootB)

	require.NotEqual(t, rootA, rootB, "roots for pure chain A and pure chain B must differ")

	// Switch back to A and ensure the root is stable
	chain.reorgToChainA()

	fmt.Println("=== Re-check: root over chain A after switching back ===")
	rootA2, err := api.GetRootHash(1, 4)
	require.NoError(t, err)
	fmt.Printf("rootA2 (all A headers): %s\n\n", rootA2)

	require.Equal(t, rootA, rootA2, "root over chain A should be stable")
}

func TestGetRootHashCacheInvalidatedOnReorg(t *testing.T) {
	t.Parallel()

	chain := newTestRawDBChain(t)

	// Start on chain A and compute a root, which should be cached
	chain.reorgToChainA()
	api := &API{chain: chain}

	fmt.Println("=== Cache test: root on chain A ===")
	rootA, err := api.GetRootHash(1, 4)
	require.NoError(t, err)

	headA := chain.CurrentHeader().Hash()

	// Reorg to chain B and compute again: cache entry for (1,4) must not be reused,
	// because it's bound to headA and the tip has changed.
	chain.reorgToChainB()

	fmt.Println("=== Cache test: root on chain B after reorg ===")
	rootB, err := api.GetRootHash(1, 4)
	require.NoError(t, err)

	headB := chain.CurrentHeader().Hash()
	require.NotEqual(t, headA, headB, "tip hashes must differ after reorg")
	require.NotEqual(t, rootA, rootB, "root after reorg must be recomputed for the new canonical fork")
}

func newTestRawDBChain(t *testing.T) *rawdbChain {
	t.Helper()

	db := rawdb.NewMemoryDatabase()

	chainCfg := &params.ChainConfig{
		ChainID: big.NewInt(1),
	}

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       0,
		GasLimit:   8_000_000,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65),
	}
	rawdb.WriteHeader(db, genesis)
	rawdb.WriteCanonicalHash(db, genesis.Hash(), 0)
	rawdb.WriteHeadHeaderHash(db, genesis.Hash())
	rawdb.WriteTd(db, genesis.Hash(), 0, big.NewInt(1))

	chainALabel := make(map[uint64]common.Hash)
	chainBLabel := make(map[uint64]common.Hash)

	// Build chain A for blocks 1–4
	prevHashA := genesis.Hash()
	for i := uint64(1); i <= 4; i++ {
		h := &types.Header{
			ParentHash: prevHashA,
			Number:     new(big.Int).SetUint64(i),
			Time:       i,
			GasLimit:   8_000_000,
			Difficulty: big.NewInt(1),
			Extra:      make([]byte, 32+65),
			TxHash:     crypto.Keccak256Hash([]byte(fmt.Sprintf("A-tx-%d", i))),
			ReceiptHash: crypto.Keccak256Hash([]byte(fmt.Sprintf(
				"A-receipt-%d", i,
			))),
		}

		rawdb.WriteHeader(db, h)
		rawdb.WriteCanonicalHash(db, h.Hash(), i)
		rawdb.WriteTd(db, h.Hash(), i, big.NewInt(int64(i+1)))

		chainALabel[i] = h.Hash()
		prevHashA = h.Hash()
	}

	// Build chain B, which diverges from A at block 2, but shares genesis and block 1
	prevHashB := chainALabel[1]
	for i := uint64(2); i <= 4; i++ {
		h := &types.Header{
			ParentHash: prevHashB,
			Number:     new(big.Int).SetUint64(i),
			Time:       i + 100,
			GasLimit:   8_000_000,
			Difficulty: big.NewInt(2),
			Extra:      make([]byte, 32+65),
			TxHash:     crypto.Keccak256Hash([]byte(fmt.Sprintf("B-tx-%d", i))),
			ReceiptHash: crypto.Keccak256Hash([]byte(fmt.Sprintf(
				"B-receipt-%d", i,
			))),
		}

		rawdb.WriteHeader(db, h)
		rawdb.WriteTd(db, h.Hash(), i, big.NewInt(int64(i+100)))

		chainBLabel[i] = h.Hash()
		prevHashB = h.Hash()
	}

	current := rawdb.ReadHeader(db, chainALabel[4], 4)
	require.NotNil(t, current)

	return newRawDBChain(db, chainCfg, current, chainALabel, chainBLabel)
}
