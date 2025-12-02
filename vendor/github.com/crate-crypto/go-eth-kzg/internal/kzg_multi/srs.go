package kzgmulti

import (
	"errors"
	"math/big"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	"github.com/crate-crypto/go-eth-kzg/internal/kzg"
	"github.com/crate-crypto/go-eth-kzg/internal/multiexp"
)

// The commit key stays the same between the kzg single opening
// use case and the multi opening use case
type CommitKey = kzg.CommitKey

type SRS struct {
	OpeningKey OpeningKey
	CommitKey  CommitKey
}

type OpeningKey struct {
	// These are the G1 elements in monomial form from the trusted setup
	G1 []bls12381.G1Affine
	// These are the G2 elements in monomial form from the trusted setup
	// Note: the length of this list is the same as the length of the G1 list
	G2 []bls12381.G2Affine
	// Number of points that the prover will open to in a single multi-point proof
	//
	// The points that the prover can open to are not arbitrary sets, but are cosets.
	CosetSize uint64
	// A bound on the polynomial length that we can commit, create and
	// verify proofs about.
	//
	// Note: This is not the degree of the polynomial.
	// One can compute the degree of the polynomial by doing `PolySize-1``
	PolySize uint64
	// The total number of points that the prover will create proofs for.
	//
	// Example, we could have f(x) = x^3 + x^2 + x + 1
	// and a protocol may require that this polynomial which has PolySize = 4
	// to be opened up at 32 points. The number 32 is the NumPointsToOpen constant.
	NumPointsToOpen uint64
	// CosetShiftsPowCosetSize contains precomputed powers of coset shifts.
	// For each coset k, it stores (ω_k)^n where ω_k is the k-th coset shift
	// and n is the coset size. These values are used for verifying multiple
	// multi-point KZG opening proofs.
	CosetShiftsPowCosetSize []fr.Element
	// cosetDomains is a slice of CosetDomain objects, one for each coset.
	//
	// Each CosetDomain encapsulates the necessary information and methods
	// to perform FFT operations over a specific coset of the evaluation domain.
	//
	// Note: This should not be confused with the cosets that we are creating
	// and verifying opening proofs for.
	cosetDomains []*domain.CosetDomain
}

func NewOpeningKey(g1s []bls12381.G1Affine, g2s []bls12381.G2Affine, polySize, numPointsToOpen, cosetSize uint64) *OpeningKey {
	cosetDomain := domain.NewDomain(cosetSize)

	extDomain := domain.NewDomain(numPointsToOpen)
	domain.BitReverse(extDomain.Roots)

	numCosets := numPointsToOpen / cosetSize
	cosetShifts := make([]fr.Element, numCosets)
	for k := 0; k < int(numCosets); k++ {
		cosetShifts[k] = extDomain.Roots[k*int(cosetSize)]
	}

	invCosetShifts := make([]fr.Element, numCosets)
	for k := 0; k < int(numCosets); k++ {
		// Note: This is safe because the coset shifts are roots of unity
		// and zero is not a root of unity.
		invCosetShifts[k].Inverse(&cosetShifts[k])
	}

	cosetShiftsPowCosetSize := make([]fr.Element, numCosets)
	cosetSizeBigInt := big.NewInt(int64(cosetSize))
	for k := 0; k < int(numCosets); k++ {
		cosetShiftsPowCosetSize[k].Exp(cosetShifts[k], cosetSizeBigInt)
	}

	cosetDomains := make([]*domain.CosetDomain, numCosets)
	for k := 0; k < int(numCosets); k++ {
		fftCoset := domain.FFTCoset{
			CosetGen:    cosetShifts[k],
			InvCosetGen: invCosetShifts[k],
		}
		cosetDomains[k] = domain.NewCosetDomain(cosetDomain, fftCoset)
	}

	return &OpeningKey{
		G1:                      g1s,
		G2:                      g2s,
		CosetSize:               cosetSize,
		PolySize:                polySize,
		NumPointsToOpen:         numPointsToOpen,
		CosetShiftsPowCosetSize: cosetShiftsPowCosetSize,
		cosetDomains:            cosetDomains,
	}
}

// This is the degree-0 G_2 element in the trusted setup.
// In the specs, this is denoted as `KZG_SETUP_G2[0]`
func (o *OpeningKey) genG2() *bls12381.G2Affine {
	return &o.G2[0]
}

// This method has been copied and modified from kzg/srs.go
// It is only used for testing, so this is okay.
func newMonomialSRSInsecureUint64(polySize, numPointsToOpen, cosetSize uint64, bAlpha *big.Int) (*SRS, error) {
	if polySize < 2 {
		return nil, ErrMinSRSSize
	}

	var commitKey CommitKey
	commitKey.G1 = make([]bls12381.G1Affine, polySize)

	var alpha fr.Element
	alpha.SetBigInt(bAlpha)

	_, _, gen1Aff, gen2Aff := bls12381.Generators()

	alphas := make([]fr.Element, polySize)
	alphas[0] = fr.NewElement(1)
	alphas[1] = alpha

	for i := 2; i < len(alphas); i++ {
		alphas[i].Mul(&alphas[i-1], &alpha)
	}
	g1s := bls12381.BatchScalarMultiplicationG1(&gen1Aff, alphas)
	g2s := bls12381.BatchScalarMultiplicationG2(&gen2Aff, alphas)
	copy(commitKey.G1, g1s)

	return &SRS{
		CommitKey:  commitKey,
		OpeningKey: *NewOpeningKey(g1s, g2s, polySize, numPointsToOpen, cosetSize),
	}, nil
}

func (ok *OpeningKey) CommitG1(scalars []fr.Element) (*bls12381.G1Affine, error) {
	if len(scalars) == 0 || len(scalars) > len(ok.G1) {
		return nil, errors.New("invalid vector size for G1 commitment")
	}

	return multiexp.MultiExpG1(scalars, ok.G1[:len(scalars)], 0)
}

func (ok *OpeningKey) CommitG2(scalars []fr.Element) (*bls12381.G2Affine, error) {
	if len(scalars) == 0 || len(scalars) > len(ok.G2) {
		return nil, errors.New("invalid vector size for G2 commitment")
	}

	return multiexp.MultiExpG2(scalars, ok.G2[:len(scalars)], 0)
}
