package wit2test_test

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/wit2test"
)

// TestWitnessPadCrossesPageBoundaryAndRoundTrips de-risks the Phase 2 large-witness
// knob: a witness padded via WitnessPadNodes must (1) cross the 15 MB page boundary
// so transport splits it into multiple pages, (2) still RLP round-trip cleanly so
// stateless nodes can decode it, and (3) produce a deterministic WitnessCommitHash
// for a given seed (required so the producer and witness-producing relays agree on
// the signed hash). Orphan pad nodes are never looked up during execution (lookups
// are by keccak(node)), so they inflate size without breaking decode.
func TestWitnessPadCrossesPageBoundaryAndRoundTrips(t *testing.T) {
	const pageSize = 15 * 1024 * 1024 // matches eth.PageSize
	const padTarget = 20 * 1024 * 1024

	hdr := &types.Header{Number: big.NewInt(12345)}
	seed := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04}

	w, err := stateless.NewWitness(hdr, nil)
	if err != nil {
		t.Fatalf("NewWitness: %v", err)
	}
	w.AddState(wit2test.WitnessPadNodes(seed, padTarget))

	var buf bytes.Buffer
	if err := w.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP padded witness: %v", err)
	}
	size := buf.Len()
	if size <= pageSize {
		t.Fatalf("padded witness %d bytes did not cross the %d page boundary", size, pageSize)
	}
	pages := (size + pageSize - 1) / pageSize
	t.Logf("padded witness = %d bytes => %d transport pages", size, pages)
	if pages < 2 {
		t.Fatalf("expected multi-page witness, got %d page(s)", pages)
	}

	// (2) decodes cleanly back into a Witness.
	var decoded stateless.Witness
	if err := rlp.DecodeBytes(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode padded witness: %v", err)
	}
	if decoded.Header().Number.Uint64() != hdr.Number.Uint64() {
		t.Fatalf("decoded header number mismatch: got %d", decoded.Header().Number.Uint64())
	}

	// (3) deterministic: same seed => byte-identical encoding => identical commit hash.
	w2, _ := stateless.NewWitness(hdr, nil)
	w2.AddState(wit2test.WitnessPadNodes(seed, padTarget))
	var buf2 bytes.Buffer
	if err := w2.EncodeRLP(&buf2); err != nil {
		t.Fatalf("EncodeRLP second padded witness: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), buf2.Bytes()) {
		t.Fatalf("padding not deterministic: encodings differ (%d vs %d bytes)", buf.Len(), buf2.Len())
	}
	if stateless.WitnessCommitHash(buf.Bytes()) != stateless.WitnessCommitHash(buf2.Bytes()) {
		t.Fatalf("commit hash not deterministic for identical seed")
	}

	// Different seed must produce a different witness (so distinct blocks differ).
	w3, _ := stateless.NewWitness(hdr, nil)
	w3.AddState(wit2test.WitnessPadNodes([]byte{0x99, 0x88}, padTarget))
	var buf3 bytes.Buffer
	_ = w3.EncodeRLP(&buf3)
	if bytes.Equal(buf.Bytes(), buf3.Bytes()) {
		t.Fatalf("different seeds produced identical padded witnesses")
	}
}
