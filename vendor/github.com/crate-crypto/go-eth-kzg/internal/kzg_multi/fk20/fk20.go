package fk20

import (
	"errors"
	"slices"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	"github.com/crate-crypto/go-eth-kzg/internal/utils"
)

type FK20 struct {
	batchMulAgg BatchToeplitzMatrixVecMul

	proofDomain domain.Domain
	extDomain   domain.Domain

	numPointsToOpen int
	evalSetSize     int
}

func NewFK20(srs []bls12381.G1Affine, numPointsToOpen, evalSetSize int) FK20 {
	if !utils.IsPowerOfTwo(uint64(evalSetSize)) {
		panic("the evaluation set size should be a power of two. It is the size of each coset")
	}

	srs = slices.Clone(srs)

	slices.Reverse(srs)
	srsTruncated := srs[evalSetSize:]
	srsVectors := takeEveryNth(srsTruncated, evalSetSize)
	padToPowerOfTwo(srsVectors)

	batchMul := newBatchToeplitzMatrixVecMul(srsVectors)

	// Compute the number of proofs
	numProofs := numPointsToOpen / evalSetSize

	proofDomain := domain.NewDomain(uint64(numProofs))

	// The size of the extension domain corresponds to the number of points that we want to open
	extDomain := domain.NewDomain(uint64(numPointsToOpen))

	return FK20{
		batchMulAgg:     batchMul,
		proofDomain:     *proofDomain,
		extDomain:       *extDomain,
		numPointsToOpen: numPointsToOpen,
		evalSetSize:     evalSetSize,
	}
}

// computeEvaluationSet evaluates `polyCoeff` on all of the cosets
// that `ComputeMultiOpenProof` has created proofs for.
//
// Note: `polyCoeff` is not mutated in-place, ie it should be treated as a immutable reference.
func (fk *FK20) computeEvaluationSet(polyCoeff []fr.Element) [][]fr.Element {
	polyCoeff = slices.Clone(polyCoeff)
	// Pad to the correct length
	for i := len(polyCoeff); i < len(fk.extDomain.Roots); i++ {
		polyCoeff = append(polyCoeff, fr.Element{})
	}

	evaluations := fk.extDomain.FftFr(polyCoeff)
	domain.BitReverse(evaluations)
	return partition(evaluations, fk.evalSetSize)
}

func (fk *FK20) ComputeExtendedPolynomial(poly []fr.Element) [][]fr.Element {
	return fk.computeEvaluationSet(poly)
}

func (fk *FK20) ComputeMultiOpenProof(poly []fr.Element) ([]bls12381.G1Affine, [][]fr.Element, error) {
	// Note: `computeEvaluationSet` will create a copy of `poly`
	// and pad it. Hence, the rest of this method, does not use the padded
	// version  of `poly`.
	outputSets := fk.ComputeExtendedPolynomial(poly)

	hComms, err := fk.computeHPolysComm(poly)
	if err != nil {
		return nil, nil, err
	}

	// Pad hComms since fft does not do this
	numProofs := len(fk.proofDomain.Roots)
	for i := len(hComms); i < numProofs; i++ {
		hComms = append(hComms, bls12381.G1Affine{})
	}

	proofs := fk.proofDomain.FftG1(hComms)
	domain.BitReverse(proofs)

	return proofs, outputSets, nil
}

// computeHPolysComm computes commitments to the polynomials that are common amongst all
// of the opening proofs across the cosets. These are denoted as `hPolys` and the naming
// follows the FK20 paper.
//
// Note: `polyCoeff` is not mutated in-place, ie it should be treated as a immutable reference.
func (fk *FK20) computeHPolysComm(polyCoeff []fr.Element) ([]bls12381.G1Affine, error) {
	if !utils.IsPowerOfTwo(uint64(len(polyCoeff))) {
		return nil, errors.New("expected the polynomial to have power of two number of coefficients")
	}

	// Reverse polynomial so that we have the highest coefficient
	// be first.
	// Note: we clone since we want to reverse the order of the coefficients
	polyCoeff = slices.Clone(polyCoeff)
	slices.Reverse(polyCoeff)

	toeplitzRows := takeEveryNth(polyCoeff, fk.evalSetSize)

	toeplitzMatrices := make([]toeplitzMatrix, len(toeplitzRows))
	for i := 0; i < len(toeplitzRows); i++ {
		row := toeplitzRows[i]

		column := make([]fr.Element, len(row))
		column[0] = row[0]

		toeplitzMatrices[i] = newToeplitz(row, column)
	}

	return fk.batchMulAgg.BatchMulAggregation(toeplitzMatrices)
}

func takeEveryNth[T any](list []T, n int) [][]T {
	result := make([][]T, n)

	for i := 0; i < n; i++ {
		subList := make([]T, 0)
		for j := i; j < len(list); j += n {
			subList = append(subList, list[j])
		}
		result[i] = subList
	}

	return result
}

// nextPowerOfTwo returns the next power of two greater than or equal to n
func nextPowerOfTwo(n int) int {
	if n == 0 {
		return 1
	}
	k := 1
	for k <= n {
		k <<= 1
	}
	return k
}

// padToPowerOfTwo pads each inner slice to the next power of two in-place
func padToPowerOfTwo(matrix [][]bls12381.G1Affine) {
	for i, slice := range matrix {
		currentLen := len(slice)
		nextPow2 := nextPowerOfTwo(currentLen)

		if nextPow2 > currentLen {
			identityPoint := bls12381.G1Affine{}

			// Extend the slice to the next power of two
			for j := currentLen; j < nextPow2; j++ {
				matrix[i] = append(matrix[i], identityPoint)
			}
		}
	}
}

// partition groups a slice into chunks of size k
// Example:
// Input: [1, 2, 3, 4, 5, 6, 7, 8, 9], k: 3
// Output: [[1, 2, 3], [4, 5, 6], [7, 8, 9]]
//
// Panics if the slice cannot be divided into chunks of size k
func partition(slice []fr.Element, k int) [][]fr.Element {
	var result [][]fr.Element

	for i := 0; i < len(slice); i += k {
		end := i + k
		if end > len(slice) {
			panic("all partitions should have the same size")
		}
		result = append(result, slice[i:end])
	}

	return result
}
