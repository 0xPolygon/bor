package stateless

import (
	"encoding/binary"
	"errors"
	"math/big"
	"runtime"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func boundsTestHeaders(n int) []*types.Header {
	headers := make([]*types.Header, n)
	for i := range headers {
		headers[i] = &types.Header{Number: big.NewInt(int64(1000 - i))}
	}
	return headers
}

// boundsTestNodes returns n distinct state nodes of the given size.
func boundsTestNodes(n, size int) [][]byte {
	nodes := make([][]byte, n)
	for i := range nodes {
		nodes[i] = make([]byte, size)
		binary.BigEndian.PutUint64(nodes[i], uint64(i)+1)
	}
	return nodes
}

func encodeBoundsTestWitness(t *testing.T, headers int, state [][]byte) []byte {
	t.Helper()
	enc, err := rlp.EncodeToBytes(&BorWitness{
		Context: &types.Header{Number: big.NewInt(1001)},
		Headers: boundsTestHeaders(headers),
		State:   state,
	})
	if err != nil {
		t.Fatalf("encode witness: %v", err)
	}
	return enc
}

func encodeBoundsTestExtWitness(t *testing.T, codes int) []byte {
	t.Helper()
	enc, err := rlp.EncodeToBytes(&ExtWitness{
		Context: &types.Header{Number: big.NewInt(1001)},
		Headers: boundsTestHeaders(1),
		Codes:   make([]hexutil.Bytes, codes),
		State:   []hexutil.Bytes{},
		Keys:    []hexutil.Bytes{},
	})
	if err != nil {
		t.Fatalf("encode ext witness: %v", err)
	}
	return enc
}

func TestDecodeRLPWitnessListBounds(t *testing.T) {
	// A full-size message of 32-byte nodes, the densest honest witness, kept
	// under the 16 MiB wit message cap.
	const maxMessageSize = 16 * 1024 * 1024
	densestNodes := (maxMessageSize - 512*1024) / 33

	tests := []struct {
		name    string
		enc     []byte
		wantErr error
	}{
		{"max headers", encodeBoundsTestWitness(t, maxWitnessHeaders, nil), nil},
		{"too many headers", encodeBoundsTestWitness(t, maxWitnessHeaders+1, nil), errWitnessTooManyHeaders},
		{"full message of minimum size nodes", encodeBoundsTestWitness(t, maxWitnessHeaders, boundsTestNodes(densestNodes, minWitnessNodeSize)), nil},
		{"small nodes within allowance", encodeBoundsTestWitness(t, 1, boundsTestNodes(witnessElementAllowance, 8)), nil},
		{"empty nodes over bound", encodeBoundsTestWitness(t, 1, make([][]byte, 1100)), errWitnessTooManyElements},
		{"message of empty nodes", encodeBoundsTestWitness(t, 1, make([][]byte, 1<<20)), errWitnessTooManyElements},
		{"legacy codes within allowance", encodeBoundsTestExtWitness(t, witnessElementAllowance), nil},
		{"legacy codes over bound", encodeBoundsTestExtWitness(t, 1<<20), errWitnessTooManyElements},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.enc) > maxMessageSize {
				t.Fatalf("test witness is %d bytes, over the message cap", len(tt.enc))
			}
			var w Witness
			err := rlp.DecodeBytes(tt.enc, &w)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("decode error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeRLPWitnessRejectsBeforeAllocating(t *testing.T) {
	enc := encodeBoundsTestWitness(t, 1, make([][]byte, 1<<20))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var w Witness
	err := rlp.DecodeBytes(enc, &w)
	runtime.ReadMemStats(&after)

	if !errors.Is(err, errWitnessTooManyElements) {
		t.Fatalf("decode error = %v, want %v", err, errWitnessTooManyElements)
	}
	// The raw copy of the input is the only allocation proportional to it.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2*uint64(len(enc)) {
		t.Fatalf("rejected decode allocated %d bytes for a %d-byte input", allocated, len(enc))
	}
}

func TestCheckWitnessListBounds(t *testing.T) {
	// Witnesses of n empty State elements, one encoded byte each, on either side
	// of the size-derived bound.
	atBound := witnessElementAllowance * minWitnessNodeSize / (minWitnessNodeSize - 1)
	emptyState := func(n int) []byte {
		enc, err := rlp.EncodeToBytes([]interface{}{[]byte{}, []interface{}{}, make([][]byte, n)})
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}
	stringHeaders, err := rlp.EncodeToBytes([]interface{}{[]byte{}, make([]byte, maxWitnessHeaders+1), []interface{}{}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		raw     []byte
		wantErr string // substring; empty means no error
	}{
		{"elements at bound", emptyState(atBound), ""},
		{"elements over bound", emptyState(atBound + 1), errWitnessTooManyElements.Error()},
		{"non-list headers are left to the decoder", stringHeaders, ""},
		{"not a list", []byte{0x80}, rlp.ErrExpectedList.Error()},
		{"missing context", []byte{0xc0}, "witness context"},
		{"truncated headers", []byte{0xc2, 0x80, 0xc5}, "witness headers"},
		{"malformed header list", []byte{0xc3, 0x80, 0xc1, 0x81}, "witness headers"},
		{"truncated state", []byte{0xc3, 0x80, 0xc0, 0xc5}, "witness state"},
		{"malformed state list", []byte{0xc4, 0x80, 0xc0, 0xc1, 0x81}, "witness state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkWitnessListBounds(tt.raw)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
