// Package nativectf is a bit-exact native reimplementation of Gnosis CTF
// CTHelpers.getCollectionId (alt_bn128 try-and-increment + modular sqrt).
// It backs a flag-gated, result-and-gas-exact EVM fast-path; it changes no
// chain results and requires no hardfork.
package nativectf

import (
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/ethereum/go-ethereum/crypto"
)

// GetCollectionId returns the CTF collection id for parentCollectionId == 0
// (sqrt-only path). The parent != 0 branch (alt_bn128 ECADD) is added in a later
// task. It also returns the gas-relevant facts: the QR-loop iteration count and
// the two conditional-branch outcomes (parity flip, bit-254 compression).
func GetCollectionId(parent [32]byte, conditionId [32]byte, indexSet *big.Int) (result [32]byte, iters int, parityFlipped bool, bit254 bool) {
	var idx [32]byte
	indexSet.FillBytes(idx[:])
	h := crypto.Keccak256(conditionId[:], idx[:])
	raw := new(big.Int).SetBytes(h)
	odd := raw.Bit(255) == 1

	var x, yy, y1, one, three fp.Element
	x.SetBytes(h) // raw mod P
	one.SetOne()
	three.SetUint64(3)
	for {
		iters++
		x.Add(&x, &one)     // x1 = addmod(x1, 1, P); first candidate = hash+1
		yy.Square(&x)       // x^2
		yy.Mul(&yy, &x)     // x^3
		yy.Add(&yy, &three) // x^3 + 3
		if yy.Legendre() == 1 {
			y1.Sqrt(&yy)
			break
		}
	}
	yb := new(big.Int)
	y1.BigInt(yb)
	yodd := yb.Bit(0) == 1
	if (odd && !yodd) || (!odd && yodd) {
		parityFlipped = true // y1 = P - y1 (parity flips)
		yodd = !yodd
	}
	// parent != 0 handled in a later task; parent == 0 compression below.
	xb := new(big.Int)
	x.BigInt(xb)
	if yodd {
		bit254 = true
		xb.SetBit(xb, 254, 1)
	}
	xb.FillBytes(result[:])
	return result, iters, parityFlipped, bit254
}
