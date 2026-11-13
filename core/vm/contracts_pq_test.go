// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"bytes"
	"crypto"
	"crypto/mldsa"
	"crypto/rand"
	"math"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/slhdsa"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// Vectors: Wycheproof, NIST ACVP (ctx=""), Apple Secure Enclave and
// cross-verified circl/OpenSSL/trailofbits signatures; see
// testdata/precompiles/pqsig/README.md for sources and regeneration.
func TestPrecompiledMLDSA65Verify(t *testing.T) { testJson("mldsa65Verify", "0113", t) }
func TestPrecompiledSLHDSASha2128sVerify(t *testing.T) {
	testJson("slhdsaSha2128sVerify", "0115", t)
}

func BenchmarkPrecompiledMLDSA65Verify(b *testing.B) { benchJson("mldsa65Verify", "0113", b) }
func BenchmarkPrecompiledSLHDSASha2128sVerify(b *testing.B) {
	benchJson("slhdsaSha2128sVerify", "0115", b)
}

const (
	mldsa65CTildeSize = 48
	mldsa65ZSize      = 5 * 640 // l polynomials of 20-bit coefficients
	mldsa65HintOffset = mldsa65CTildeSize + mldsa65ZSize
	mldsa65Omega      = 55
	mldsa65K          = 6

	slhdsaN          = 16
	slhdsaForsTrees  = 14
	slhdsaForsHeight = 12
	slhdsaLayers     = 7
	slhdsaXmssHeight = 9
	slhdsaWotsLen    = 35
)

var (
	mldsa65Precompile = &mldsa65Verify{}
	slhdsaPrecompile  = &slhdsaSha2128sVerify{}
)

// runPQ runs p and checks the invariants every call must keep: no error, a
// 32-byte 0/1 output and an unmodified input. It returns the verdict.
func runPQ(t *testing.T, p PrecompiledContract, input []byte) bool {
	t.Helper()
	orig := bytes.Clone(input)
	out, err := p.Run(input)
	if err != nil {
		t.Fatalf("%s returned error %v", p.Name(), err)
	}
	if !bytes.Equal(input, orig) {
		t.Fatalf("%s modified its input", p.Name())
	}
	switch {
	case bytes.Equal(out, true32Byte):
		return true
	case bytes.Equal(out, false32Byte):
		return false
	}
	t.Fatalf("%s returned %x, want 32-byte 0 or 1", p.Name(), out)
	return false
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

type mldsaFixture struct {
	pk, sig, msg []byte
}

func newMLDSAFixture(t *testing.T, msg []byte) mldsaFixture {
	t.Helper()
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := sk.Sign(nil, msg, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	return mldsaFixture{pk: sk.PublicKey().Bytes(), sig: sig, msg: msg}
}

func (f mldsaFixture) input() []byte { return concat(f.pk, f.sig, f.msg) }

// circlMLDSA65 is an independent ML-DSA-65 implementation, used as a
// differential oracle for every malformed signature below.
func circlMLDSA65(pk, sig, msg []byte) bool {
	var key mldsa65.PublicKey
	if key.UnmarshalBinary(pk) != nil {
		return false
	}
	return mldsa65.Verify(&key, msg, nil, sig)
}

// checkMLDSAReject asserts that both the precompile and circl reject sig.
func checkMLDSAReject(t *testing.T, name string, f mldsaFixture, sig []byte) {
	t.Helper()
	if runPQ(t, mldsa65Precompile, concat(f.pk, sig, f.msg)) {
		t.Errorf("%s: precompile accepted", name)
	}
	if circlMLDSA65(f.pk, sig, f.msg) {
		t.Errorf("%s: circl accepted", name)
	}
}

func TestMLDSA65VerifyFreshSignatures(t *testing.T) {
	for _, n := range []int{0, 1, 31, 32, 33, 1024, 1 << 16} {
		msg := make([]byte, n)
		rand.Read(msg)
		f := newMLDSAFixture(t, msg)
		if !runPQ(t, mldsa65Precompile, f.input()) {
			t.Errorf("valid %d-byte message rejected", n)
		}
		if !circlMLDSA65(f.pk, f.sig, f.msg) {
			t.Errorf("circl rejected valid %d-byte message", n)
		}
	}
}

// TestMLDSA65VerifyHintEncoding covers FIPS 204 Algorithm 21 (HintBitUnpack):
// every non-canonical hint encoding must be rejected.
func TestMLDSA65VerifyHintEncoding(t *testing.T) {
	// Retry until a signature has a polynomial with two hints and some
	// padding left over; almost every signature does.
	var (
		f            mldsaFixture
		poly, first  = -1, 0
		total, start int
	)
	for poly < 0 {
		f = newMLDSAFixture(t, []byte("hint encoding"))
		y := f.sig[mldsa65HintOffset:]
		prev := 0
		for i := 0; i < mldsa65K; i++ {
			end := int(y[mldsa65Omega+i])
			if end-prev >= 2 {
				poly, first = i, prev
			}
			prev = end
		}
		total = prev
		if total == mldsa65Omega {
			poly = -1
		}
	}
	start = mldsa65HintOffset
	mutate := func(name string, fn func(y []byte)) {
		sig := bytes.Clone(f.sig)
		fn(sig[start:])
		checkMLDSAReject(t, name, f, sig)
	}
	mutate("indices swapped", func(y []byte) { y[first], y[first+1] = y[first+1], y[first] })
	mutate("indices equal", func(y []byte) { y[first+1] = y[first] })
	mutate("limit above omega", func(y []byte) { y[mldsa65Omega+mldsa65K-1] = mldsa65Omega + 1 })
	mutate("limit 255", func(y []byte) { y[mldsa65Omega+mldsa65K-1] = 255 })
	mutate("nonzero padding", func(y []byte) { y[mldsa65Omega-1] = 1 })
	if poly > 0 {
		mutate("limits decreasing", func(y []byte) { y[mldsa65Omega+poly-1] = y[mldsa65Omega+poly] + 1 })
	}
	mutate("hint dropped", func(y []byte) {
		for i := poly; i < mldsa65K; i++ {
			y[mldsa65Omega+i]--
		}
	})
}

// setZ0 overwrites the first z coefficient. z is packed as γ1 - z in 20 bits.
func setZ0(sig []byte, z int32) {
	const gamma1 = 1 << 19
	v := uint32(gamma1 - z)
	b := sig[mldsa65CTildeSize:]
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = b[2]&0xf0 | byte(v>>16)&0x0f
}

// TestMLDSA65VerifyZBound covers the ‖z‖∞ < γ1 − β check of FIPS 204
// Algorithm 8, line 13, at and around the boundary.
func TestMLDSA65VerifyZBound(t *testing.T) {
	const bound = 1<<19 - 49*4 // γ1 - β
	f := newMLDSAFixture(t, []byte("z bound"))
	for _, z := range []int32{bound, -bound, bound - 1, -(bound - 1), 1 << 19, -(1<<19 - 1)} {
		sig := bytes.Clone(f.sig)
		setZ0(sig, z)
		checkMLDSAReject(t, "z0", f, sig)
	}
}

func TestMLDSA65VerifyMalformed(t *testing.T) {
	f := newMLDSAFixture(t, []byte("malformed"))
	for i := 0; i < mldsa65CTildeSize; i++ {
		sig := bytes.Clone(f.sig)
		sig[i] ^= 0x01
		checkMLDSAReject(t, "c tilde", f, sig)
	}
	checkMLDSAReject(t, "zero c tilde", f, concat(make([]byte, mldsa65CTildeSize), f.sig[mldsa65CTildeSize:]))
	checkMLDSAReject(t, "zero signature", f, make([]byte, len(f.sig)))
	for _, i := range []int{0, 31, 32, 1000, len(f.pk) - 1} {
		pk := bytes.Clone(f.pk)
		pk[i] ^= 0x80
		if runPQ(t, mldsa65Precompile, concat(pk, f.sig, f.msg)) {
			t.Errorf("public key byte %d flipped: accepted", i)
		}
	}
	if runPQ(t, mldsa65Precompile, concat(make([]byte, len(f.pk)), f.sig, f.msg)) {
		t.Error("zero public key accepted")
	}
	for _, msg := range [][]byte{nil, f.msg[1:], append(bytes.Clone(f.msg), 0)} {
		if runPQ(t, mldsa65Precompile, concat(f.pk, f.sig, msg)) {
			t.Errorf("changed message %x accepted", msg)
		}
	}
}

// TestMLDSA65VerifyContext checks that only pure ML-DSA with the empty
// context verifies: signatures with any context must give 0.
func TestMLDSA65VerifyContext(t *testing.T) {
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("context")
	for _, ctx := range []string{"x", string(make([]byte, 255))} {
		sig, err := sk.Sign(nil, msg, &mldsa.Options{Context: ctx})
		if err != nil {
			t.Fatal(err)
		}
		if runPQ(t, mldsa65Precompile, concat(sk.PublicKey().Bytes(), sig, msg)) {
			t.Errorf("%d-byte context accepted", len(ctx))
		}
	}
}

// TestMLDSA65VerifyOtherParameterSets feeds ML-DSA-44 and ML-DSA-87 keys and
// signatures, which have different sizes, to the ML-DSA-65 precompile.
func TestMLDSA65VerifyOtherParameterSets(t *testing.T) {
	msg := []byte("other parameter sets")
	for _, params := range []mldsa.Parameters{mldsa.MLDSA44(), mldsa.MLDSA87()} {
		sk, err := mldsa.GenerateKey(params)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := sk.Sign(nil, msg, crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		if runPQ(t, mldsa65Precompile, concat(sk.PublicKey().Bytes(), sig, msg)) {
			t.Errorf("%v input accepted", params)
		}
	}
}

type slhFixture struct {
	pk, sig, msg []byte
	priv         slhdsa.PrivateKey
}

func newSLHFixture(t *testing.T, msg []byte) slhFixture {
	t.Helper()
	pub, priv, err := slhdsa.GenerateKey(rand.Reader, slhdsa.SHA2_128s)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := slhdsa.SignRandomized(&priv, rand.Reader, slhdsa.NewMessage(msg), nil)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := pub.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return slhFixture{pk: pk, sig: sig, msg: msg, priv: priv}
}

func (f slhFixture) input() []byte { return concat(f.pk, f.sig, f.msg) }

func flipBit(b []byte, bit int) []byte {
	c := bytes.Clone(b)
	c[bit/8] ^= 1 << (bit % 8)
	return c
}

// TestSLHDSAVerifyBitFlips flips every bit of the public key and of R, and
// bits in every FORS tree and every hypertree layer (WOTS+ part and
// authentication path).
func TestSLHDSAVerifyBitFlips(t *testing.T) {
	f := newSLHFixture(t, []byte("bit flips"))
	if !runPQ(t, slhdsaPrecompile, f.input()) {
		t.Fatal("valid signature rejected")
	}
	reject := func(name string, pk, sig, msg []byte) {
		t.Helper()
		if runPQ(t, slhdsaPrecompile, concat(pk, sig, msg)) {
			t.Errorf("%s: accepted", name)
		}
	}
	for bit := 0; bit < 8*len(f.pk); bit++ {
		reject("pk", flipBit(f.pk, bit), f.sig, f.msg)
	}
	for bit := 0; bit < 8*slhdsaN; bit++ {
		reject("R", f.pk, flipBit(f.sig, bit), f.msg)
	}
	const forsTreeSize = slhdsaN * (1 + slhdsaForsHeight)
	for tree := 0; tree < slhdsaForsTrees; tree++ {
		base := 8 * (slhdsaN + tree*forsTreeSize)
		reject("FORS sk", f.pk, flipBit(f.sig, base+3), f.msg)
		reject("FORS auth", f.pk, flipBit(f.sig, base+8*(forsTreeSize-1)), f.msg)
	}
	const xmssSize = slhdsaN * (slhdsaWotsLen + slhdsaXmssHeight)
	htStart := slhdsaN + slhdsaForsTrees*forsTreeSize
	for layer := 0; layer < slhdsaLayers; layer++ {
		base := 8 * (htStart + layer*xmssSize)
		reject("WOTS+", f.pk, flipBit(f.sig, base+5), f.msg)
		reject("WOTS+ checksum chain", f.pk, flipBit(f.sig, base+8*slhdsaN*(slhdsaWotsLen-1)), f.msg)
		reject("XMSS auth", f.pk, flipBit(f.sig, base+8*(xmssSize-1)), f.msg)
	}
	for bit := 0; bit < 8*len(f.msg); bit += 7 {
		reject("message", f.pk, f.sig, flipBit(f.msg, bit))
	}
	reject("zero signature", f.pk, make([]byte, len(f.sig)), f.msg)
	reject("zero public key", make([]byte, len(f.pk)), f.sig, f.msg)
}

// TestSLHDSAVerifyRandomFlips flips random bits anywhere in the signature.
func TestSLHDSAVerifyRandomFlips(t *testing.T) {
	f := newSLHFixture(t, []byte("random flips"))
	var r [4]byte
	for i := 0; i < 500; i++ {
		rand.Read(r[:])
		bit := int(uint32(r[0])|uint32(r[1])<<8|uint32(r[2])<<16) % (8 * len(f.sig))
		if runPQ(t, slhdsaPrecompile, concat(f.pk, flipBit(f.sig, bit), f.msg)) {
			t.Fatalf("signature bit %d flipped: accepted", bit)
		}
	}
}

func TestSLHDSAVerifyContextAndPreHash(t *testing.T) {
	f := newSLHFixture(t, []byte("context"))
	for _, ctx := range [][]byte{{0x01}, make([]byte, 255)} {
		sig, err := slhdsa.SignRandomized(&f.priv, rand.Reader, slhdsa.NewMessage(f.msg), ctx)
		if err != nil {
			t.Fatal(err)
		}
		if runPQ(t, slhdsaPrecompile, concat(f.pk, sig, f.msg)) {
			t.Errorf("%d-byte context accepted", len(ctx))
		}
	}
	ph, err := slhdsa.NewPreHashWithHash(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	ph.Write(f.msg)
	pmsg, err := ph.BuildMessage()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := slhdsa.SignRandomized(&f.priv, rand.Reader, pmsg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runPQ(t, slhdsaPrecompile, concat(f.pk, sig, f.msg)) {
		t.Error("HashSLH-DSA signature accepted")
	}
}

func TestSLHDSAVerifyOtherParameterSets(t *testing.T) {
	msg := []byte("other parameter sets")
	for _, id := range []slhdsa.ID{slhdsa.SHAKE_128s, slhdsa.SHA2_128f} {
		pub, priv, err := slhdsa.GenerateKey(rand.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := slhdsa.SignRandomized(&priv, rand.Reader, slhdsa.NewMessage(msg), nil)
		if err != nil {
			t.Fatal(err)
		}
		pk, _ := pub.MarshalBinary()
		if runPQ(t, slhdsaPrecompile, concat(pk, sig, msg)) {
			t.Errorf("%v input accepted", id)
		}
	}
}

// TestPQVerifyCrossScheme sends each scheme's valid input to the other
// precompile.
func TestPQVerifyCrossScheme(t *testing.T) {
	ml := newMLDSAFixture(t, []byte("cross"))
	slh := newSLHFixture(t, []byte("cross"))
	if runPQ(t, slhdsaPrecompile, ml.input()) {
		t.Error("ML-DSA input accepted by SLH-DSA")
	}
	if runPQ(t, mldsa65Precompile, slh.input()) {
		t.Error("SLH-DSA input accepted by ML-DSA")
	}
}

// TestPQVerifyInputLengths covers every length boundary around pk || sig.
func TestPQVerifyInputLengths(t *testing.T) {
	ml := newMLDSAFixture(t, nil)
	slh := newSLHFixture(t, nil)
	for _, tc := range []struct {
		p     PrecompiledContract
		input []byte
		base  uint64
		fixed int
	}{
		{mldsa65Precompile, ml.input(), params.MLDSA65VerifyBaseGas, mldsa.MLDSA65PublicKeySize + mldsa.MLDSA65SignatureSize},
		{slhdsaPrecompile, slh.input(), params.SLHDSASha2128sVerifyBaseGas, slhdsaSha2128sPublicKeySize + slhdsaSha2128sSignatureSize},
	} {
		if len(tc.input) != tc.fixed {
			t.Fatalf("%s: fixture length %d, want %d", tc.p.Name(), len(tc.input), tc.fixed)
		}
		if !runPQ(t, tc.p, tc.input) {
			t.Errorf("%s: valid signature over the empty message rejected", tc.p.Name())
		}
		for _, n := range []int{0, 1, 31, 32, 33, tc.fixed - 1} {
			if runPQ(t, tc.p, tc.input[:n]) {
				t.Errorf("%s: %d-byte input accepted", tc.p.Name(), n)
			}
			if gas := tc.p.RequiredGas(tc.input[:n]); gas != tc.base {
				t.Errorf("%s: %d-byte input costs %d, want base %d", tc.p.Name(), n, gas, tc.base)
			}
		}
		if runPQ(t, tc.p, append(bytes.Clone(tc.input), 0)) {
			t.Errorf("%s: extra message byte accepted", tc.p.Name())
		}
	}
}

func TestPQVerifyGas(t *testing.T) {
	for _, tc := range []struct {
		fixed         int
		base, perWord uint64
	}{
		{mldsa.MLDSA65PublicKeySize + mldsa.MLDSA65SignatureSize, params.MLDSA65VerifyBaseGas, params.MLDSAVerifyPerWordGas},
		{slhdsaSha2128sPublicKeySize + slhdsaSha2128sSignatureSize, params.SLHDSASha2128sVerifyBaseGas, params.SLHDSAVerifyPerWordGas},
	} {
		for _, c := range []struct {
			inputLen int
			want     uint64
		}{
			{0, tc.base},
			{tc.fixed - 1, tc.base},
			{tc.fixed, tc.base},
			{tc.fixed + 1, tc.base + tc.perWord},
			{tc.fixed + 32, tc.base + tc.perWord},
			{tc.fixed + 33, tc.base + 2*tc.perWord},
			{tc.fixed + 1<<20, tc.base + (1<<20/32)*tc.perWord},
			{math.MaxInt, tc.base + toWordSize(uint64(math.MaxInt-tc.fixed))*tc.perWord},
		} {
			if got := pqVerifyGas(c.inputLen, tc.fixed, tc.base, tc.perWord); got != c.want {
				t.Errorf("fixed %d, input %d: gas %d, want %d", tc.fixed, c.inputLen, got, c.want)
			}
		}
		// Saturates instead of wrapping when the per-word price is huge.
		if got := pqVerifyGas(math.MaxInt, tc.fixed, tc.base, math.MaxUint64/2); got != math.MaxUint64 {
			t.Errorf("gas %d, want saturation at MaxUint64", got)
		}
	}
}

// TestPQVerifyNotActivated guards against an accidental activation: the
// precompiles are not part of any fork until their hard fork is defined.
func TestPQVerifyNotActivated(t *testing.T) {
	addrs := []common.Address{mldsa65VerifyAddr, slhdsaSha2128sVerifyAddr}
	for name, set := range map[string]PrecompiledContracts{
		"Homestead": PrecompiledContractsHomestead, "Byzantium": PrecompiledContractsByzantium,
		"Istanbul": PrecompiledContractsIstanbul, "Berlin": PrecompiledContractsBerlin,
		"Cancun": PrecompiledContractsCancun, "Prague": PrecompiledContractsPrague,
		"Osaka": PrecompiledContractsOsaka, "Verkle": PrecompiledContractsVerkle,
		"Madhugiri": PrecompiledContractsMadhugiri, "MadhugiriPro": PrecompiledContractsMadhugiriPro,
		"Lisovo": PrecompiledContractsLisovo, "LisovoPro": PrecompiledContractsLisovoPro,
		"Chicago": PrecompiledContractsChicago,
	} {
		for _, a := range addrs {
			if _, ok := set[a]; ok {
				t.Errorf("%v is active in %s", a, name)
			}
		}
	}
	for name, list := range map[string][]common.Address{
		"Homestead": PrecompiledAddressesHomestead, "Byzantium": PrecompiledAddressesByzantium,
		"Istanbul": PrecompiledAddressesIstanbul, "Berlin": PrecompiledAddressesBerlin,
		"Cancun": PrecompiledAddressesCancun, "Prague": PrecompiledAddressesPrague,
		"Osaka": PrecompiledAddressesOsaka, "Madhugiri": PrecompiledAddressesMadhugiri,
		"MadhugiriPro": PrecompiledAddressesMadhugiriPro, "Lisovo": PrecompiledAddressesLisovo,
		"LisovoPro": PrecompiledAddressesLisovoPro, "Chicago": PrecompiledAddressesChicago,
	} {
		for _, a := range list {
			if a == mldsa65VerifyAddr || a == slhdsaSha2128sVerifyAddr {
				t.Errorf("%v is in PrecompiledAddresses%s", a, name)
			}
		}
	}
	for a, p := range PrecompiledContractsPQ {
		if a != mldsa65VerifyAddr && a != slhdsaSha2128sVerifyAddr {
			t.Errorf("unexpected address %v (%s) in PrecompiledContractsPQ", a, p.Name())
		}
	}
	if mldsa65Precompile.Name() != "MLDSA65VERIFY" || slhdsaPrecompile.Name() != "SLHDSASHA2128SVERIFY" {
		t.Errorf("names %q, %q", mldsa65Precompile.Name(), slhdsaPrecompile.Name())
	}
	if len(PrecompiledContractsPQ) != 2 {
		t.Errorf("PrecompiledContractsPQ has %d entries, want 2", len(PrecompiledContractsPQ))
	}
}
