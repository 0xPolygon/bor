package hashing

import (
	"github.com/minio/sha256-simd"
)

// Hash the given input. Use a new hashing object, and ditch it after hashing.
// See GetHashFn for a more efficient approach for repeated hashing.
// Defaults to SHA-256.
var Hash HashFn = sha256.Sum256

// Hashes the input, and returns the hash as a byte slice
type HashFn func(input []byte) [32]byte

type NewHashFn func() HashFn

// re-uses the sha256 working variables for each new call of a allocated hash-function.
func Sha256Repeat() HashFn {
	h := sha256.New()
	hashFn := func(in []byte) (out [32]byte) {
		h.Reset()
		h.Write(in)
		copy(out[:], h.Sum(nil))
		return
	}
	return hashFn
}

// Get a hash-function that re-uses the hashing working-variables. Defaults to SHA-256.
var GetHashFn NewHashFn = Sha256Repeat

func XorBytes32(a [32]byte, b [32]byte) (out [32]byte) {
	for i := 0; i < 32; i++ {
		out[i] = a[i] ^ b[i]
	}
	return
}
