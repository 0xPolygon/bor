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
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBaseFeeBoundary tests the MaxBaseFeeChangePercent base fee change limit post-Dandeli.
// It verifies both the CalcBaseFee capping and VerifyEIP1559Header validation.
func TestBaseFeeBoundary(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(10)
	testConfig.Bor.BhilaiBlock = big.NewInt(5)

	tests := []struct {
		name          string
		blockNumber   uint64
		parentBaseFee *big.Int
		headerBaseFee *big.Int
		expectError   bool
		errorContains string
	}{
		{
			name:          "Post-Dandeli: Increase at 3% (within boundary)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(1030000000), // 3% increase
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Increase at 5% (exactly at boundary)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(1050000000), // 5% increase
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Increase exceeding boundary (7% increase)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(1070000000), // 7% increase
			expectError:   true,
			errorContains: "baseFee change exceeds",
		},
		{
			name:          "Post-Dandeli: Decrease at 3% (within boundary)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(970000000), // 3% decrease
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Decrease at 5% (exactly at boundary)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(950000000), // 5% decrease
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Decrease exceeding boundary (7% decrease)",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(930000000), // 7% decrease
			expectError:   true,
			errorContains: "baseFee change exceeds",
		},
		{
			name:          "Post-Dandeli: No change",
			blockNumber:   15,
			parentBaseFee: big.NewInt(1000000000),
			headerBaseFee: big.NewInt(1000000000), // 0% change
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Large base fee with 2% increase",
			blockNumber:   15,
			parentBaseFee: big.NewInt(100000000000), // 100 gwei
			headerBaseFee: big.NewInt(102000000000), // 2% increase
			expectError:   false,
		},
		{
			name:          "Post-Dandeli: Small base fee with 4% increase",
			blockNumber:   15,
			parentBaseFee: big.NewInt(100000000), // 0.1 gwei
			headerBaseFee: big.NewInt(104000000), // 4% increase
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := &types.Header{
				Number:   big.NewInt(int64(tt.blockNumber - 1)),
				GasLimit: 30000000,
				GasUsed:  15000000,
				BaseFee:  tt.parentBaseFee,
			}

			header := &types.Header{
				Number:   big.NewInt(int64(tt.blockNumber)),
				GasLimit: 30000000,
				GasUsed:  20000000,
				BaseFee:  tt.headerBaseFee,
			}

			err := VerifyEIP1559Header(testConfig, parent, header)

			if tt.expectError {
				require.Error(t, err, "Expected error for %s", tt.name)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err, "Expected no error for %s", tt.name)
			}
		})
	}
}

// TestBaseFeeBoundaryPreDandeli tests that pre-Dandeli blocks still use strict validation
func TestBaseFeeBoundaryPreDandeli(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(20)

	parent := &types.Header{
		Number:   big.NewInt(10), // Pre-Dandeli
		GasLimit: 30000000,
		GasUsed:  15000000,
		BaseFee:  big.NewInt(1000000000),
	}

	t.Run("pre-Dandeli: exact match passes", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)

		header := &types.Header{
			Number:   big.NewInt(11),
			GasLimit: 30000000,
			GasUsed:  20000000,
			BaseFee:  calculatedBaseFee, // Exact match
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.NoError(t, err)
	})

	t.Run("pre-Dandeli: off by 1 wei fails", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)
		offByOne := new(big.Int).Add(calculatedBaseFee, big.NewInt(1))

		header := &types.Header{
			Number:   big.NewInt(11),
			GasLimit: 30000000,
			GasUsed:  20000000,
			BaseFee:  offByOne, // Off by 1 wei
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid baseFee")
	})

	t.Run("pre-Dandeli: 3% difference fails", func(t *testing.T) {
		calculatedBaseFee := CalcBaseFee(testConfig, parent)
		threePercent := new(big.Int).Mul(calculatedBaseFee, big.NewInt(103))
		threePercent.Div(threePercent, big.NewInt(100))

		header := &types.Header{
			Number:   big.NewInt(11),
			GasLimit: 30000000,
			GasUsed:  20000000,
			BaseFee:  threePercent, // 3% different
		}

		err := VerifyEIP1559Header(testConfig, parent, header)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid baseFee")
	})
}

// TestAggressiveParametersExceedBoundary tests that aggressive parameter configurations
// (low denominators) are properly capped at MaxBaseFeeChangePercent by CalcBaseFee post-Dandeli
func TestAggressiveParametersExceedBoundary(t *testing.T) {
	t.Parallel()

	testConfig := copyConfig(config())
	testConfig.Bor.DandeliBlock = big.NewInt(10)
	testConfig.Bor.BhilaiBlock = big.NewInt(5)

	tests := []struct {
		name                     string
		baseFeeChangeDenominator uint64
		targetGasPercentage      uint64
		parentGasUsed            uint64
		parentGasLimit           uint64
		parentBaseFee            int64
		description              string
	}{
		{
			name:                     "Very aggressive denominator (8) at full usage",
			baseFeeChangeDenominator: 8,        // Very aggressive
			targetGasPercentage:      65,       // Default target
			parentGasUsed:            30000000, // 100% usage
			parentGasLimit:           30000000,
			parentBaseFee:            1000000000, // 1 gwei
			description:              "With denominator=8, full block usage would naturally exceed boundary without cap",
		},
		{
			name:                     "Aggressive denominator (16) at full usage",
			baseFeeChangeDenominator: 16, // Aggressive
			targetGasPercentage:      65,
			parentGasUsed:            30000000, // 100% usage
			parentGasLimit:           30000000,
			parentBaseFee:            1000000000,
			description:              "With denominator=16, full block usage should be capped at boundary",
		},
		{
			name:                     "Very aggressive denominator (8) at zero usage",
			baseFeeChangeDenominator: 8,
			targetGasPercentage:      65,
			parentGasUsed:            0, // 0% usage
			parentGasLimit:           30000000,
			parentBaseFee:            1000000000,
			description:              "With denominator=8, zero usage would naturally exceed boundary without cap",
		},
		{
			name:                     "Aggressive denominator (16) at zero usage",
			baseFeeChangeDenominator: 16,
			targetGasPercentage:      65,
			parentGasUsed:            0, // 0% usage
			parentGasLimit:           30000000,
			parentBaseFee:            1000000000,
			description:              "With denominator=16, zero usage should be capped at boundary",
		},
		{
			name:                     "Extreme denominator (4) at full usage",
			baseFeeChangeDenominator: 4, // Extremely aggressive
			targetGasPercentage:      65,
			parentGasUsed:            30000000,
			parentGasLimit:           30000000,
			parentBaseFee:            1000000000,
			description:              "With denominator=4, should be heavily capped at boundary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := copyConfig(testConfig)
			config.Bor.BaseFeeChangeDenominator = &tt.baseFeeChangeDenominator
			config.Bor.TargetGasPercentage = &tt.targetGasPercentage

			parent := &types.Header{
				Number:   big.NewInt(20), // Post-Dandeli, Post-Bhilai
				GasLimit: tt.parentGasLimit,
				GasUsed:  tt.parentGasUsed,
				BaseFee:  big.NewInt(tt.parentBaseFee),
			}

			calculatedBaseFee := CalcBaseFee(config, parent)

			// Calculate the actual change percentage
			actualChange := new(big.Int)
			if calculatedBaseFee.Cmp(parent.BaseFee) >= 0 {
				actualChange.Sub(calculatedBaseFee, parent.BaseFee)
			} else {
				actualChange.Sub(parent.BaseFee, calculatedBaseFee)
			}

			changePercentBig := new(big.Int).Mul(actualChange, big.NewInt(10000))
			changePercentBig.Div(changePercentBig, parent.BaseFee)
			changePercent := float64(changePercentBig.Int64()) / 100.0

			t.Logf("%s", tt.description)
			t.Logf("  Parent BaseFee: %s wei", parent.BaseFee.String())
			t.Logf("  Calculated BaseFee: %s wei", calculatedBaseFee.String())
			t.Logf("  Change: %.2f%%", changePercent)
			t.Logf("  Parameters: Denominator=%d, TargetGas=%d%%", tt.baseFeeChangeDenominator, tt.targetGasPercentage)

			// Assert that the change is capped at 5%
			assert.LessOrEqual(t, changePercent, float64(MaxBaseFeeChangePercent),
				"Base fee change should be capped at %d%%, got %.2f%%",
				MaxBaseFeeChangePercent, changePercent)

			// Verify that a block with this calculated base fee would be accepted
			header := &types.Header{
				Number:   big.NewInt(21),
				GasLimit: parent.GasLimit,
				GasUsed:  parent.GasUsed,
				BaseFee:  calculatedBaseFee,
			}

			err := VerifyEIP1559Header(config, parent, header)
			require.NoError(t, err, "Block with calculated base fee should be accepted")
		})
	}
}

// TestDefaultParametersWithinBoundary documents and verifies that default post-Dandeli
// parameters stay well within the MaxBaseFeeChangePercent boundary at all gas usage levels
func TestDefaultParametersWithinBoundary(t *testing.T) {
	t.Parallel()

	config := copyConfig(params.TestChainConfig)
	config.LondonBlock = big.NewInt(0)
	config.Bor = &params.BorConfig{
		DandeliBlock: big.NewInt(10),
		BhilaiBlock:  big.NewInt(5), // Critical: Bhilai changes denominator to 64
		// No custom parameters - will use defaults:
		// BaseFeeChangeDenominator = 64 (post-Bhilai)
		// TargetGasPercentage = 65
	}

	baseBaseFee := int64(1000000000) // 1 gwei
	gasLimit := uint64(30000000)     // 30M gas limit

	// Test at various gas usage levels
	tests := []struct {
		name         string
		gasUsed      uint64
		usagePercent string
	}{
		{"0% gas usage (max decrease pressure)", 0, "0%"},
		{"10% gas usage", gasLimit * 10 / 100, "10%"},
		{"25% gas usage", gasLimit * 25 / 100, "25%"},
		{"50% gas usage", gasLimit * 50 / 100, "50%"},
		{"65% gas usage (at target)", gasLimit * 65 / 100, "65%"},
		{"75% gas usage", gasLimit * 75 / 100, "75%"},
		{"90% gas usage", gasLimit * 90 / 100, "90%"},
		{"100% gas usage (max increase pressure)", gasLimit, "100%"},
	}

	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("Default Post-Dandeli Parameters within %d%% Boundary", MaxBaseFeeChangePercent)
	t.Logf("%s", strings.Repeat("=", 80))
	t.Logf("Configuration:")
	t.Logf("  BaseFeeChangeDenominator: 64 (default post-Bhilai)")
	t.Logf("  TargetGasPercentage: 65%% (default)")
	t.Logf("  DandeliBlock: 10")
	t.Logf("  BhilaiBlock: 5")
	t.Logf("")

	maxChangePercent := 0.0

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := &types.Header{
				Number:   big.NewInt(20), // Post-Dandeli, Post-Bhilai
				GasLimit: gasLimit,
				GasUsed:  tt.gasUsed,
				BaseFee:  big.NewInt(baseBaseFee),
			}

			calculatedBaseFee := CalcBaseFee(config, parent)

			// Calculate change percentage
			actualChange := new(big.Int)
			direction := "no change"
			if calculatedBaseFee.Cmp(parent.BaseFee) > 0 {
				actualChange.Sub(calculatedBaseFee, parent.BaseFee)
				direction = "increase"
			} else if calculatedBaseFee.Cmp(parent.BaseFee) < 0 {
				actualChange.Sub(parent.BaseFee, calculatedBaseFee)
				direction = "decrease"
			}

			changePercent := 0.0
			if actualChange.Cmp(big.NewInt(0)) > 0 {
				changePercentBig := new(big.Int).Mul(actualChange, big.NewInt(10000))
				changePercentBig.Div(changePercentBig, parent.BaseFee)
				changePercent = float64(changePercentBig.Int64()) / 100.0
			}

			if changePercent > maxChangePercent {
				maxChangePercent = changePercent
			}

			// Calculate margin below 5% boundary
			marginBelowBoundary := float64(MaxBaseFeeChangePercent) - changePercent
			marginPercent := (marginBelowBoundary / float64(MaxBaseFeeChangePercent)) * 100.0

			t.Logf("Gas Usage: %s → BaseFee %s by %.2f%% (%.1f%% margin below %d%% boundary)",
				tt.usagePercent, direction, changePercent, marginPercent, MaxBaseFeeChangePercent)

			// Assert within boundary
			assert.LessOrEqual(t, changePercent, float64(MaxBaseFeeChangePercent),
				"Change should be within %d%% boundary", MaxBaseFeeChangePercent)

			// Verify header would be accepted
			header := &types.Header{
				Number:   big.NewInt(21),
				GasLimit: gasLimit,
				GasUsed:  tt.gasUsed,
				BaseFee:  calculatedBaseFee,
			}

			err := VerifyEIP1559Header(config, parent, header)
			require.NoError(t, err, "Header should be valid with default parameters")
		})
	}

	t.Logf("")
	t.Logf("Summary:")
	t.Logf("  Maximum change observed: %.2f%%", maxChangePercent)
	t.Logf("  Boundary limit: %d%%", MaxBaseFeeChangePercent)
	t.Logf("  Margin: %.2f%% (%.1f%% of boundary)",
		float64(MaxBaseFeeChangePercent)-maxChangePercent,
		((float64(MaxBaseFeeChangePercent)-maxChangePercent)/float64(MaxBaseFeeChangePercent))*100.0)
	t.Logf("%s", strings.Repeat("=", 80))

	// Assert that default parameters stay well within the 5% boundary
	assert.Less(t, maxChangePercent, float64(MaxBaseFeeChangePercent)*0.5,
		"Default parameters should use less than 50%% of the %d%% boundary", MaxBaseFeeChangePercent)
}
