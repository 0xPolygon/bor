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
	"crypto/sha256"

	"github.com/ethereum/go-ethereum/common"
)

// pqVerifyCacheCap bounds the post-quantum verify cache. Every call costs at
// least MLDSA65VerifyBaseGas, so a block's gas limit already bounds the calls;
// the cap bounds writes from discarded speculative executions.
const pqVerifyCacheCap = 8192

// pqVerifyCacheKey identifies a verification by precompile address and the
// SHA-256 of its whole input. A collision would return a wrong result, so the
// hash must be collision resistant; truncating it is not allowed.
type pqVerifyCacheKey struct {
	addr  common.Address
	input common.Hash
}

// PQVerifyCache memoizes the ML-DSA-65 and SLH-DSA-SHA2-128s verify
// precompiles. A verification costs 80-200µs, so the prefetcher filling it
// takes that work off the block's critical path. Gas is charged in full on a
// hit: prices must cover a miss.
type PQVerifyCache = BoundedCache[pqVerifyCacheKey, []byte]

// NewPQVerifyCache returns an empty cache.
func NewPQVerifyCache() *PQVerifyCache {
	return newBoundedCache[pqVerifyCacheKey, []byte](pqVerifyCacheCap)
}

// isPQVerify reports whether p is one of the cached post-quantum precompiles.
func isPQVerify(p PrecompiledContract) bool {
	switch p.(type) {
	case *mldsa65Verify, *slhdsaSha2128sVerify:
		return true
	}
	return false
}

// runPQVerifyWithCache runs a post-quantum verify precompile through cache.
// Gas, out-of-gas and tracing behave exactly like RunPrecompiledContract.
func (evm *EVM) runPQVerifyWithCache(p PrecompiledContract, addr common.Address, input []byte, gas uint64, cache *PQVerifyCache) ([]byte, uint64, error) {
	gasCost := p.RequiredGas(input)
	if gas < gasCost {
		return nil, 0, ErrOutOfGas
	}
	key := pqVerifyCacheKey{addr: addr, input: sha256.Sum256(input)}
	if cached, ok := cache.Load(key); ok {
		evm.traceGasChange(gas, gas-gasCost)
		return cached, gas - gasCost, nil
	}
	ret, remainingGas, err := RunPrecompiledContract(p, input, gas, evm.Config.Tracer)
	if err == nil {
		cache.Store(key, ret)
	}
	return ret, remainingGas, err
}
