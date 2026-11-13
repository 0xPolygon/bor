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

// Package pqsig differentially fuzzes the post-quantum signature verifiers
// behind the ML-DSA-65 and SLH-DSA-SHA2-128s precompiles against independent
// implementations: Go's crypto/mldsa vs circl, and circl's SLH-DSA vs
// trailofbits/go-slh-dsa.
package pqsig

import (
	"bytes"
	"crypto"
	"crypto/mldsa"
	_ "embed"
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/slhdsa"
	tob "github.com/trailofbits/go-slh-dsa/slh_dsa"
)

// mutate applies byte edits (3 bytes each: big-endian position, xor value)
// to a copy of b.
func mutate(b, edits []byte) []byte {
	out := bytes.Clone(b)
	for i := 0; i+2 < len(edits); i += 3 {
		pos := (int(edits[i])<<8 | int(edits[i+1])) % len(out)
		out[pos] ^= edits[i+2]
	}
	return out
}

// fuzzMLDSA65 signs a message with a key derived from data, mutates
// pk || sig || msg, and checks that Go and circl agree. Any mutation that
// changes the input must be rejected by both.
func fuzzMLDSA65(data []byte) {
	if len(data) < 34 {
		return
	}
	sk, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), data[:32])
	if err != nil {
		panic(err)
	}
	msgLen := int(data[32]) % 64
	if len(data) < 33+msgLen {
		return
	}
	msg, edits := data[33:33+msgLen], data[33+msgLen:]
	sig, err := sk.SignDeterministic(msg, crypto.Hash(0))
	if err != nil {
		panic(err)
	}
	orig := concat(sk.PublicKey().Bytes(), sig, msg)
	in := mutate(orig, edits)
	pk, s, m := in[:mldsa.MLDSA65PublicKeySize], in[mldsa.MLDSA65PublicKeySize:mldsa.MLDSA65PublicKeySize+mldsa.MLDSA65SignatureSize], in[mldsa.MLDSA65PublicKeySize+mldsa.MLDSA65SignatureSize:]

	goOK := false
	if key, err := mldsa.NewPublicKey(mldsa.MLDSA65(), pk); err == nil {
		goOK = mldsa.Verify(key, m, s, nil) == nil
	}
	circlOK := false
	var ckey mldsa65.PublicKey
	if ckey.UnmarshalBinary(pk) == nil {
		circlOK = mldsa65.Verify(&ckey, m, nil, s)
	}
	if goOK != circlOK {
		panic(fmt.Sprintf("ML-DSA-65 mismatch: go %v circl %v, input %x", goOK, circlOK, in))
	}
	if changed := !bytes.Equal(in, orig); goOK == changed {
		panic(fmt.Sprintf("ML-DSA-65 verdict %v with changed=%v, input %x", goOK, changed, in))
	}
}

// slhSeeds holds valid SLH-DSA-SHA2-128s inputs (pk || sig || msg) signed by
// circl and by OpenSSL. Signing 128s takes ~100 ms, too slow per fuzz input.
//
//go:embed testdata/slhdsa_seeds.bin
var slhSeedsRaw []byte

const slhInputLen = 32 + 7856 + 32 // every seed signs a 32-byte message

// fuzzSLHDSA mutates a valid SLH-DSA-SHA2-128s input and checks that circl
// and trailofbits agree. Any change must be rejected by both.
func fuzzSLHDSA(data []byte) {
	if len(data) < 1 {
		return
	}
	n := len(slhSeedsRaw) / slhInputLen
	orig := slhSeedsRaw[int(data[0])%n*slhInputLen:][:slhInputLen]
	in := mutate(orig, data[1:])
	pk, sig, msg := in[:32], in[32:32+7856], in[32+7856:]

	circlOK := false
	key := slhdsa.PublicKey{ID: slhdsa.SHA2_128s}
	if key.UnmarshalBinary(pk) == nil {
		circlOK = slhdsa.Verify(&key, slhdsa.NewMessage(msg), sig, nil)
	}
	tobOK := false
	params := tob.SlhDsaSha2_128s()
	if tpk, err := tob.LoadPublicKey(params, pk); err == nil {
		if tsig, err := tob.LoadSignature(params, sig); err == nil {
			tobOK = tob.SLHVerify(msg, tsig, nil, tpk)
		}
	}
	if circlOK != tobOK {
		panic(fmt.Sprintf("SLH-DSA mismatch: circl %v trailofbits %v, input %x", circlOK, tobOK, in))
	}
	if changed := !bytes.Equal(in, orig); circlOK == changed {
		panic(fmt.Sprintf("SLH-DSA verdict %v with changed=%v, input %x", circlOK, changed, in))
	}
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}
