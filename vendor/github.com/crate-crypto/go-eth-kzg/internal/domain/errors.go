package domain

import "errors"

var ErrPolynomialMismatchedSizeDomain = errors.New("domain size does not equal the number of evaluations in the polynomial")
