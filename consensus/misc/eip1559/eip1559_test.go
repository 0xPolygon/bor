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
