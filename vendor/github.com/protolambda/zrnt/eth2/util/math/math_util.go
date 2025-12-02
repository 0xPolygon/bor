package math

import "math"

func MaxU64(a uint64, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func MinU64(a uint64, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// The largest integer x such that x**2 is less than or equal to n.
func IntegerSquareroot(n uint64) uint64 {
	x := n
	y := (x + 1) >> 1
	for y < x {
		x = y
		y = (x + n/x) >> 1
	}
	return x
}

var squareRootTable = map[uint64]uint64{
	4:       2,
	16:      4,
	64:      8,
	256:     16,
	1024:    32,
	4096:    64,
	16384:   128,
	65536:   256,
	262144:  512,
	1048576: 1024,
	4194304: 2048,
}

func IntegerSquareRootPrysm(n uint64) uint64 {
	if v, ok := squareRootTable[n]; ok {
		return v
	}

	return uint64(math.Sqrt(float64(n)))
}

func IsPowerOfTwo(n uint64) bool {
	return (n > 0) && (n&(n-1) == 0)
}

// NextPowerOfTwo returns the next power of two for the given number.
// It returns the number itself if it's a power of two.
func NextPowerOfTwo(in uint64) uint64 {
	v := in
	v--
	v |= v >> (1 << 0)
	v |= v >> (1 << 1)
	v |= v >> (1 << 2)
	v |= v >> (1 << 3)
	v |= v >> (1 << 4)
	v |= v >> (1 << 5)
	v++
	return v
}
