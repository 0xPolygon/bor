// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eip1559

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// copyConfig does a _shallow_ copy of a given config. Safe to set new values, but
// do not use e.g. SetInt() on the numbers. For testing only
func copyConfig(original *params.ChainConfig) *params.ChainConfig {
	config := &params.ChainConfig{
		ChainID:                 original.ChainID,
		HomesteadBlock:          original.HomesteadBlock,
		DAOForkBlock:            original.DAOForkBlock,
		DAOForkSupport:          original.DAOForkSupport,
		EIP150Block:             original.EIP150Block,
		EIP155Block:             original.EIP155Block,
		EIP158Block:             original.EIP158Block,
		ByzantiumBlock:          original.ByzantiumBlock,
		ConstantinopleBlock:     original.ConstantinopleBlock,
		PetersburgBlock:         original.PetersburgBlock,
		IstanbulBlock:           original.IstanbulBlock,
		MuirGlacierBlock:        original.MuirGlacierBlock,
		BerlinBlock:             original.BerlinBlock,
		LondonBlock:             original.LondonBlock,
		TerminalTotalDifficulty: original.TerminalTotalDifficulty,
		Ethash:                  original.Ethash,
		Clique:                  original.Clique,
	}
	if original.Bor != nil {
		config.Bor = &params.BorConfig{
			Period:                          original.Bor.Period,
			ProducerDelay:                   original.Bor.ProducerDelay,
			Sprint:                          original.Bor.Sprint,
			BackupMultiplier:                original.Bor.BackupMultiplier,
			ValidatorContract:               original.Bor.ValidatorContract,
			StateReceiverContract:           original.Bor.StateReceiverContract,
			OverrideStateSyncRecords:        original.Bor.OverrideStateSyncRecords,
			OverrideStateSyncRecordsInRange: original.Bor.OverrideStateSyncRecordsInRange,
			BlockAlloc:                      original.Bor.BlockAlloc,
			BurntContract:                   original.Bor.BurntContract,
			Coinbase:                        original.Bor.Coinbase,
			SkipValidatorByteCheck:          original.Bor.SkipValidatorByteCheck,
			JaipurBlock:                     original.Bor.JaipurBlock,
			DelhiBlock:                      original.Bor.DelhiBlock,
			IndoreBlock:                     original.Bor.IndoreBlock,
			StateSyncConfirmationDelay:      original.Bor.StateSyncConfirmationDelay,
			AhmedabadBlock:                  original.Bor.AhmedabadBlock,
			BhilaiBlock:                     original.Bor.BhilaiBlock,
			RioBlock:                        original.Bor.RioBlock,
			MadhugiriBlock:                  original.Bor.MadhugiriBlock,
			MadhugiriProBlock:               original.Bor.MadhugiriProBlock,
			DandeliBlock:                    original.Bor.DandeliBlock,
		}
	}
	return config
}

func config() *params.ChainConfig {
	config := copyConfig(params.TestChainConfig)
	config.LondonBlock = big.NewInt(5)
	config.Bor.DelhiBlock = big.NewInt(8)

	return config
}

// TestBlockGasLimits tests the gasLimit checks for blocks both across
// the EIP-1559 boundary and post-1559 blocks
func TestBlockGasLimits(t *testing.T) {
	initial := new(big.Int).SetUint64(params.InitialBaseFee)

	for i, tc := range []struct {
		pGasLimit uint64
		pNum      int64
		gasLimit  uint64
		ok        bool
	}{
		// Transitions from non-london to london
		{10000000, 4, 20000000, true},  // No change
		{10000000, 4, 20019530, true},  // Upper limit
		{10000000, 4, 20019531, false}, // Upper +1
		{10000000, 4, 19980470, true},  // Lower limit
		{10000000, 4, 19980469, false}, // Lower limit -1
		// London to London
		{20000000, 5, 20000000, true},
		{20000000, 5, 20019530, true},  // Upper limit
		{20000000, 5, 20019531, false}, // Upper limit +1
		{20000000, 5, 19980470, true},  // Lower limit
		{20000000, 5, 19980469, false}, // Lower limit -1
		{40000000, 5, 40039061, true},  // Upper limit
		{40000000, 5, 40039062, false}, // Upper limit +1
		{40000000, 5, 39960939, true},  // lower limit
		{40000000, 5, 39960938, false}, // Lower limit -1
	} {
		parent := &types.Header{
			GasUsed:  tc.pGasLimit / 2,
			GasLimit: tc.pGasLimit,
			BaseFee:  initial,
			Number:   big.NewInt(tc.pNum),
		}
		header := &types.Header{
			GasUsed:  tc.gasLimit / 2,
			GasLimit: tc.gasLimit,
			BaseFee:  initial,
			Number:   big.NewInt(tc.pNum + 1),
		}
		err := VerifyEIP1559Header(config(), parent, header)
		if tc.ok && err != nil {
			t.Errorf("test %d: Expected valid header: %s", i, err)
		}

		if !tc.ok && err == nil {
			t.Errorf("test %d: Expected invalid header", i)
		}
	}
}

// TestCalcBaseFee assumes all blocks are 1559-blocks
func TestCalcBaseFee(t *testing.T) {
	t.Parallel()

	tests := []struct {
		parentBaseFee   int64
		parentGasLimit  uint64
		parentGasUsed   uint64
		expectedBaseFee int64
	}{
		{params.InitialBaseFee, 20000000, 10000000, params.InitialBaseFee}, // usage == target
		{params.InitialBaseFee, 20000000, 9000000, 987500000},              // usage below target
		{params.InitialBaseFee, 20000000, 11000000, 1012500000},            // usage above target
		{params.InitialBaseFee, 20000000, 20000000, 1125000000},            // usage full
		{params.InitialBaseFee, 20000000, 0, 875000000},                    // usage 0
	}
	for i, test := range tests {
		parent := &types.Header{
			Number:   big.NewInt(6),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		if have, want := CalcBaseFee(config(), parent), big.NewInt(test.expectedBaseFee); have.Cmp(want) != 0 {
			t.Errorf("test %d: have %d  want %d, ", i, have, want)
		}
	}
}

// TestCalcBaseFeeDelhi assumes all blocks are 1559-blocks and uses
// parameters post Delhi Hard Fork
func TestCalcBaseFeeDelhi(t *testing.T) {
	t.Parallel()

	// Delhi HF kicks in at block 8
	testConfig := copyConfig(config())

	tests := []struct {
		parentBaseFee   int64
		parentGasLimit  uint64
		parentGasUsed   uint64
		expectedBaseFee int64
	}{
		{params.InitialBaseFee, 20000000, 10000000, params.InitialBaseFee}, // usage == target
		{params.InitialBaseFee, 20000000, 9000000, 993750000},              // usage below target
		{params.InitialBaseFee, 20000000, 11000000, 1006250000},            // usage above target
		{params.InitialBaseFee, 20000000, 20000000, 1062500000},            // usage full
		{params.InitialBaseFee, 20000000, 0, 937500000},                    // usage 0

	}
	for i, test := range tests {
		parent := &types.Header{
			Number:   big.NewInt(8),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		if have, want := CalcBaseFee(testConfig, parent), big.NewInt(test.expectedBaseFee); have.Cmp(want) != 0 {
			t.Errorf("test %d: have %d  want %d, ", i, have, want)
		}
	}
}

// TestCalcBaseFeeBhilai assumes all blocks are 1559-blocks and uses
// parameters post Bhilai Hard Fork
func TestCalcBaseFeeBhilai(t *testing.T) {
	t.Parallel()

	// Bhilai HF kicks in at block 8
	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)

	tests := []struct {
		parentBaseFee   int64
		parentGasLimit  uint64
		parentGasUsed   uint64
		expectedBaseFee int64
	}{
		{params.InitialBaseFee, 20000000, 10000000, params.InitialBaseFee}, // usage == target
		{params.InitialBaseFee, 20000000, 9000000, 998437500},              // usage below target
		{params.InitialBaseFee, 20000000, 11000000, 1001562500},            // usage above target
		{params.InitialBaseFee, 20000000, 20000000, 1015625000},            // usage full
		{params.InitialBaseFee, 20000000, 0, 984375000},                    // usage 0

	}
	for i, test := range tests {
		parent := &types.Header{
			Number:   big.NewInt(8),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		if have, want := CalcBaseFee(testConfig, parent), big.NewInt(test.expectedBaseFee); have.Cmp(want) != 0 {
			t.Errorf("test %d: have %d  want %d, ", i, have, want)
		}
	}
}

func TestCalcParentGasTarget(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	defaultGasLimit := uint64(60_000_000)

	t.Run("gas target calculation pre dandeli HF", func(t *testing.T) {
		block := &types.Header{
			Number:   big.NewInt(9),
			GasLimit: defaultGasLimit,
			GasUsed:  defaultGasLimit / 2,
			BaseFee:  big.NewInt(params.InitialBaseFee),
		}
		gasTarget := calcParentGasTarget(testConfig, block)
		expected := block.GasLimit / 2 // because elasticity multiplier is set to 2 by default
		require.Equal(t, expected, gasTarget, "expected gas target = gaslimit/2")
	})

	t.Run("gas target calculation post dandeli HF", func(t *testing.T) {
		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: defaultGasLimit,
			GasUsed:  defaultGasLimit / 2,
			BaseFee:  big.NewInt(params.InitialBaseFee),
		}
		gasTarget := calcParentGasTarget(testConfig, block)
		expected := block.GasLimit * params.TargetGasPercentagePostDandeli / 100 // because gas target is derived by this protocol parameter
		require.Equal(t, expected, gasTarget, "case #1: expected gas target = 60 percent of gas limit")

		block = &types.Header{
			Number:   big.NewInt(21),
			GasLimit: defaultGasLimit,
			GasUsed:  defaultGasLimit / 2,
			BaseFee:  big.NewInt(params.InitialBaseFee),
		}
		gasTarget = calcParentGasTarget(testConfig, block)
		expected = block.GasLimit * params.TargetGasPercentagePostDandeli / 100 // because gas target is derived by this protocol parameter
		require.Equal(t, expected, gasTarget, "case #2: expected gas target = 60 percent of gas limit")
	})

	t.Run("nil bor config", func(t *testing.T) {
		testConfig.Bor = nil
		block := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: defaultGasLimit,
			GasUsed:  defaultGasLimit / 2,
			BaseFee:  big.NewInt(params.InitialBaseFee),
		}
		gasTarget := calcParentGasTarget(testConfig, block)
		expected := block.GasLimit / 2 // because elasticity multiplier is set to 2 by default
		require.Equal(t, expected, gasTarget, "expected gas target = gaslimit/2 when bor config is nil")
	})
}

// simpleBaseFeeCalculator contains an overly simplified logic of base fee calculations useful for generating
// expected values in test cases. It assumes all blocks are post-bhilai HF.
func simpleBaseFeeCalculator(initialBaseFee int64, gasLimit, gasUsed uint64, targetGasPercentage uint64) uint64 {
	initial := big.NewInt(initialBaseFee)
	val := big.NewInt(1)
	val.Mul(val, initial)

	// Assuming tests are running post bhilai
	bfd := int64(params.BaseFeeChangeDenominatorPostBhilai)

	// Define a target gas based on given percentage
	target := gasLimit * targetGasPercentage / 100
	if gasUsed == target {
		return initial.Uint64()
	}

	// follow the simple formula to get the new base fee:
	// base fee = initialBaseFee +/- (initialBaseFee * gasUsedDelta / gasTarget / baseFeeChangeDenominator)

	var delta int64
	if gasUsed > target {
		delta = int64(gasUsed - target)
	} else {
		delta = int64(target - gasUsed)
	}

	val.Mul(val, big.NewInt(delta))
	val.Div(val, big.NewInt(bfd))
	val.Div(val, big.NewInt(int64(target)))

	if gasUsed > target {
		return initial.Add(initial, val).Uint64()
	} else {
		return initial.Sub(initial, val).Uint64()
	}
}

func TestCalcBaseFeeDandeli(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	// Case 1: Create pre-dandeli cases where HF is defined in future. Validate
	// base fee calculations before HF kicks in. Base fee should be calculated
	// based on default elasticity multiplier.
	tests := []struct {
		name            string
		parentBaseFee   int64
		parentGasLimit  uint64
		parentGasUsed   uint64
		expectedBaseFee uint64
	}{
		{"usage == target", params.InitialBaseFee, 60_000_000, 30_000_000, params.InitialBaseFee},
		{"usage below target #1", params.InitialBaseFee, 60_000_000, 20_000_000, 994791667},
		{"usage below target #2", params.InitialBaseFee, 60_000_000, 10_000_000, 989583334},
		{"usage above target #1", params.InitialBaseFee, 60_000_000, 40_000_000, 1005208333},
		{"usage above target #2", params.InitialBaseFee, 60_000_000, 50_000_000, 1010416666},
		{"usage full", params.InitialBaseFee, 60_000_000, 60_000_000, 1015625000},
		{"usage 0", params.InitialBaseFee, 60_000_000, 0, 984375000},
	}
	for _, test := range tests {
		block := &types.Header{
			Number:   big.NewInt(8),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee := simpleBaseFeeCalculator(block.BaseFee.Int64(), block.GasLimit, block.GasUsed, params.DefaultTargetGasPercentage)
		require.Equal(
			t,
			expectedBaseFee,
			baseFee,
			fmt.Sprintf("pre-dandeli base fee mismatch with expected value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)
		// Also check with manually calculated base fee
		require.Equal(
			t,
			test.expectedBaseFee,
			baseFee,
			fmt.Sprintf("pre-dandeli base fee mismatch with manually calculated value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)
	}

	// Case 2: Create post-dandeli cases where HF has kicked in. Validate base fee changes
	// based on the newly introduced protocol param: TargetGasPrecentage. Target gas limit
	// should be calculated based on this percentage value out of total gas limit. Base
	// fee should be changed accordingly.
	tests = []struct {
		name            string
		parentBaseFee   int64
		parentGasLimit  uint64
		parentGasUsed   uint64
		expectedBaseFee uint64
	}{
		{"usage == target (65%)", params.InitialBaseFee, 60_000_000, 39_000_000, params.InitialBaseFee},
		{"usage below target #1", params.InitialBaseFee, 60_000_000, 30_000_000, 996394231},
		{"usage below target #2", params.InitialBaseFee, 60_000_000, 10_000_000, 988381411},
		{"usage above target #1", params.InitialBaseFee, 60_000_000, 40_000_000, 1000400641},
		{"usage above target #2", params.InitialBaseFee, 60_000_000, 50_000_000, 1004407051},
		{"usage full", params.InitialBaseFee, 60_000_000, 60_000_000, 1008413461},
		{"usage 0", params.InitialBaseFee, 60_000_000, 0, 984375000},
	}
	for _, test := range tests {
		// Post-dandeli block #1
		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee := simpleBaseFeeCalculator(block.BaseFee.Int64(), block.GasLimit, block.GasUsed, params.TargetGasPercentagePostDandeli)
		require.Equal(
			t,
			expectedBaseFee,
			baseFee,
			fmt.Sprintf("post-dandeli #1: base fee mismatch with expected value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)
		// Also check with manually calculated base fee
		require.Equal(
			t,
			test.expectedBaseFee,
			baseFee,
			fmt.Sprintf("post-dandeli #1: base fee mismatch with manually calculated value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)

		// Post-dandeli block #2
		block = &types.Header{
			Number:   big.NewInt(21),
			GasLimit: test.parentGasLimit,
			GasUsed:  test.parentGasUsed,
			BaseFee:  big.NewInt(test.parentBaseFee),
		}
		baseFee = CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee = simpleBaseFeeCalculator(block.BaseFee.Int64(), block.GasLimit, block.GasUsed, params.TargetGasPercentagePostDandeli)
		require.Equal(
			t,
			expectedBaseFee,
			baseFee,
			fmt.Sprintf("post-dandeli #2: base fee mismatch with expected value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)
		// Also check with manually calculated base fee
		require.Equal(
			t,
			test.expectedBaseFee,
			baseFee,
			fmt.Sprintf("post-dandeli #2: base fee mismatch with manually calculated value, test: %s, got: %d, want: %d", test.name, baseFee, expectedBaseFee),
		)
	}
}

// TestDynamicTargetGasPercentage verifies that the TargetGasPercentage parameter
// can be dynamically set after Dandeli HF and affects base fee calculations correctly
func TestDynamicTargetGasPercentage(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	// Test with 70% target gas percentage
	targetGasPercentage70 := uint64(70)
	testConfig.Bor.TargetGasPercentage = &targetGasPercentage70

	gasLimit := uint64(60_000_000)
	initialBaseFee := int64(params.InitialBaseFee)

	t.Run("70% target gas percentage", func(t *testing.T) {
		// When gas used equals 70% of gas limit, base fee should stay the same
		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: gasLimit,
			GasUsed:  42_000_000, // 70% of 60M
			BaseFee:  big.NewInt(initialBaseFee),
		}
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "base fee should remain unchanged at target")

		// When gas used is below target (50%), base fee should decrease
		block.GasUsed = 30_000_000 // 50% of 60M
		baseFee = CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee := simpleBaseFeeCalculator(initialBaseFee, gasLimit, block.GasUsed, targetGasPercentage70)
		require.Equal(t, expectedBaseFee, baseFee, "base fee should decrease when below target")
		require.Less(t, baseFee, uint64(initialBaseFee), "base fee should be less than initial")

		// When gas used is above target (90%), base fee should increase
		block.GasUsed = 54_000_000 // 90% of 60M
		baseFee = CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee = simpleBaseFeeCalculator(initialBaseFee, gasLimit, block.GasUsed, targetGasPercentage70)
		require.Equal(t, expectedBaseFee, baseFee, "base fee should increase when above target")
		require.Greater(t, baseFee, uint64(initialBaseFee), "base fee should be greater than initial")
	})

	// Change target gas percentage to 50%
	targetGasPercentage50 := uint64(50)
	testConfig.Bor.TargetGasPercentage = &targetGasPercentage50

	t.Run("50% target gas percentage - same run different value", func(t *testing.T) {
		// When gas used equals 50% of gas limit, base fee should stay the same
		block := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: gasLimit,
			GasUsed:  30_000_000, // 50% of 60M
			BaseFee:  big.NewInt(initialBaseFee),
		}
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "base fee should remain unchanged at new target")

		// When gas used is below new target (40%), base fee should decrease
		block.GasUsed = 24_000_000 // 40% of 60M
		baseFee = CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee := simpleBaseFeeCalculator(initialBaseFee, gasLimit, block.GasUsed, targetGasPercentage50)
		require.Equal(t, expectedBaseFee, baseFee, "base fee should decrease when below new target")
		require.Less(t, baseFee, uint64(initialBaseFee), "base fee should be less than initial")

		// When gas used is above new target (70%), base fee should increase
		block.GasUsed = 42_000_000 // 70% of 60M
		baseFee = CalcBaseFee(testConfig, block).Uint64()
		expectedBaseFee = simpleBaseFeeCalculator(initialBaseFee, gasLimit, block.GasUsed, targetGasPercentage50)
		require.Equal(t, expectedBaseFee, baseFee, "base fee should increase when above new target")
		require.Greater(t, baseFee, uint64(initialBaseFee), "base fee should be greater than initial")
	})

	t.Run("nil target gas percentage falls back to default", func(t *testing.T) {
		testConfig.Bor.TargetGasPercentage = nil
		block := &types.Header{
			Number:   big.NewInt(22),
			GasLimit: gasLimit,
			GasUsed:  39_000_000, // 65% of 60M (default is 65%)
			BaseFee:  big.NewInt(initialBaseFee),
		}
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "base fee should remain unchanged at default target")
	})
}

// TestDynamicBaseFeeChangeDenominator verifies that the BaseFeeChangeDenominator parameter
// can be dynamically set after Dandeli HF and affects the rate of base fee change correctly
func TestDynamicBaseFeeChangeDenominator(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	gasLimit := uint64(60_000_000)
	initialBaseFee := int64(params.InitialBaseFee)
	targetGasPercentage := uint64(params.TargetGasPercentagePostDandeli)

	// Test with denominator of 32 (slower changes)
	denominator32 := uint64(32)
	testConfig.Bor.BaseFeeChangeDenominator = &denominator32

	t.Run("denominator 32 - slower base fee changes", func(t *testing.T) {
		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: gasLimit,
			GasUsed:  50_000_000, // Above target
			BaseFee:  big.NewInt(initialBaseFee),
		}

		baseFee := CalcBaseFee(testConfig, block).Uint64()

		// Calculate expected with custom denominator
		target := gasLimit * targetGasPercentage / 100
		gasUsedDelta := block.GasUsed - target
		expectedIncrease := new(big.Int).Mul(big.NewInt(initialBaseFee), big.NewInt(int64(gasUsedDelta)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(target)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(denominator32)))
		expectedBaseFee := new(big.Int).Add(big.NewInt(initialBaseFee), expectedIncrease).Uint64()

		require.Equal(t, expectedBaseFee, baseFee, "base fee should change according to denominator 32")
	})

	// Change denominator to 16 (faster changes) in the same run
	denominator16 := uint64(16)
	testConfig.Bor.BaseFeeChangeDenominator = &denominator16

	t.Run("denominator 16 - faster base fee changes - same run different value", func(t *testing.T) {
		block := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: gasLimit,
			GasUsed:  50_000_000, // Same gas used as before
			BaseFee:  big.NewInt(initialBaseFee),
		}

		baseFee := CalcBaseFee(testConfig, block).Uint64()

		// Calculate expected with new denominator (should result in larger change)
		target := gasLimit * targetGasPercentage / 100
		gasUsedDelta := block.GasUsed - target
		expectedIncrease := new(big.Int).Mul(big.NewInt(initialBaseFee), big.NewInt(int64(gasUsedDelta)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(target)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(denominator16)))
		expectedBaseFee := new(big.Int).Add(big.NewInt(initialBaseFee), expectedIncrease).Uint64()

		require.Equal(t, expectedBaseFee, baseFee, "base fee should change more with denominator 16")
	})

	t.Run("nil denominator falls back to Bhilai default (64)", func(t *testing.T) {
		testConfig.Bor.BaseFeeChangeDenominator = nil
		block := &types.Header{
			Number:   big.NewInt(22),
			GasLimit: gasLimit,
			GasUsed:  50_000_000,
			BaseFee:  big.NewInt(initialBaseFee),
		}

		baseFee := CalcBaseFee(testConfig, block).Uint64()

		// Should use Bhilai denominator (64)
		target := gasLimit * targetGasPercentage / 100
		gasUsedDelta := block.GasUsed - target
		expectedIncrease := new(big.Int).Mul(big.NewInt(initialBaseFee), big.NewInt(int64(gasUsedDelta)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(target)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(params.BaseFeeChangeDenominatorPostBhilai)))
		expectedBaseFee := new(big.Int).Add(big.NewInt(initialBaseFee), expectedIncrease).Uint64()

		require.Equal(t, expectedBaseFee, baseFee, "base fee should use Bhilai default denominator")
	})
}

// TestVerifyEIP1559HeaderNoBaseFeeValidation tests post-Dandeli boundary validation
// instead of strict validation. Base fees must be within MaxBaseFeeChangePercent boundary.
func TestVerifyEIP1559HeaderNoBaseFeeValidation(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(20)
	testConfig.Bor.BhilaiBlock = big.NewInt(5)

	parent := &types.Header{
		Number:   big.NewInt(20),
		GasLimit: 30_000_000,
		GasUsed:  15_000_000,
		BaseFee:  big.NewInt(1_000_000_000),
	}

	t.Run("accepts base fee within boundary", func(t *testing.T) {
		// Header with a base fee that doesn't match calculated value but is within MaxBaseFeeChangePercent boundary
		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  big.NewInt(1_040_000_000), // 4% increase - within boundary
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.NoError(t, err, "should accept header with base fee within boundary")
	})

	t.Run("accepts base fee different from calculated but within boundary", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)

		// Use a different base fee that's still within MaxBaseFeeChangePercent of parent
		differentBaseFee := new(big.Int).Mul(parent.BaseFee, big.NewInt(103))
		differentBaseFee.Div(differentBaseFee, big.NewInt(100)) // 3% increase

		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  differentBaseFee,
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.NoError(t, err, "should accept header with base fee within boundary even if different from calculated: calculated=%s, header=%s", calculatedBaseFee, differentBaseFee)
	})

	t.Run("rejects base fee exceeding boundary", func(t *testing.T) {
		// Base fee that exceeds MaxBaseFeeChangePercent boundary
		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  big.NewInt(1_100_000_000), // 10% increase - exceeds boundary
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.Error(t, err, "should reject header with base fee exceeding boundary")
		require.Contains(t, err.Error(), "baseFee change exceeds", "error should mention boundary exceeded")
	})

	t.Run("rejects nil base fee", func(t *testing.T) {
		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  nil, // Nil base fee should still be rejected
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.Error(t, err, "should reject header with nil base fee")
		require.Contains(t, err.Error(), "baseFee", "error should mention baseFee")
	})

	t.Run("accepts zero base fee when parent is also zero", func(t *testing.T) {
		parentZero := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: 30_000_000,
			GasUsed:  15_000_000,
			BaseFee:  big.NewInt(0),
		}

		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  big.NewInt(0), // Zero is valid when parent is also zero
		}

		err := VerifyEIP1559Header(testConfig, parentZero, header)
		require.NoError(t, err, "should accept header with zero base fee when parent is zero")
	})
}

// TestInvalidTargetGasPercentage tests that invalid TargetGasPercentage values
// fall back to defaults and don't cause panics
func TestInvalidTargetGasPercentage(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	gasLimit := uint64(60_000_000)
	initialBaseFee := int64(params.InitialBaseFee)

	t.Run("zero target gas percentage falls back to default", func(t *testing.T) {
		zeroValue := uint64(0)
		testConfig.Bor.TargetGasPercentage = &zeroValue

		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: gasLimit,
			GasUsed:  39_000_000, // 65% of 60M (default target)
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should not panic and should use default (65%)
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "should use default target and base fee unchanged")
	})

	t.Run("target gas percentage > 100 falls back to default", func(t *testing.T) {
		invalidValue := uint64(150)
		testConfig.Bor.TargetGasPercentage = &invalidValue

		block := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: gasLimit,
			GasUsed:  39_000_000, // 65% of 60M (default target)
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should not panic and should use default (65%)
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "should use default target and base fee unchanged")
	})

	t.Run("valid edge case: 1% target gas percentage", func(t *testing.T) {
		onePercent := uint64(1)
		testConfig.Bor.TargetGasPercentage = &onePercent

		block := &types.Header{
			Number:   big.NewInt(22),
			GasLimit: gasLimit,
			GasUsed:  600_000, // 1% of 60M
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should work with 1% target
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "should accept 1% as valid target")
	})

	t.Run("valid edge case: 100% target gas percentage", func(t *testing.T) {
		hundredPercent := uint64(100)
		testConfig.Bor.TargetGasPercentage = &hundredPercent

		block := &types.Header{
			Number:   big.NewInt(23),
			GasLimit: gasLimit,
			GasUsed:  gasLimit, // 100% of gas limit
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should work with 100% target
		baseFee := CalcBaseFee(testConfig, block).Uint64()
		require.Equal(t, uint64(initialBaseFee), baseFee, "should accept 100% as valid target")
	})
}

// TestInvalidBaseFeeChangeDenominator tests that invalid BaseFeeChangeDenominator values
// fall back to defaults and don't cause panics (especially division by zero)
func TestInvalidBaseFeeChangeDenominator(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.BhilaiBlock = big.NewInt(8)
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	gasLimit := uint64(60_000_000)
	initialBaseFee := int64(params.InitialBaseFee)

	t.Run("zero denominator falls back to default", func(t *testing.T) {
		zeroDenom := uint64(0)
		testConfig.Bor.BaseFeeChangeDenominator = &zeroDenom

		block := &types.Header{
			Number:   big.NewInt(20),
			GasLimit: gasLimit,
			GasUsed:  50_000_000, // Above target
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should not panic (no division by zero) and use Bhilai default (64)
		baseFee := CalcBaseFee(testConfig, block)
		require.NotNil(t, baseFee, "should calculate base fee without panic")

		// Verify it used default denominator (64) by checking the change is small
		target := gasLimit * params.TargetGasPercentagePostDandeli / 100
		gasUsedDelta := block.GasUsed - target
		expectedIncrease := new(big.Int).Mul(big.NewInt(initialBaseFee), big.NewInt(int64(gasUsedDelta)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(target)))
		expectedIncrease.Div(expectedIncrease, big.NewInt(int64(params.BaseFeeChangeDenominatorPostBhilai)))
		expectedBaseFee := new(big.Int).Add(big.NewInt(initialBaseFee), expectedIncrease)

		require.Equal(t, expectedBaseFee.Uint64(), baseFee.Uint64(), "should use Bhilai default denominator (64)")
	})

	t.Run("valid edge case: denominator = 1 (extreme volatility)", func(t *testing.T) {
		extremeDenom := uint64(1)
		testConfig.Bor.BaseFeeChangeDenominator = &extremeDenom

		block := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: gasLimit,
			GasUsed:  50_000_000,
			BaseFee:  big.NewInt(initialBaseFee),
		}

		// Should work but produce large changes
		baseFee := CalcBaseFee(testConfig, block)
		require.NotNil(t, baseFee, "should handle denominator of 1")
		require.Greater(t, baseFee.Uint64(), uint64(initialBaseFee), "base fee should increase significantly with denominator 1")
	})
}

// TestBaseFeeValidationPreDandeli tests that base fee validation still works before Dandeli HF
func TestBaseFeeValidationPreDandeli(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	parent := &types.Header{
		Number:   big.NewInt(10), // Pre-Dandeli
		GasLimit: 30_000_000,
		GasUsed:  15_000_000,
		BaseFee:  big.NewInt(1_000_000_000),
	}

	t.Run("pre-Dandeli: rejects incorrect base fee", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)
		incorrectBaseFee := new(big.Int).Mul(calculatedBaseFee, big.NewInt(2))

		header := &types.Header{
			Number:   big.NewInt(11),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  incorrectBaseFee, // Wrong base fee
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.Error(t, err, "should reject incorrect base fee pre-Dandeli")
		require.Contains(t, err.Error(), "invalid baseFee", "error should mention invalid baseFee")
	})

	t.Run("pre-Dandeli: accepts correct base fee", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)

		header := &types.Header{
			Number:   big.NewInt(11),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  calculatedBaseFee, // Correct base fee
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.NoError(t, err, "should accept correct base fee pre-Dandeli")
	})

	t.Run("post-Dandeli: accepts base fee within boundary", func(t *testing.T) {
		parent := &types.Header{
			Number:   big.NewInt(20), // Post-Dandeli
			GasLimit: 30_000_000,
			GasUsed:  15_000_000,
			BaseFee:  big.NewInt(1_000_000_000),
		}

		calculatedBaseFee := CalcBaseFee(testConfig, parent)
		// Use a base fee within MaxBaseFeeChangePercent boundary (3% increase)
		baseFeeWithinBoundary := new(big.Int).Mul(parent.BaseFee, big.NewInt(103))
		baseFeeWithinBoundary.Div(baseFeeWithinBoundary, big.NewInt(100))

		header := &types.Header{
			Number:   big.NewInt(21),
			GasLimit: 30_000_000,
			GasUsed:  20_000_000,
			BaseFee:  baseFeeWithinBoundary, // Within boundary
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.NoError(t, err, "should accept base fee within boundary post-Dandeli: calculated=%s, header=%s", calculatedBaseFee, baseFeeWithinBoundary)
	})
}
