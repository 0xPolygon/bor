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

// VerifyEIP1559Header verifies some header attributes which were changed in EIP-1559,
// - gas limit check
// - basefee check
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
	// Verify the baseFee is correct based on the parent header.
	expectedBaseFee := CalcBaseFee(config, parent)
	if header.BaseFee.Cmp(expectedBaseFee) != 0 {
		return fmt.Errorf("invalid baseFee: have %s, want %s, parentBaseFee %s, parentGasUsed %d",
			header.BaseFee, expectedBaseFee, parent.BaseFee, parent.GasUsed)
	}

	return nil
}

// CalcBaseFee calculates the basefee of the header.
func CalcBaseFee(config *params.ChainConfig, parent *types.Header) *big.Int {
	// If the current block is the first EIP-1559 block, return the InitialBaseFee.
	if !config.IsLondon(parent.Number) {
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
		baseFeeChangeDenominatorUint64 = baseFeeChangeDenominator(config, parent.Number)
	)

	if parent.GasUsed > parentGasTarget {
		// If the parent block used more gas than its target, the baseFee should increase.
		// max(1, parentBaseFee * gasUsedDelta / parentGasTarget / baseFeeChangeDenominator)
		num.SetUint64(parent.GasUsed - parentGasTarget)
		num.Mul(num, parent.BaseFee)
		num.Div(num, denom.SetUint64(parentGasTarget))
		num.Div(num, denom.SetUint64(baseFeeChangeDenominatorUint64))
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

		baseFee := num.Sub(parent.BaseFee, num)
		if baseFee.Cmp(common.Big0) < 0 {
			baseFee = common.Big0
		}
		return baseFee
	}
}

// baseFeeChangeDenominator returns the denominator used to bound the amount the base fee can change between blocks.
// The value varies based on the hard fork:
// - Pre-Delhi: 8 (default)
// - Post-Delhi: 16
// - Post-Bhilai: 64
// If borConfig is nil, returns the default value of 8.
func baseFeeChangeDenominator(config *params.ChainConfig, number *big.Int) uint64 {
	if config.Bor == nil {
		return params.DefaultBaseFeeChangeDenominator
	}
	if config.Bor.IsBhilai(number) {
		return params.BaseFeeChangeDenominatorPostBhilai
	} else if config.Bor.IsDelhi(number) {
		return params.BaseFeeChangeDenominatorPostDelhi
	} else {
		return params.DefaultBaseFeeChangeDenominator
	}
}

// calcParentGasTarget calculates the target gas based on parent block gas limit. Earlier
// it was derived by `ElasticityMultiplier` as it had an integer multiplier value. Post
// dandeli HF, a percentage value will be used to calculate the gas target.
func calcParentGasTarget(config *params.ChainConfig, parent *types.Header) uint64 {
	if config.Bor != nil && config.Bor.IsDandeli(parent.Number) {
		return parent.GasLimit * params.TargetGasPercentagePostDandeli / 100
	}
	return parent.GasLimit / config.ElasticityMultiplier()
}
