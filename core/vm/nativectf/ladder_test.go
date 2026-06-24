package nativectf

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
)

func bigP() *big.Int {
	p, _ := new(big.Int).SetString("30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd47", 16)
	return p
}
func bigE() *big.Int {
	e, _ := new(big.Int).SetString("0c19139cb84c680a6e14116da060561765e05aa45a1c72a34f082305b61f3f52", 16)
	return e
}

// ModSqrtCandidate must equal the exact integer modexp x^((P+1)/4) mod P that the
// recognized BN254 ladder block computes, for any 256-bit x.
func TestModSqrtCandidate_MatchesBigInt(t *testing.T) {
	p, e := bigP(), bigE()
	r := rand.New(rand.NewSource(1))
	max := new(big.Int).Lsh(big.NewInt(1), 256)
	for i := 0; i < 5000; i++ {
		xb := new(big.Int).Rand(r, max)
		want := new(big.Int).Exp(xb, e, p) // exact integer oracle
		x := new(uint256.Int).SetBytes(xb.Bytes())
		got := ModSqrtCandidate(x)
		var wb [32]byte
		want.FillBytes(wb[:])
		if got.Bytes32() != wb {
			t.Fatalf("x=%x: got %x want %x", xb, got.Bytes32(), wb)
		}
	}
}

// Edge cases: 0, 1, P-1, and values >= P (must reduce).
func TestModSqrtCandidate_Edges(t *testing.T) {
	p, e := bigP(), bigE()
	cases := []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		new(big.Int).Sub(p, big.NewInt(1)),
		p, // == 0 mod P
		new(big.Int).Add(p, big.NewInt(7)),                       // 7 mod P
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), // 2^256-1
	}
	for _, xb := range cases {
		want := new(big.Int).Exp(xb, e, p)
		x := new(uint256.Int).SetBytes(xb.Bytes())
		got := ModSqrtCandidate(x)
		var wb [32]byte
		want.FillBytes(wb[:])
		if got.Bytes32() != wb {
			t.Fatalf("x=%x: got %x want %x", xb, got.Bytes32(), wb)
		}
	}
}
