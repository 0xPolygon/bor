package kzgmulti

import (
	"crypto/sha256"
	"encoding/binary"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	"github.com/crate-crypto/go-eth-kzg/internal/kzg"
	"github.com/crate-crypto/go-eth-kzg/internal/multiexp"
	"github.com/crate-crypto/go-eth-kzg/internal/poly"
	"github.com/crate-crypto/go-eth-kzg/internal/utils"
)

// u64ToByteArray16 converts a uint64 to a byte slice of length 16 in big endian format. This implies that the first 8 bytes of the result are always 0.
func u64ToByteArray16(number uint64) []byte {
	bytes := make([]byte, 16)
	binary.BigEndian.PutUint64(bytes[8:], number)
	return bytes
}

// Verifies Multiple KZGProofs
//
// Note: `cosetEvals` is mutated in-place, ie it should be treated as a mutable reference
func VerifyMultiPointKZGProofBatch(deduplicatedCommitments []bls12381.G1Affine, commitmentIndices, cosetIndices []uint64, proofs []bls12381.G1Affine, cosetEvals [][]fr.Element, openKey *OpeningKey) error {
	r := fiatShamirChallenge(deduplicatedCommitments, cosetEvals, commitmentIndices, cosetIndices, openKey)
	rPowers := utils.ComputePowers(r, uint(len(commitmentIndices)))

	numCosets := len(cosetIndices)
	numUniqueCommitments := len(deduplicatedCommitments)
	commRandomSumProofs, err := multiexp.MultiExpG1(rPowers, proofs, 0)
	if err != nil {
		return err
	}

	weights := make([]fr.Element, numUniqueCommitments)
	for k := 0; k < numCosets; k++ {
		commitmentIndex := commitmentIndices[k]
		weights[commitmentIndex].Add(&weights[commitmentIndex], &rPowers[k])
	}
	commRandomSumComms, err := multiexp.MultiExpG1(weights, deduplicatedCommitments, 0)
	if err != nil {
		return err
	}

	cosetSize := openKey.CosetSize

	// Compute random linear sum of interpolation polynomials
	interpolationPoly := []fr.Element{}
	for k, cosetEval := range cosetEvals {
		domain.BitReverse(cosetEval)

		// Coset IFFT
		cosetIndex := cosetIndices[k]
		cosetDomain := openKey.cosetDomains[cosetIndex]
		cosetMonomial := cosetDomain.CosetIFFtFr(cosetEval)

		// Scale the interpolation polynomial
		for i := 0; i < len(cosetMonomial); i++ {
			cosetMonomial[i].Mul(&cosetMonomial[i], &rPowers[k])
		}

		interpolationPoly = poly.PolyAdd(interpolationPoly, cosetMonomial)
	}

	commRandomSumInterPoly, err := openKey.CommitG1(interpolationPoly)
	if err != nil {
		return err
	}

	weightedRPowers := make([]fr.Element, numCosets)
	for k := 0; k < len(rPowers); k++ {
		cosetIndex := cosetIndices[k]
		rPower := rPowers[k]
		cosetShiftPowN := openKey.CosetShiftsPowCosetSize[cosetIndex]
		weightedRPowers[k].Mul(&cosetShiftPowN, &rPower)
	}
	randomWeightedSumProofs, err := multiexp.MultiExpG1(weightedRPowers, proofs, 0)
	if err != nil {
		return err
	}

	rl := bls12381.G1Affine{}
	rl.Sub(commRandomSumComms, commRandomSumInterPoly)
	rl.Add(&rl, randomWeightedSumProofs)

	negG2Gen := bls12381.G2Affine{}
	negG2Gen.Neg(openKey.genG2())

	sPowCosetSize := openKey.G2[cosetSize]

	check, err := bls12381.PairingCheck(
		[]bls12381.G1Affine{*commRandomSumProofs, rl},
		[]bls12381.G2Affine{sPowCosetSize, negG2Gen},
	)
	if err != nil {
		return err
	}
	if !check {
		return kzg.ErrVerifyOpeningProof
	}
	return nil
}

func fiatShamirChallenge(rowCommitments []bls12381.G1Affine, cosetsEvals [][]fr.Element, rowIndices, cosetIndices []uint64, openKey *OpeningKey) fr.Element {
	const DomSepProtocol = "RCKZGCBATCH__V1_"

	h := sha256.New()
	h.Write([]byte(DomSepProtocol))
	h.Write(u64ToByteArray16(openKey.PolySize))
	h.Write(u64ToByteArray16(openKey.CosetSize))

	lenRowCommitments := len(rowCommitments)
	h.Write(u64ToByteArray16(uint64(lenRowCommitments)))

	lenCosetIndices := len(cosetIndices)
	h.Write(u64ToByteArray16(uint64(lenCosetIndices)))

	for _, commitment := range rowCommitments {
		h.Write(commitment.Marshal())
	}

	for k := 0; k < lenCosetIndices; k++ {
		rowIndex := rowIndices[k]
		cosetIndex := cosetIndices[k]
		cosetEvals := cosetsEvals[k]

		h.Write(u64ToByteArray16(rowIndex))
		h.Write(u64ToByteArray16(cosetIndex))
		for _, eval := range cosetEvals {
			h.Write(eval.Marshal())
		}
	}

	digest := h.Sum(nil)
	var challenge fr.Element
	challenge.SetBytes(digest[:])
	return challenge
}
