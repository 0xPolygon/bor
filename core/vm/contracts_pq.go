// Copyright 2026 The go-ethereum Authors
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

package vm

import (
	"crypto/mldsa"
	"math"

	"github.com/cloudflare/circl/sign/slhdsa"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// Post-quantum signature verification precompiles (bor). Both take
// pk || sig || msg, verify in pure mode with the empty context, and always
// return 32 bytes: 1 for a valid signature, 0 for anything else, including
// malformed or short input. They never return an error.
//
// Reviewed implementations: Go's crypto/mldsa (FIPS 204, Go 1.27.3) and
// cloudflare/circl v1.6.5 sign/slhdsa (FIPS 205). Bumping either needs the
// verify path re-reviewed and the vector, mutation and differential tests rerun.
// crypto/mldsa comes from the toolchain that builds bor, so a Go upgrade is a
// consensus change for ML-DSA: every node must build with a reviewed Go.
var (
	mldsa65VerifyAddr        = common.BytesToAddress([]byte{0x01, 0x13})
	slhdsaSha2128sVerifyAddr = common.BytesToAddress([]byte{0x01, 0x15})
)

// PrecompiledContractsPQ contains the post-quantum verification precompiles.
// They are not part of any fork yet; a hard fork must add them to its set.
var PrecompiledContractsPQ = PrecompiledContracts{
	mldsa65VerifyAddr:        &mldsa65Verify{},
	slhdsaSha2128sVerifyAddr: &slhdsaSha2128sVerify{},
}

const (
	mldsa65InputPrefix = mldsa.MLDSA65PublicKeySize + mldsa.MLDSA65SignatureSize

	slhdsaSha2128sPublicKeySize = 32
	slhdsaSha2128sSignatureSize = 7856
	slhdsaSha2128sInputPrefix   = slhdsaSha2128sPublicKeySize + slhdsaSha2128sSignatureSize
)

// pqVerifyGas returns base plus perWord for every 32-byte word of message
// after the fixed pk || sig prefix. It saturates instead of overflowing.
func pqVerifyGas(inputLen, fixed int, base, perWord uint64) uint64 {
	if inputLen <= fixed {
		return base
	}
	words := toWordSize(uint64(inputLen - fixed))
	if words > (math.MaxUint64-base)/perWord {
		return math.MaxUint64
	}
	return base + words*perWord
}

// mldsa65Verify implements ML-DSA-65 verification (FIPS 204) with the
// EIP-8355 input layout.
type mldsa65Verify struct{}

func (c *mldsa65Verify) RequiredGas(input []byte) uint64 {
	return pqVerifyGas(len(input), mldsa65InputPrefix, params.MLDSA65VerifyBaseGas, params.MLDSAVerifyPerWordGas)
}

func (c *mldsa65Verify) Run(input []byte) ([]byte, error) {
	if len(input) < mldsa65InputPrefix {
		return false32Byte, nil
	}
	pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), input[:mldsa.MLDSA65PublicKeySize])
	if err != nil {
		return false32Byte, nil
	}
	if mldsa.Verify(pk, input[mldsa65InputPrefix:], input[mldsa.MLDSA65PublicKeySize:mldsa65InputPrefix], nil) != nil {
		return false32Byte, nil
	}
	return true32Byte, nil
}

func (c *mldsa65Verify) Name() string {
	return "MLDSA65VERIFY"
}

// slhdsaSha2128sVerify implements SLH-DSA-SHA2-128s verification (FIPS 205)
// with the same input layout as mldsa65Verify.
type slhdsaSha2128sVerify struct{}

func (c *slhdsaSha2128sVerify) RequiredGas(input []byte) uint64 {
	return pqVerifyGas(len(input), slhdsaSha2128sInputPrefix, params.SLHDSASha2128sVerifyBaseGas, params.SLHDSAVerifyPerWordGas)
}

func (c *slhdsaSha2128sVerify) Run(input []byte) ([]byte, error) {
	if len(input) < slhdsaSha2128sInputPrefix {
		return false32Byte, nil
	}
	// The ID must be set before unmarshalling: circl panics on an unknown one.
	pk := slhdsa.PublicKey{ID: slhdsa.SHA2_128s}
	if pk.UnmarshalBinary(input[:slhdsaSha2128sPublicKeySize]) != nil {
		return false32Byte, nil
	}
	sig := input[slhdsaSha2128sPublicKeySize:slhdsaSha2128sInputPrefix]
	if !slhdsa.Verify(&pk, slhdsa.NewMessage(input[slhdsaSha2128sInputPrefix:]), sig, nil) {
		return false32Byte, nil
	}
	return true32Byte, nil
}

func (c *slhdsaSha2128sVerify) Name() string {
	return "SLHDSASHA2128SVERIFY"
}
