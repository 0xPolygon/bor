// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package stateless

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestWitnessCommitHashDeterministic(t *testing.T) {
	in := bytes.Repeat([]byte{0xab}, 5*WitnessCommitChunkBytes+1234)
	a := WitnessCommitHash(in)
	b := WitnessCommitHash(in)
	if a != b {
		t.Fatalf("non-deterministic: %s vs %s", a.Hex(), b.Hex())
	}
}

// TestWitnessCommitHashWorkerInvariant pins the load-bearing property: the
// committed hash MUST NOT depend on GOMAXPROCS. If it does, two honest peers
// running with different parallelism would diverge on the same witness.
func TestWitnessCommitHashWorkerInvariant(t *testing.T) {
	in := bytes.Repeat([]byte{0xcd}, 6*WitnessCommitChunkBytes+777)
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	one := WitnessCommitHash(in)

	runtime.GOMAXPROCS(8)
	eight := WitnessCommitHash(in)

	if one != eight {
		t.Fatalf("hash depends on GOMAXPROCS: 1=%s 8=%s", one.Hex(), eight.Hex())
	}
}

// TestWitnessCommitHashEmptyInput pins the empty-witness behavior so producer
// and verifier agree on the degenerate case.
func TestWitnessCommitHashEmptyInput(t *testing.T) {
	if got := WitnessCommitHash(nil); got != (common.Hash{}) {
		t.Fatalf("expected zero hash for nil, got %s", got.Hex())
	}
	if got := WitnessCommitHash([]byte{}); got != (common.Hash{}) {
		t.Fatalf("expected zero hash for empty slice, got %s", got.Hex())
	}
}

// TestWitnessCommitHashSingleSubChunk pins the small-input shape: an input
// shorter than one chunk hashes to keccak256(keccak256(input)), since the
// scheme always wraps a final aggregate-keccak around the chunk-hash list.
func TestWitnessCommitHashSingleSubChunk(t *testing.T) {
	in := bytes.Repeat([]byte{0x42}, 4096)
	got := WitnessCommitHash(in)

	inner := crypto.Keccak256Hash(in)
	want := crypto.Keccak256Hash(inner[:])
	if got != want {
		t.Fatalf("single-subchunk shape mismatch: got %s want %s", got.Hex(), want.Hex())
	}
}

// TestWitnessCommitHashMultiChunkShape spot-checks the multi-chunk recipe so a
// silent change in concat order or chunking would be caught immediately.
func TestWitnessCommitHashMultiChunkShape(t *testing.T) {
	a := bytes.Repeat([]byte{0x01}, WitnessCommitChunkBytes)
	b := bytes.Repeat([]byte{0x02}, WitnessCommitChunkBytes)
	c := bytes.Repeat([]byte{0x03}, 1234)
	in := append(append(append([]byte{}, a...), b...), c...)

	ha := crypto.Keccak256Hash(a)
	hb := crypto.Keccak256Hash(b)
	hc := crypto.Keccak256Hash(c)
	concat := append(append(append([]byte{}, ha[:]...), hb[:]...), hc[:]...)
	want := crypto.Keccak256Hash(concat)

	if got := WitnessCommitHash(in); got != want {
		t.Fatalf("multi-chunk shape mismatch: got %s want %s", got.Hex(), want.Hex())
	}
}
