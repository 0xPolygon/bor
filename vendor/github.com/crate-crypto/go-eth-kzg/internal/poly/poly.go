package poly

import (
	"slices"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
)

// A polynomial in lagrange form
type Polynomial = []fr.Element

// A polynomial in monomial form
type PolynomialCoeff = []fr.Element

// PolyAdd adds two polynomials in coefficient form and returns the result.
// The resulting polynomial has a degree equal to the maximum degree of the input polynomials.
func PolyAdd(a, b PolynomialCoeff) PolynomialCoeff {
	minPolyLen := min(numCoeffs(a), numCoeffs(b))
	maxPolyLen := max(numCoeffs(a), numCoeffs(b))

	result := make([]fr.Element, maxPolyLen)

	for i := 0; i < int(minPolyLen); i++ {
		result[i].Add(&a[i], &b[i])
	}

	// If a has more coefficients than b, copy the remaining coefficients from a
	// into result
	// If b has more coefficients than a, copy the remaining coefficients of b
	// and copy them into result
	if int(numCoeffs(a)) > int(minPolyLen) {
		for i := minPolyLen; i < numCoeffs(a); i++ {
			result[i].Set(&a[i])
		}
	} else if numCoeffs(b) > minPolyLen {
		for i := minPolyLen; i < numCoeffs(b); i++ {
			result[i].Set(&b[i])
		}
	}
	return result
}

// PolyMul multiplies two polynomials in coefficient form and returns the result.
// The degree of the resulting polynomial is the sum of the degrees of the input polynomials.
func PolyMul(a, b PolynomialCoeff) PolynomialCoeff {
	// The degree of result will be degree(a) + degree(b) = numCoeffs(a) + numCoeffs(b) - 1
	productDegree := numCoeffs(a) + numCoeffs(b)
	result := make([]fr.Element, productDegree-1)

	for i := uint64(0); i < numCoeffs(a); i++ {
		for j := uint64(0); j < numCoeffs(b); j++ {
			mulRes := fr.Element{}
			mulRes.Mul(&a[i], &b[j])
			result[i+j].Add(&result[i+j], &mulRes)
		}
	}

	return result
}

// equalPoly checks if two polynomials in coefficient form are equal.
// It removes trailing zeros (normalizes) before comparison.
func equalPoly(a, b PolynomialCoeff) bool {
	a = removeTrailingZeros(a)
	b = removeTrailingZeros(b)

	// Two polynomials that do not have the same
	if numCoeffs(a) != numCoeffs(b) {
		return false
	}

	polyLen := numCoeffs(a)
	if polyLen == 0 {
		return true
	}
	// Check element-wise equality
	for i := uint64(0); i < polyLen; i++ {
		if !a[i].Equal(&b[i]) {
			return false
		}
	}
	return true
}

// PolyEval evaluates a polynomial f(x) at a point z, computing f(z).
// The polynomial is given in coefficient form, and `z` is denoted as inputPoint.
func PolyEval(poly PolynomialCoeff, inputPoint fr.Element) fr.Element {
	result := fr.NewElement(0)

	for i := len(poly) - 1; i >= 0; i-- {
		tmp := fr.Element{}
		tmp.Mul(&result, &inputPoint)
		result.Add(&tmp, &poly[i])
	}

	return result
}

// DividePolyByXminusA computes f(x) / (x - a) and returns the quotient.
//
// Note: (x-a) is not a factor of f(x), the remainder will not be returned.
//
// This was copied and modified from the gnark codebase.
func DividePolyByXminusA(poly PolynomialCoeff, a fr.Element) []fr.Element {
	// clone the slice so we do not modify the slice in place
	quotient := slices.Clone(poly)

	var t fr.Element

	for i := len(quotient) - 2; i >= 0; i-- {
		t.Mul(&quotient[i+1], &a)

		quotient[i].Add(&quotient[i], &t)
	}

	// the result is of degree deg(f)-1
	return quotient[1:]
}

func numCoeffs(p PolynomialCoeff) uint64 {
	return uint64(len(p))
}

// Removes the higher coefficients from the polynomial
// that are zero.
//
// This method assumes that the slice is a polynomial where
// the higher coefficients are placed at the end of the slice,
// ie f(x) = 5 + 6x + 10x^2 would be [5, 6, 10] as a slice.
//
// This therefore has no impact on the polynomial and is just
// normalizing the polynomial.
func removeTrailingZeros(slice []fr.Element) []fr.Element {
	for len(slice) > 0 && slice[len(slice)-1].IsZero() {
		slice = slice[:len(slice)-1]
	}
	return slice
}
