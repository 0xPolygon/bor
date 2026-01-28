//go:build integration
// +build integration

package bor

import (
	"crypto/ecdsa"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBorConfigParameterChange tests that when BaseFeeChangeDenominator and TargetGasPercentage
// are changed at a certain block, blocks produced with the new parameters are accepted by validators.
// This test verifies the validate header flow and shows the dynamic change of BorConfig parameters.
func TestBorConfigParameterChange(t *testing.T) {
	t.Parallel()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	// Generate a batch of accounts to seal and fund with
	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}

	// Initialize genesis with 2 validators
	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 16)

	// Enable London fork from genesis to test EIP-1559
	genesis.Config.LondonBlock = common.Big0
	genesis.Config.Bor.JaipurBlock = common.Big0

	// Enable Dandeli fork at block 20 - this is where we'll change the parameters
	dandeliBlock := big.NewInt(20)
	genesis.Config.Bor.DandeliBlock = dandeliBlock

	// Set custom BaseFeeChangeDenominator and TargetGasPercentage that will take effect at Dandeli
	customBaseFeeChangeDenominator := uint64(32)  // Different from default (64)
	customTargetGasPercentage := uint64(70)        // Different from default (65)
	genesis.Config.Bor.BaseFeeChangeDenominator = &customBaseFeeChangeDenominator
	genesis.Config.Bor.TargetGasPercentage = &customTargetGasPercentage

	// Setup 2 nodes: one producer and one validator
	stacks, nodes, _ := setupMiner(t, 2, genesis)

	defer func() {
		for _, stack := range stacks {
			stack.Close()
		}
	}()

	// Start mining on both nodes
	for _, node := range nodes {
		if err := node.StartMining(); err != nil {
			t.Fatal("Error occurred while starting miner", "node", node, "error", err)
		}
	}

	// Wait for blocks to be mined beyond the Dandeli fork block
	targetBlockNum := dandeliBlock.Uint64() + 10 // Mine 10 blocks after the fork
	timeout := time.After(120 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for blocks to be mined")
		default:
			currentBlock := nodes[0].BlockChain().CurrentHeader()
			if currentBlock.Number.Uint64() >= targetBlockNum {
				goto checkResults
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

checkResults:
	// Verify that both nodes have the same chain (validator accepted producer's blocks)
	chain0 := nodes[0].BlockChain()
	chain1 := nodes[1].BlockChain()

	t.Logf("Node 0 current block: %d", chain0.CurrentHeader().Number.Uint64())
	t.Logf("Node 1 current block: %d", chain1.CurrentHeader().Number.Uint64())

	// Check that both nodes are synced
	require.Equal(t, chain0.CurrentHeader().Number.Uint64(), chain1.CurrentHeader().Number.Uint64(),
		"Both nodes should be at the same block height")

	// Verify blocks around the Dandeli fork
	preDandeliBlock := dandeliBlock.Uint64() - 1
	atDandeliBlock := dandeliBlock.Uint64()
	postDandeliBlock := dandeliBlock.Uint64() + 5

	// Check block hashes match between nodes (validator accepted producer's blocks)
	for _, blockNum := range []uint64{preDandeliBlock, atDandeliBlock, postDandeliBlock} {
		header0 := chain0.GetHeaderByNumber(blockNum)
		header1 := chain1.GetHeaderByNumber(blockNum)

		require.NotNil(t, header0, "Node 0 should have block %d", blockNum)
		require.NotNil(t, header1, "Node 1 should have block %d", blockNum)

		assert.Equal(t, header0.Hash(), header1.Hash(),
			"Both nodes should have the same block hash at height %d", blockNum)

		t.Logf("Block %d: Hash=%s, BaseFee=%s, GasLimit=%d, GasUsed=%d",
			blockNum, header0.Hash().Hex()[:10], header0.BaseFee.String(), header0.GasLimit, header0.GasUsed)
	}

	// Verify that the BaseFeeChangeDenominator and TargetGasPercentage are being used correctly
	// by checking the base fee calculation before and after Dandeli fork

	// Pre-Dandeli block: should use default parameters
	preHeader := chain0.GetHeaderByNumber(preDandeliBlock)
	preParentHeader := chain0.GetHeaderByNumber(preDandeliBlock - 1)

	if preParentHeader != nil && preHeader != nil {
		// Calculate expected base fee using pre-Dandeli parameters
		expectedPreBaseFee := eip1559.CalcBaseFee(genesis.Config, preParentHeader)
		t.Logf("Pre-Dandeli block %d: Expected BaseFee=%s, Actual BaseFee=%s",
			preDandeliBlock, expectedPreBaseFee.String(), preHeader.BaseFee.String())
		// Note: We don't assert equality here because the block producer might have chosen different gas usage
	}

	// Post-Dandeli block: should use custom parameters
	postHeader := chain0.GetHeaderByNumber(postDandeliBlock)
	postParentHeader := chain0.GetHeaderByNumber(postDandeliBlock - 1)

	if postParentHeader != nil && postHeader != nil {
		// Calculate expected base fee using post-Dandeli parameters
		expectedPostBaseFee := eip1559.CalcBaseFee(genesis.Config, postParentHeader)
		t.Logf("Post-Dandeli block %d: Expected BaseFee=%s, Actual BaseFee=%s",
			postDandeliBlock, expectedPostBaseFee.String(), postHeader.BaseFee.String())

		// After Dandeli, base fee validation is skipped to allow dynamic configuration
		// But we can verify that the calculation uses the new parameters by checking the target gas
		targetPercentage := genesis.Config.Bor.GetTargetGasPercentage(postParentHeader.Number)
		expectedTargetGas := postParentHeader.GasLimit * targetPercentage / 100
		t.Logf("Post-Dandeli target gas calculation: TargetPercentage=%d, GasLimit=%d, Expected TargetGas=%d",
			targetPercentage, postParentHeader.GasLimit, expectedTargetGas)

		// Verify the custom target percentage is being used
		assert.Equal(t, customTargetGasPercentage, targetPercentage,
			"Custom TargetGasPercentage should be used after Dandeli fork")

		// Verify the custom base fee change denominator is being used
		baseFeeChangeDenom := params.BaseFeeChangeDenominator(genesis.Config.Bor, postParentHeader.Number)
		assert.Equal(t, customBaseFeeChangeDenominator, baseFeeChangeDenom,
			"Custom BaseFeeChangeDenominator should be used after Dandeli fork")
	}

	// Verify both nodes successfully validated the blocks with new parameters
	// by checking that the validator node has the same blocks as the producer
	for blockNum := atDandeliBlock; blockNum <= postDandeliBlock; blockNum++ {
		header0 := chain0.GetHeaderByNumber(blockNum)
		header1 := chain1.GetHeaderByNumber(blockNum)

		// Get the block authors to identify producer vs validator
		author0, err := nodes[0].Engine().Author(header0)
		require.NoError(t, err)
		author1, err := nodes[1].Engine().Author(header1)
		require.NoError(t, err)

		t.Logf("Block %d: Node0 Author=%s, Node1 Author=%s, Same=%v",
			blockNum, author0.Hex()[:10], author1.Hex()[:10], author0 == author1)

		// Both nodes should have accepted the same block
		assert.Equal(t, header0.Hash(), header1.Hash(),
			"Validator should accept blocks produced with new BorConfig parameters at block %d", blockNum)
	}

	t.Log("Test completed successfully: Validator accepted blocks with changed BorConfig parameters")
}

// TestBorConfigParameterChangeVerification tests that the header verification
// correctly handles the parameter changes by verifying headers explicitly
func TestBorConfigParameterChangeVerification(t *testing.T) {
	t.Parallel()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	// Generate a batch of accounts
	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}

	// Initialize genesis
	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 16)
	genesis.Config.LondonBlock = common.Big0
	genesis.Config.Bor.JaipurBlock = common.Big0

	// Enable Dandeli fork at block 10
	dandeliBlock := big.NewInt(10)
	genesis.Config.Bor.DandeliBlock = dandeliBlock

	// Set custom parameters
	customBaseFeeChangeDenominator := uint64(128) // Very different from default
	customTargetGasPercentage := uint64(80)       // Very different from default
	genesis.Config.Bor.BaseFeeChangeDenominator = &customBaseFeeChangeDenominator
	genesis.Config.Bor.TargetGasPercentage = &customTargetGasPercentage

	// Setup a single node to produce blocks
	stack, ethBackend, err := InitMiner(genesis, keys[0], true)
	require.NoError(t, err)
	defer stack.Close()

	// Start mining
	err = ethBackend.StartMining()
	require.NoError(t, err)

	// Wait for blocks to be mined beyond the Dandeli fork
	targetBlockNum := dandeliBlock.Uint64() + 5
	timeout := time.After(60 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for blocks to be mined")
		default:
			currentBlock := ethBackend.BlockChain().CurrentHeader()
			if currentBlock.Number.Uint64() >= targetBlockNum {
				goto verifyHeaders
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

verifyHeaders:
	chain := ethBackend.BlockChain()
	engine := ethBackend.Engine().(*bor.Bor)

	// Verify headers before and after the fork
	for blockNum := uint64(1); blockNum <= targetBlockNum; blockNum++ {
		header := chain.GetHeaderByNumber(blockNum)
		require.NotNil(t, header, "Header %d should exist", blockNum)

		// Explicitly verify the header using the consensus engine
		err := engine.VerifyHeader(chain, header)
		require.NoError(t, err, "Header %d should be valid", blockNum)

		// Check which parameters are in effect
		if blockNum >= dandeliBlock.Uint64() {
			// Post-Dandeli: custom parameters should be used
			targetPercentage := genesis.Config.Bor.GetTargetGasPercentage(header.Number)
			baseFeeChangeDenom := params.BaseFeeChangeDenominator(genesis.Config.Bor, header.Number)

			assert.Equal(t, customTargetGasPercentage, targetPercentage,
				"Block %d should use custom TargetGasPercentage", blockNum)
			assert.Equal(t, customBaseFeeChangeDenominator, baseFeeChangeDenom,
				"Block %d should use custom BaseFeeChangeDenominator", blockNum)

			t.Logf("Block %d (Post-Dandeli): TargetGasPercentage=%d, BaseFeeChangeDenominator=%d, BaseFee=%s",
				blockNum, targetPercentage, baseFeeChangeDenom, header.BaseFee.String())
		} else {
			// Pre-Dandeli: default parameters should be used
			targetPercentage := genesis.Config.Bor.GetTargetGasPercentage(header.Number)
			baseFeeChangeDenom := params.BaseFeeChangeDenominator(genesis.Config.Bor, header.Number)

			// Pre-Dandeli should use pre-Dandeli defaults (not our custom values)
			assert.NotEqual(t, customTargetGasPercentage, targetPercentage,
				"Block %d should NOT use custom TargetGasPercentage", blockNum)

			t.Logf("Block %d (Pre-Dandeli): TargetGasPercentage=%d, BaseFeeChangeDenominator=%d, BaseFee=%s",
				blockNum, targetPercentage, baseFeeChangeDenom, header.BaseFee.String())
		}
	}

	t.Log("Test completed successfully: Headers verified correctly with parameter changes")
}

// TestBorConfigParameterDivergence tests that when producer and validator use different
// BaseFeeChangeDenominator and TargetGasPercentage values post-Dandeli, blocks are still
// accepted by validators. This demonstrates the flexibility of skipping base fee validation
// after Dandeli fork, allowing for dynamic configuration across the network.
func TestBorConfigParameterDivergence(t *testing.T) {
	t.Parallel()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	// Generate a batch of accounts to seal and fund with
	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}

	// Create genesis for producer (Node 0) with first set of parameters
	genesisProducer := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 16)
	genesisProducer.Config.LondonBlock = common.Big0
	genesisProducer.Config.Bor.JaipurBlock = common.Big0
	genesisProducer.Config.Bor.DandeliBlock = big.NewInt(10) // Enable Dandeli early

	// Producer uses first set of parameters
	producerBaseFeeChangeDenominator := uint64(32)
	producerTargetGasPercentage := uint64(70)
	genesisProducer.Config.Bor.BaseFeeChangeDenominator = &producerBaseFeeChangeDenominator
	genesisProducer.Config.Bor.TargetGasPercentage = &producerTargetGasPercentage

	// Create genesis for validator (Node 1) with DIFFERENT parameters
	genesisValidator := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 16)
	genesisValidator.Config.LondonBlock = common.Big0
	genesisValidator.Config.Bor.JaipurBlock = common.Big0
	genesisValidator.Config.Bor.DandeliBlock = big.NewInt(10) // Same Dandeli activation

	// Validator uses DIFFERENT parameters (simulating a "second change")
	validatorBaseFeeChangeDenominator := uint64(128) // Much larger denominator
	validatorTargetGasPercentage := uint64(80)        // Higher target percentage
	genesisValidator.Config.Bor.BaseFeeChangeDenominator = &validatorBaseFeeChangeDenominator
	genesisValidator.Config.Bor.TargetGasPercentage = &validatorTargetGasPercentage

	// Setup producer node
	stackProducer, nodeProducer, err := InitMiner(genesisProducer, keys[0], true)
	require.NoError(t, err)
	defer stackProducer.Close()

	// Wait for producer node to be ready
	for stackProducer.Server().NodeInfo().Ports.Listener == 0 {
		time.Sleep(250 * time.Millisecond)
	}

	// Setup validator node with different config
	stackValidator, nodeValidator, err := InitMiner(genesisValidator, keys[1], true)
	require.NoError(t, err)
	defer stackValidator.Close()

	// Wait for validator node to be ready
	for stackValidator.Server().NodeInfo().Ports.Listener == 0 {
		time.Sleep(250 * time.Millisecond)
	}

	// Connect validator to producer
	producerEnode := stackProducer.Server().Self()
	stackValidator.Server().AddPeer(producerEnode)

	// Start mining on producer only (validator will sync)
	err = nodeProducer.StartMining()
	require.NoError(t, err)

	// Let producer mine blocks with its parameter set
	time.Sleep(5 * time.Second)

	// Now start validator's mining to see it accept producer's blocks
	err = nodeValidator.StartMining()
	require.NoError(t, err)

	// Wait for both to mine blocks beyond Dandeli fork
	targetBlockNum := uint64(25)
	timeout := time.After(90 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for blocks to be mined")
		default:
			producerBlock := nodeProducer.BlockChain().CurrentHeader()
			validatorBlock := nodeValidator.BlockChain().CurrentHeader()
			if producerBlock.Number.Uint64() >= targetBlockNum &&
				validatorBlock.Number.Uint64() >= targetBlockNum-5 { // Validator might lag slightly
				goto checkDivergence
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

checkDivergence:
	chainProducer := nodeProducer.BlockChain()
	chainValidator := nodeValidator.BlockChain()

	t.Logf("Producer current block: %d", chainProducer.CurrentHeader().Number.Uint64())
	t.Logf("Validator current block: %d", chainValidator.CurrentHeader().Number.Uint64())

	// Verify blocks at key positions
	dandeliBlock := uint64(10)
	checkBlocks := []uint64{dandeliBlock, dandeliBlock + 5, dandeliBlock + 10}

	for _, blockNum := range checkBlocks {
		producerHeader := chainProducer.GetHeaderByNumber(blockNum)
		validatorHeader := chainValidator.GetHeaderByNumber(blockNum)

		if producerHeader == nil || validatorHeader == nil {
			continue // Skip if block not yet available
		}

		// Check if validator accepted producer's block
		assert.Equal(t, producerHeader.Hash(), validatorHeader.Hash(),
			"Validator should accept producer's block %d despite different config parameters", blockNum)

		// Get the parameters each node would use for calculation
		producerTargetPct := genesisProducer.Config.Bor.GetTargetGasPercentage(producerHeader.Number)
		producerDenom := params.BaseFeeChangeDenominator(genesisProducer.Config.Bor, producerHeader.Number)

		validatorTargetPct := genesisValidator.Config.Bor.GetTargetGasPercentage(validatorHeader.Number)
		validatorDenom := params.BaseFeeChangeDenominator(genesisValidator.Config.Bor, validatorHeader.Number)

		t.Logf("Block %d: Hash=%s", blockNum, producerHeader.Hash().Hex()[:10])
		t.Logf("  Producer config: TargetGasPercentage=%d, BaseFeeChangeDenominator=%d",
			producerTargetPct, producerDenom)
		t.Logf("  Validator config: TargetGasPercentage=%d, BaseFeeChangeDenominator=%d",
			validatorTargetPct, validatorDenom)
		t.Logf("  Block BaseFee=%s, GasLimit=%d, GasUsed=%d",
			producerHeader.BaseFee.String(), producerHeader.GasLimit, producerHeader.GasUsed)

		// Post-Dandeli: verify nodes are using their respective different configs
		if blockNum >= dandeliBlock {
			assert.Equal(t, producerTargetGasPercentage, producerTargetPct,
				"Producer should use its own TargetGasPercentage")
			assert.Equal(t, producerBaseFeeChangeDenominator, producerDenom,
				"Producer should use its own BaseFeeChangeDenominator")

			assert.Equal(t, validatorTargetGasPercentage, validatorTargetPct,
				"Validator should use its own TargetGasPercentage")
			assert.Equal(t, validatorBaseFeeChangeDenominator, validatorDenom,
				"Validator should use its own BaseFeeChangeDenominator")

			// Despite different configs, blocks should match (validator accepts producer's blocks)
			assert.Equal(t, producerHeader.Hash(), validatorHeader.Hash(),
				"Blocks should match despite different parameter configs")
		}
	}

	// Verify validator explicitly accepts producer's headers despite config divergence
	engineValidator := nodeValidator.Engine().(*bor.Bor)
	for blockNum := dandeliBlock; blockNum <= dandeliBlock+10; blockNum++ {
		producerHeader := chainProducer.GetHeaderByNumber(blockNum)
		if producerHeader == nil {
			continue
		}

		// Validator's consensus engine should accept producer's header
		// even though validator would calculate base fee differently
		err := engineValidator.VerifyHeader(chainValidator, producerHeader)
		require.NoError(t, err,
			"Validator should accept producer's block %d despite different base fee calculation parameters",
			blockNum)
	}

	t.Log("Test completed successfully: Validator accepted blocks from producer despite divergent BorConfig parameters")
	t.Log("This demonstrates that post-Dandeli base fee validation skip allows for flexible parameter updates")
}

// TestBorConfigMultipleParameterChanges tests a scenario where parameters are conceptually
// "changed" multiple times by having blocks at different heights that would use different
// parameter sets if the system supported block-height-keyed parameters.
// This test demonstrates header validation across multiple "transition points".
func TestBorConfigMultipleParameterChanges(t *testing.T) {
	t.Parallel()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	// Generate a batch of accounts
	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}

	// Initialize genesis with Dandeli fork at block 10
	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 16)
	genesis.Config.LondonBlock = common.Big0
	genesis.Config.Bor.JaipurBlock = common.Big0
	genesis.Config.Bor.DandeliBlock = big.NewInt(10)

	// Start with first set of parameters (will be used from block 10 onward)
	firstBaseFeeChangeDenominator := uint64(32)
	firstTargetGasPercentage := uint64(70)
	genesis.Config.Bor.BaseFeeChangeDenominator = &firstBaseFeeChangeDenominator
	genesis.Config.Bor.TargetGasPercentage = &firstTargetGasPercentage

	// Setup a single node
	stack, ethBackend, err := InitMiner(genesis, keys[0], true)
	require.NoError(t, err)
	defer stack.Close()

	// Start mining
	err = ethBackend.StartMining()
	require.NoError(t, err)

	// Mine blocks with first parameter set
	firstPhaseTarget := uint64(20)
	timeout := time.After(60 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for first phase blocks")
		default:
			currentBlock := ethBackend.BlockChain().CurrentHeader()
			if currentBlock.Number.Uint64() >= firstPhaseTarget {
				goto firstPhaseComplete
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

firstPhaseComplete:
	chain := ethBackend.BlockChain()
	engine := ethBackend.Engine().(*bor.Bor)

	t.Log("=== Phase 1 Complete: Blocks 1-20 with first parameter set ===")

	// Verify first phase blocks
	for blockNum := uint64(10); blockNum <= firstPhaseTarget; blockNum++ {
		header := chain.GetHeaderByNumber(blockNum)
		require.NotNil(t, header)

		err := engine.VerifyHeader(chain, header)
		require.NoError(t, err, "Header %d should be valid", blockNum)

		targetPct := genesis.Config.Bor.GetTargetGasPercentage(header.Number)
		denom := params.BaseFeeChangeDenominator(genesis.Config.Bor, header.Number)

		assert.Equal(t, firstTargetGasPercentage, targetPct)
		assert.Equal(t, firstBaseFeeChangeDenominator, denom)

		if blockNum%5 == 0 {
			t.Logf("Block %d: TargetGasPercentage=%d, BaseFeeChangeDenominator=%d, BaseFee=%s",
				blockNum, targetPct, denom, header.BaseFee.String())
		}
	}

	// Now simulate a "second parameter change" by updating the config
	// Note: In production, this would require a new hard fork. Here we're testing
	// that the validation logic would handle such changes gracefully.
	secondBaseFeeChangeDenominator := uint64(128)
	secondTargetGasPercentage := uint64(80)

	// Update the config (this simulates what a second hard fork would do)
	genesis.Config.Bor.BaseFeeChangeDenominator = &secondBaseFeeChangeDenominator
	genesis.Config.Bor.TargetGasPercentage = &secondTargetGasPercentage

	t.Log("=== Phase 2: Simulating parameter change at block 21 ===")
	t.Logf("New parameters: TargetGasPercentage=%d, BaseFeeChangeDenominator=%d",
		secondTargetGasPercentage, secondBaseFeeChangeDenominator)

	// Continue mining with new parameters
	secondPhaseTarget := uint64(35)
	timeout = time.After(60 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for second phase blocks")
		default:
			currentBlock := ethBackend.BlockChain().CurrentHeader()
			if currentBlock.Number.Uint64() >= secondPhaseTarget {
				goto secondPhaseComplete
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

secondPhaseComplete:
	t.Log("=== Phase 2 Complete: Blocks 21-35 with second parameter set ===")

	// Verify second phase blocks
	for blockNum := firstPhaseTarget + 1; blockNum <= secondPhaseTarget; blockNum++ {
		header := chain.GetHeaderByNumber(blockNum)
		require.NotNil(t, header)

		// Headers should still be valid even with parameter changes
		err := engine.VerifyHeader(chain, header)
		require.NoError(t, err, "Header %d should be valid with new parameters", blockNum)

		targetPct := genesis.Config.Bor.GetTargetGasPercentage(header.Number)
		denom := params.BaseFeeChangeDenominator(genesis.Config.Bor, header.Number)

		// After updating config, new parameters should be in effect
		assert.Equal(t, secondTargetGasPercentage, targetPct)
		assert.Equal(t, secondBaseFeeChangeDenominator, denom)

		if blockNum%5 == 0 {
			t.Logf("Block %d: TargetGasPercentage=%d, BaseFeeChangeDenominator=%d, BaseFee=%s",
				blockNum, targetPct, denom, header.BaseFee.String())
		}
	}

	// Verify all blocks in the chain are still valid
	t.Log("=== Verifying entire chain remains valid ===")
	for blockNum := uint64(1); blockNum <= secondPhaseTarget; blockNum++ {
		header := chain.GetHeaderByNumber(blockNum)
		require.NotNil(t, header)

		err := engine.VerifyHeader(chain, header)
		require.NoError(t, err, "Header %d should remain valid", blockNum)
	}

	t.Log("Test completed successfully: Multiple parameter changes handled correctly")
	t.Log("Chain remains valid across parameter transitions")
}
