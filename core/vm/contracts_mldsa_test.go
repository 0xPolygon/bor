package vm

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/mldsa/mldsa87"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// Vectors: Wycheproof (C2SP), EIP-8355 assets and signatures made by Apple's
// Secure Enclave (CryptoKit) for ML-DSA-65/87.
func TestPrecompiledMLDSA44(t *testing.T) { testJson("mldsa44", "12", t) }
func TestPrecompiledMLDSA65(t *testing.T) { testJson("mldsa65", "13", t) }
func TestPrecompiledMLDSA87(t *testing.T) { testJson("mldsa87", "14", t) }

func BenchmarkPrecompiledMLDSA44(b *testing.B) { benchJson("mldsa44", "12", b) }
func BenchmarkPrecompiledMLDSA65(b *testing.B) { benchJson("mldsa65", "13", b) }
func BenchmarkPrecompiledMLDSA87(b *testing.B) { benchJson("mldsa87", "14", b) }

type mldsaSigner func(msg, ctx []byte) (pk, sig []byte)

func newMLDSASigner(t testing.TB, c *mldsaVerify) mldsaSigner {
	t.Helper()
	switch c {
	case mldsa44Verify:
		pk, sk, err := mldsa44.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return func(msg, ctx []byte) ([]byte, []byte) {
			sig := make([]byte, mldsa44.SignatureSize)
			if err := mldsa44.SignTo(sk, msg, ctx, true, sig); err != nil {
				t.Fatal(err)
			}
			return pk.Bytes(), sig
		}
	case mldsa65Verify:
		pk, sk, err := mldsa65.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return func(msg, ctx []byte) ([]byte, []byte) {
			sig := make([]byte, mldsa65.SignatureSize)
			if err := mldsa65.SignTo(sk, msg, ctx, true, sig); err != nil {
				t.Fatal(err)
			}
			return pk.Bytes(), sig
		}
	default:
		pk, sk, err := mldsa87.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return func(msg, ctx []byte) ([]byte, []byte) {
			sig := make([]byte, mldsa87.SignatureSize)
			if err := mldsa87.SignTo(sk, msg, ctx, true, sig); err != nil {
				t.Fatal(err)
			}
			return pk.Bytes(), sig
		}
	}
}

func runMLDSA(t *testing.T, c *mldsaVerify, input []byte) bool {
	t.Helper()
	gas := c.RequiredGas(input)
	out, left, err := RunPrecompiledContract(c, input, gas, nil)
	if err != nil {
		t.Fatalf("%s: unexpected error %v", c.Name(), err)
	}
	if left != 0 {
		t.Fatalf("%s: %d gas left over", c.Name(), left)
	}
	if len(out) != 32 {
		t.Fatalf("%s: output length %d, want 32", c.Name(), len(out))
	}
	switch {
	case bytes.Equal(out, true32Byte):
		return true
	case bytes.Equal(out, common.Hash{}.Bytes()):
		return false
	default:
		t.Fatalf("%s: output %x is neither 0 nor 1", c.Name(), out)
		return false
	}
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// TestMLDSAVerifyMutations signs with fresh keys and checks that every change
// to the key, signature, message or context is rejected.
func TestMLDSAVerifyMutations(t *testing.T) {
	for _, c := range []*mldsaVerify{mldsa44Verify, mldsa65Verify, mldsa87Verify} {
		t.Run(c.Name(), func(t *testing.T) {
			sign := newMLDSASigner(t, c)
			for _, msg := range [][]byte{nil, {0}, []byte("Hello world"), bytes.Repeat([]byte{0xab}, 4096)} {
				pk, sig := sign(msg, nil)
				if !runMLDSA(t, c, concat(pk, sig, msg)) {
					t.Fatalf("valid signature rejected (msg len %d)", len(msg))
				}
				for _, i := range []int{0, 31, 32, len(pk) - 1} {
					bad := bytes.Clone(pk)
					bad[i] ^= 1
					if runMLDSA(t, c, concat(bad, sig, msg)) {
						t.Errorf("public key byte %d flipped: accepted", i)
					}
				}
				for _, i := range []int{0, len(sig) / 2, len(sig) - 1} {
					bad := bytes.Clone(sig)
					bad[i] ^= 1
					if runMLDSA(t, c, concat(pk, bad, msg)) {
						t.Errorf("signature byte %d flipped: accepted", i)
					}
				}
				if runMLDSA(t, c, concat(pk, sig, msg, []byte{0})) {
					t.Error("extra trailing byte: accepted")
				}
				if len(msg) > 0 && runMLDSA(t, c, concat(pk, sig, msg[:len(msg)-1])) {
					t.Error("truncated message: accepted")
				}
			}
			// The precompile always uses the empty context, so a signature over
			// any other context must fail, even though it is valid under FIPS 204.
			msg := []byte("Hello world")
			for _, ctx := range [][]byte{{0}, []byte("polygon"), bytes.Repeat([]byte{0x41}, 255)} {
				pk, sig := sign(msg, ctx)
				if runMLDSA(t, c, concat(pk, sig, msg)) {
					t.Errorf("signature with context %q accepted", ctx)
				}
			}
			// Short inputs never verify and pay only the base cost.
			pk, sig := sign(msg, nil)
			for _, in := range [][]byte{nil, pk, concat(pk, sig[:len(sig)-1])} {
				if runMLDSA(t, c, in) {
					t.Errorf("short input (%d bytes) accepted", len(in))
				}
				if gas := c.RequiredGas(in); gas != c.baseGas {
					t.Errorf("short input gas %d, want %d", gas, c.baseGas)
				}
			}
		})
	}
}

func TestMLDSAVerifyGas(t *testing.T) {
	c := mldsa65Verify
	head := c.pkLen + c.sigLen
	for _, tt := range []struct {
		msgLen int
		want   uint64
	}{
		{0, params.MLDSA65VerifyBaseGas},
		{1, params.MLDSA65VerifyBaseGas + 6},
		{32, params.MLDSA65VerifyBaseGas + 6},
		{33, params.MLDSA65VerifyBaseGas + 12},
		{65536, params.MLDSA65VerifyBaseGas + 6*2048},
	} {
		if got := c.RequiredGas(make([]byte, head+tt.msgLen)); got != tt.want {
			t.Errorf("message %d bytes: gas %d, want %d", tt.msgLen, got, tt.want)
		}
	}
}

// FuzzMLDSA65Verify checks that arbitrary input never errors, never panics,
// never changes the input and always returns a 32-byte 0 or 1.
func FuzzMLDSA65Verify(f *testing.F) {
	tests, err := loadJson("mldsa65")
	if err != nil {
		f.Fatal(err)
	}
	for _, test := range tests {
		f.Add(common.Hex2Bytes(test.Input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		in := bytes.Clone(input)
		out, err := mldsa65Verify.Run(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 32 || (!bytes.Equal(out, true32Byte) && !bytes.Equal(out, common.Hash{}.Bytes())) {
			t.Fatalf("bad output %x", out)
		}
		if !bytes.Equal(in, input) {
			t.Fatal("input modified")
		}
	})
}
