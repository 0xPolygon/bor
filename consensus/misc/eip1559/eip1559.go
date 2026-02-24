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
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

const (
	// MaxBaseFeeChangePercent limits the maximum base fee change per block to 5% of parent base fee.
	// This prevents excessive fee volatility by capping both increases and decreases.
	// The 5% limit provides protection against aggressive parameter configurations while
	// accommodating the natural behavior of default post-Dandeli parameters (maximum ~1.7% change).
	MaxBaseFeeChangePercent = 5
)

// VerifyEIP1559Header verifies some header attributes which were changed in EIP-1559,
// - gas limit check
// - basefee check with different rules pre/post Lisovo:
//   - Pre-Lisovo: Strict validation (baseFee must exactly match calculated value)
//   - Post-Lisovo: Boundary validation (baseFee change must be within MaxBaseFeeChangePercent)
func VerifyEIP1559Header(config *params.ChainConfig, parent, header *types.Header) error {
	// Verify that the gas limit remains within allowed bounds
	parentGasLimit := parent.GasLimit
	if !config.IsLondon(parent.Number) {
		parentGasLimit = parent.GasLimit * config.ElasticityMultiplier()
	}
	if err := misc.VerifyGaslimit(parentGasLimit, header.GasLimit); err != nil {
		return err
	}
	// Verify the header is not malformed
	if header.BaseFee == nil {
		return errors.New("header is missing baseFee")
	}
	// Verify the parent header is not malformed
	if config.IsLondon(parent.Number) && parent.BaseFee == nil {
		return errors.New("parent header is missing baseFee")
	}
	// Verify the baseFee is correct based on the parent header.

	// Post-Lisovo: Validate that base fee changes are within allowed boundaries
	if config.Bor != nil && config.Bor.IsLisovo(header.Number) {
		return verifyBaseFeeWithinBoundaries(parent, header)
	}

	// Pre-Lisovo: Verify the baseFee is correct based on the parent header
	expectedBaseFee := CalcBaseFee(config, parent)
	if header.BaseFee.Cmp(expectedBaseFee) != 0 {
		return fmt.Errorf("invalid baseFee: have %s, want %s, parentBaseFee %s, parentGasUsed %d",
			header.BaseFee, expectedBaseFee, parent.BaseFee, parent.GasUsed)
	}

	return nil
}

// verifyBaseFeeWithinBoundaries checks that the base fee change is within the allowed boundary.
// This prevents excessive fee volatility while allowing dynamic fee adjustment post-Lisovo.
// The boundary limit is defined by MaxBaseFeeChangePercent constant.
func verifyBaseFeeWithinBoundaries(parent, header *types.Header) error {
	// Calculate the maximum allowed change (MaxBaseFeeChangePercent of parent base fee)
	maxAllowedChange := new(big.Int).Mul(parent.BaseFee, big.NewInt(MaxBaseFeeChangePercent))
	maxAllowedChange.Div(maxAllowedChange, big.NewInt(100))

	// Ensure minimum 1 wei cap to prevent unlimited growth at very low base fees.
	// This matches the logic in CalcBaseFee.
	if maxAllowedChange.Cmp(common.Big1) < 0 {
		maxAllowedChange = new(big.Int).Set(common.Big1)
	}

	// Calculate the actual change in base fee
	actualChange := new(big.Int)
	if header.BaseFee.Cmp(parent.BaseFee) >= 0 {
		// Base fee increased or stayed the same
		actualChange.Sub(header.BaseFee, parent.BaseFee)
	} else {
		// Base fee decreased
		actualChange.Sub(parent.BaseFee, header.BaseFee)
	}

	// Verify the change is within the allowed boundary
	if actualChange.Cmp(maxAllowedChange) > 0 {
		return fmt.Errorf("baseFee change exceeds %d%% limit: change=%s, maxAllowed=%s, parentBaseFee=%s, headerBaseFee=%s",
			MaxBaseFeeChangePercent, actualChange, maxAllowedChange, parent.BaseFee, header.BaseFee)
	}

	return nil
}

// CalcBaseFee calculates the basefee of the header.
func CalcBaseFee(config *params.ChainConfig, parent *types.Header) *big.Int {
	// If the current block is the first EIP-1559 block, return the InitialBaseFee.
	if !config.IsLondon(parent.Number) || parent.BaseFee == nil {
		return new(big.Int).SetUint64(params.InitialBaseFee)
	}

	// Modified for bor to derive gas target by percentage instead of using elasticity multiplier post dandeli HF
	parentGasTarget := calcParentGasTarget(config, parent)
	// If the parent gasUsed is the same as the target, the baseFee remains unchanged.
	if parent.GasUsed == parentGasTarget {
		return new(big.Int).Set(parent.BaseFee)
	}

	var (
		num                            = new(big.Int)
		denom                          = new(big.Int)
		baseFeeChangeDenominatorUint64 = params.BaseFeeChangeDenominator(config.Bor, parent.Number)
	)

	// Calculate maximum allowed change (only applies post-Lisovo)
	var maxAllowedChange *big.Int
	applyBoundaryCap := config.Bor != nil && config.Bor.IsLisovo(parent.Number)
	if applyBoundaryCap {
		maxAllowedChange = new(big.Int).Mul(parent.BaseFee, big.NewInt(MaxBaseFeeChangePercent))
		maxAllowedChange.Div(maxAllowedChange, big.NewInt(100))

		// Ensure minimum 1 wei cap to prevent unlimited growth at very low base fees.
		// When percentage calculation rounds to 0 (baseFee < 20 wei), this ensures
		// there's still an absolute cap of 1 wei per block.
		if maxAllowedChange.Cmp(common.Big1) < 0 {
			maxAllowedChange = new(big.Int).Set(common.Big1)
		}
	}

	if parent.GasUsed > parentGasTarget {
		// If the parent block used more gas than its target, the baseFee should increase.
		// max(1, parentBaseFee * gasUsedDelta / parentGasTarget / baseFeeChangeDenominator)
		num.SetUint64(parent.GasUsed - parentGasTarget)
		num.Mul(num, parent.BaseFee)
		num.Div(num, denom.SetUint64(parentGasTarget))
		num.Div(num, denom.SetUint64(baseFeeChangeDenominatorUint64))

		// Cap the increase to MaxBaseFeeChangePercent post-Lisovo
		if applyBoundaryCap && num.Cmp(maxAllowedChange) > 0 {
			num.Set(maxAllowedChange)
		}

		if num.Cmp(common.Big1) < 0 {
			return num.Add(parent.BaseFee, common.Big1)
		}
		return num.Add(parent.BaseFee, num)
	} else {
		// Otherwise if the parent block used less gas than its target, the baseFee should decrease.
		// max(0, parentBaseFee * gasUsedDelta / parentGasTarget / baseFeeChangeDenominator)
		num.SetUint64(parentGasTarget - parent.GasUsed)
		num.Mul(num, parent.BaseFee)
		num.Div(num, denom.SetUint64(parentGasTarget))
		num.Div(num, denom.SetUint64(baseFeeChangeDenominatorUint64))

		// Cap the decrease to MaxBaseFeeChangePercent post-Lisovo
		if applyBoundaryCap && num.Cmp(maxAllowedChange) > 0 {
			num.Set(maxAllowedChange)
		}

		baseFee := num.Sub(parent.BaseFee, num)
		if baseFee.Cmp(common.Big0) < 0 {
			baseFee = common.Big0
		}
		return baseFee
	}
}

// calcParentGasTarget calculates the target gas based on parent block gas limit. Earlier
// it was derived by `ElasticityMultiplier` as it had an integer multiplier value. Post
// Dandeli HF, a percentage value is used to calculate the gas target (validated with fallback to default).
func calcParentGasTarget(config *params.ChainConfig, parent *types.Header) uint64 {
	if config.Bor != nil && config.Bor.IsDandeli(parent.Number) {
		// Use helper function which validates and provides defaults
		targetPercentage := config.Bor.GetTargetGasPercentage(parent.Number)
		return parent.GasLimit * targetPercentage / 100
	}
	return parent.GasLimit / config.ElasticityMultiplier()
}
