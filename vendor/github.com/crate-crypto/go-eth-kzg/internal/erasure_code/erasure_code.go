package erasure_code

import (
	"errors"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	"github.com/crate-crypto/go-eth-kzg/internal/poly"
)

// BlockErasureIndex is used to indicate the index of the block erasure that is missing
// from the codeword.
type BlockErasureIndex = uint64

// DataRecovery implements a unique decoding algorithm.
//
// The algorithm is not generic and is specific to the use-case where:
//   - We have block erasures. ie we do not lose data in random locations, but in contiguous chunks.
//     The chunks themselves are predetermined.
type DataRecovery struct {
	// rootsOfUnityBlockErasureIndex is a domain that corresponds to the number of blocks
	// that we can have in the codeword.
	rootsOfUnityBlockErasureIndex *domain.Domain
	domainExtended                *domain.Domain
	domainExtendedCoset           *domain.CosetDomain
	// blockErasureSize indicates the size of `blocks of evaluations` that
	// can be missing. For example, if blockErasureSize is 4, then 4 evaluations
	// can be missing, or 8 or 16.
	//
	// This is contrary to a general unique decoding algorithm where the number of
	// missing elements can be any number and the evaluations do not need to
	// be in blocks.
	blockErasureSize int
	// numScalarsInCodeword is the number of scalars in the codeword
	// ie the number of scalars we get when we get encode the data.
	numScalarsInCodeword int
	// numScalarsInDataWord is the number of scalars in the message
	// that we encode.
	numScalarsInDataWord int
	// expansionFactor is the factor by which the data word or message is expanded
	expansionFactor int
	// totalNumBlocks is the total number of blocks(groups of evaluations) in the codeword
	totalNumBlocks int
}

func NewDataRecovery(blockErasureSize, numScalarsInDataWord, expansionFactor int) *DataRecovery {
	// Compute the number of scalars that will be in the codeword
	numScalarsInCodeword := numScalarsInDataWord * expansionFactor

	// Compute the total number of blocks that we will need to
	// represent the codeword
	totalNumBlocks := numScalarsInCodeword / blockErasureSize

	rootsOfUnityBlockErasureIndex := domain.NewDomain(uint64(totalNumBlocks))
	domainExtended := domain.NewDomain(uint64(numScalarsInCodeword))

	fftCoset := domain.FFTCoset{}
	fftCoset.CosetGen = fr.NewElement(7)
	fftCoset.InvCosetGen.Inverse(&fftCoset.CosetGen)
	domainExtendedCoset := domain.NewCosetDomain(domainExtended, fftCoset)

	return &DataRecovery{
		rootsOfUnityBlockErasureIndex: rootsOfUnityBlockErasureIndex,
		domainExtended:                domainExtended,
		domainExtendedCoset:           domainExtendedCoset,
		blockErasureSize:              blockErasureSize,
		numScalarsInCodeword:          numScalarsInCodeword,
		numScalarsInDataWord:          numScalarsInDataWord,
		expansionFactor:               expansionFactor,
		totalNumBlocks:                totalNumBlocks,
	}
}

// Note: These blockErasure indices should not be in bit reversed order
func (dr *DataRecovery) constructVanishingPolyOnIndices(missingBlockErasureIndices []BlockErasureIndex) []fr.Element {
	// Collect all of the roots that are associated with the missing block erasure indices
	missingBlockErasureIndexRoots := make([]fr.Element, len(missingBlockErasureIndices))
	for i, index := range missingBlockErasureIndices {
		missingBlockErasureIndexRoots[i] = dr.rootsOfUnityBlockErasureIndex.Roots[index]
	}

	shortZeroPoly := vanishingPolyCoeff(missingBlockErasureIndexRoots)

	zeroPolyCoeff := make([]fr.Element, dr.numScalarsInCodeword)
	for i, coeff := range shortZeroPoly {
		zeroPolyCoeff[i*dr.blockErasureSize] = coeff
	}

	return zeroPolyCoeff
}

// Encode the polynomial by evaluating it on the extended domain.
//
// Note: `polyCoeff` is mutated in-place, ie it should be seen as mutable reference.
func (dr *DataRecovery) Encode(polyCoeff []fr.Element) []fr.Element {
	// Pad to the correct length
	for i := len(polyCoeff); i < len(dr.domainExtended.Roots); i++ {
		polyCoeff = append(polyCoeff, fr.Element{})
	}
	return dr.domainExtended.FftFr(polyCoeff)
}

// NumBlocksNeededToReconstruct returns the number of blocks that are needed to reconstruct
// the original data word.
func (dr *DataRecovery) NumBlocksNeededToReconstruct() int {
	return dr.numScalarsInDataWord / dr.blockErasureSize
}

func (dr *DataRecovery) RecoverPolynomialCoefficients(data []fr.Element, missingIndices []BlockErasureIndex) ([]fr.Element, error) {
	zX := dr.constructVanishingPolyOnIndices(missingIndices)

	zXEval := dr.domainExtended.FftFr(zX)

	if len(zXEval) != len(data) {
		return nil, errors.New("length of data and zXEval should be equal")
	}

	eZEval := make([]fr.Element, len(data))
	for i := 0; i < len(data); i++ {
		eZEval[i].Mul(&data[i], &zXEval[i])
	}

	dzPoly := dr.domainExtended.IfftFr(eZEval)

	cosetZxEval := dr.domainExtendedCoset.CosetFFtFr(zX)
	cosetDzEVal := dr.domainExtendedCoset.CosetFFtFr(dzPoly)

	cosetQuotientEval := make([]fr.Element, len(cosetZxEval))
	cosetZxEval = fr.BatchInvert(cosetZxEval)

	for i := 0; i < len(cosetZxEval); i++ {
		cosetQuotientEval[i].Mul(&cosetDzEVal[i], &cosetZxEval[i])
	}

	polyCoeff := dr.domainExtendedCoset.CosetIFFtFr(cosetQuotientEval)

	// Truncate the polynomial coefficients to the number of scalars in the data word
	polyCoeff = polyCoeff[:dr.numScalarsInDataWord]
	return polyCoeff, nil
}

// vanishingPolyCoeff returns the polynomial that has roots at the given points
func vanishingPolyCoeff(xs []fr.Element) poly.PolynomialCoeff {
	result := []fr.Element{fr.One()}

	for _, x := range xs {
		// This is to silence: G601: Implicit memory aliasing in for loop.
		x := x

		negX := fr.Element{}
		negX.Neg(&x)
		result = poly.PolyMul(result, []fr.Element{negX, fr.One()})
	}

	return result
}
