package fk20

import (
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	"github.com/crate-crypto/go-eth-kzg/internal/multiexp"
	"github.com/crate-crypto/go-eth-kzg/internal/utils"
)

type toeplitzMatrix struct {
	col []fr.Element
	row []fr.Element
}

// Embed toeplitz matrix within a circulant matrix
func (tm *toeplitzMatrix) embedCirculant() circulantMatrix {
	n := len(tm.row)
	row := make([]fr.Element, len(tm.col)+n)

	// Copy tm.Col
	copy(row, tm.col)

	// Append rotated and reversed tm.Row
	for i := 0; i < n; i++ {
		row[len(tm.col)+i] = tm.row[(n-i)%n]
	}
	return circulantMatrix{row: row}
}

type circulantMatrix struct {
	row []fr.Element
}

func newToeplitz(row, column []fr.Element) toeplitzMatrix {
	return toeplitzMatrix{
		col: column,
		row: row,
	}
}

type BatchToeplitzMatrixVecMul struct {
	transposedFFTFixedVectors [][]bls12381.G1Affine
	circulantDomain           domain.Domain
}

// newBatchToeplitzMatrixVecMul creates a new Instance of `BatchToeplitzMatrixVecMul`
//
// Note: `fixedVectors` is mutated in place, ie it is treated as mutable reference to a pointer.
func newBatchToeplitzMatrixVecMul(fixedVectors [][]bls12381.G1Affine) BatchToeplitzMatrixVecMul {
	// We assume that the length of the vector is at least one.
	// If this is not true, then we panic on startup.
	//
	// Check that these vectors have a power of two size
	size := len(fixedVectors[0])
	for i := 1; i < len(fixedVectors); i++ {
		if size != len(fixedVectors[i]) {
			panic("all vectors must be the same size")
		}
	}

	// Check that the size is a power of two
	// This just makes padding to the next power of two
	// simple.
	if !utils.IsPowerOfTwo(uint64(size)) {
		panic("fixedVectors do not have a power of two size")
	}

	// We assume that all vectors have the same size
	vecLen := size

	// Given we force the toeplitz matrix to be a power of two.
	// Embedding the toeplitz matrix into a circulant matrix
	// will produce a circulant matrix whose row is twice the size
	// of the toeplitz matrix.
	circulantPaddedVecSize := vecLen * 2

	circulantDomain := domain.NewDomain(uint64(circulantPaddedVecSize))

	fftFixedVectors := fixedVectors
	// Before performing the fft, pad the vector so that it is the correct size.
	padToPowerOfTwo(fftFixedVectors)

	for i := 0; i < len(fftFixedVectors); i++ {
		fftFixedVectors[i] = circulantDomain.FftG1(fftFixedVectors[i])
	}
	transposedFFTFixedVectors := transposeVectors(fftFixedVectors)

	return BatchToeplitzMatrixVecMul{
		transposedFFTFixedVectors: transposedFFTFixedVectors,
		circulantDomain:           *circulantDomain,
	}
}

func (bt *BatchToeplitzMatrixVecMul) BatchMulAggregation(matrices []toeplitzMatrix) ([]bls12381.G1Affine, error) {
	// Convert toeplitz matrices into circulant matrices
	circulantMatrices := make([]circulantMatrix, len(matrices))
	for i := 0; i < len(matrices); i++ {
		circulantMatrices[i] = matrices[i].embedCirculant()
	}

	// Compute FFT of circulant matrices rows
	fftCirculantRows := make([][]fr.Element, len(matrices))
	for i := 0; i < len(matrices); i++ {
		fftCirculantRows[i] = bt.circulantDomain.FftFr(circulantMatrices[i].row)
	}

	// Transpose rows converting the hadamard product(scalar multiplications) due to the Diagnol matrix
	// into an inner product (MSM)
	transposedFFTRows := transposeVectors(fftCirculantRows)
	results := make([]bls12381.G1Affine, len(transposedFFTRows))
	for i := 0; i < len(transposedFFTRows); i++ {
		result, err := multiexp.MultiExpG1(transposedFFTRows[i], bt.transposedFFTFixedVectors[i], 0)
		if err != nil {
			return nil, err
		}
		results[i] = *result
	}

	circulantSum := bt.circulantDomain.IfftG1(results)

	return circulantSum[:len(circulantSum)/2], nil
}

func transposeVectors[T any](vectors [][]T) [][]T {
	if len(vectors) == 0 {
		return nil
	}

	n := len(vectors[0])
	transposed := make([][]T, n)

	for i := range transposed {
		transposed[i] = make([]T, 0, len(vectors))
	}

	for _, vector := range vectors {
		for i, a := range vector {
			transposed[i] = append(transposed[i], a)
		}
	}

	return transposed
}
