package kzgmulti

import (
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/kzg_multi/fk20"
	"github.com/crate-crypto/go-eth-kzg/internal/poly"
)

func ComputeMultiPointKZGProofs(fk20 *fk20.FK20, poly poly.PolynomialCoeff) ([]bls12381.G1Affine, [][]fr.Element, error) {
	return fk20.ComputeMultiOpenProof(poly)
}
