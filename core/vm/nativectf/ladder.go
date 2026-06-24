package nativectf

import (
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/holiman/uint256"
)

// modSqrtExp is (P+1)/4, the BN254 modular-sqrt exponent. For the BN254 base
// field prime P (P ≡ 3 mod 4), x^((P+1)/4) is a square root of x when x is a QR
// — the exponentiation the recognized ladder block performs.
var modSqrtExp, _ = new(big.Int).SetString("0c19139cb84c680a6e14116da060561765e05aa45a1c72a34f082305b61f3f52", 16)

// ModSqrtCandidate returns x^((P+1)/4) mod P — the exact value computed by the
// recognized BN254 square-and-multiply ladder block. It is computed in the
// Montgomery field (gnark bn254/fp) for speed; equivalence to the plain integer
// modexp (big.Int.Exp) is asserted in tests. Any 256-bit x is accepted and
// reduced mod P, matching the EVM MULMOD semantics of the ladder.
func ModSqrtCandidate(x *uint256.Int) *uint256.Int {
	var xe fp.Element
	b := x.Bytes32()
	xe.SetBytes(b[:]) // reduces mod P
	var out fp.Element
	out.Exp(xe, modSqrtExp)
	res := out.Bytes() // 32-byte big-endian, canonical (< P)
	return new(uint256.Int).SetBytes(res[:])
}
