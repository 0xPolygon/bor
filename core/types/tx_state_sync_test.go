package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestStateSyncHashing_Sensitivity_UsingTxDataCopy(t *testing.T) {
	base := &StateSyncTx{
		StateSyncData: []*StateSyncData{
			{
				ID:       1,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001001"),
				Data:     []byte("alpha"),
				TxHash:   common.HexToHash("0x1111"),
			},
			{
				ID:       2,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001002"),
				Data:     []byte("beta"),
				TxHash:   common.HexToHash("0x2222"),
			},
		},
	}

	hashOf := func(mutate func(*StateSyncTx)) (h common.Hash) {
		st := base.copy().(*StateSyncTx)
		if mutate != nil {
			mutate(st)
		}
		return NewTx(st).Hash()
	}

	baseHash := hashOf(nil)

	// Determinism: hashing twice on same content must be identical.
	if got := hashOf(nil); got != baseHash {
		t.Fatalf("base hash not deterministic: %x vs %x", baseHash, got)
	}

	// Identical copy (via TxData.copy) must equal base (control).
	if got := hashOf(func(st *StateSyncTx) { /* no-op */ }); got != baseHash {
		t.Fatalf("identical copy should keep same hash: %x vs %x", baseHash, got)
	}

	type mutCase struct {
		name   string
		mutate func(*StateSyncTx)
	}

	cases := []mutCase{
		{
			name: "change ID of first entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[0].ID++
			},
		},
		{
			name: "change Contract of first entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[0].Contract = common.HexToAddress("0x000000000000000000000000000000000000CAFE")
			},
		},
		{
			name: "change Data of first entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[0].Data = []byte("alpha-mutated")
			},
		},
		{
			name: "change TxHash of first entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[0].TxHash = common.HexToHash("0xAAAA")
			},
		},
		{
			name: "change all fields in second entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[1].ID += 10
				st.StateSyncData[1].Contract = common.HexToAddress("0x000000000000000000000000000000000000BEEF")
				st.StateSyncData[1].Data = []byte("beta-mutated")
				st.StateSyncData[1].TxHash = common.HexToHash("0xBBBB")
			},
		},
		{
			name: "append a new entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData = append(st.StateSyncData, &StateSyncData{
					ID:       3,
					Contract: common.HexToAddress("0x0000000000000000000000000000000000001003"),
					Data:     []byte("gamma"),
					TxHash:   common.HexToHash("0x3333"),
				})
			},
		},
		{
			name: "remove the first entry",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData = st.StateSyncData[1:]
			},
		},
		{
			name: "swap order of entries",
			mutate: func(st *StateSyncTx) {
				st.StateSyncData[0], st.StateSyncData[1] = st.StateSyncData[1], st.StateSyncData[0]
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hashOf(tc.mutate)
			if got == baseHash {
				t.Fatalf("%s: hash DID NOT change; expected different (base=%x, got=%x)", tc.name, baseHash, got)
			}
		})
	}
}

func makeBase() *StateSyncTx {
	return &StateSyncTx{
		StateSyncData: []*StateSyncData{
			{
				ID:       1,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001001"),
				Data:     []byte("alpha"),
				TxHash:   common.HexToHash("0x1111"),
			},
			// keep a middle element for order-preservation checks
			{
				ID:       2,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001002"),
				Data:     []byte("beta"),
				TxHash:   common.HexToHash("0x2222"),
			},
		},
	}
}

func TestStateSyncTx_EncodeDecode_RoundTrip(t *testing.T) {
	orig := makeBase()

	var buf bytes.Buffer
	if err := orig.encode(&buf); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	enc := buf.Bytes()

	var rt StateSyncTx
	if err := rt.decode(enc); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Deep equality (pointers can differ; values must match)
	if !reflect.DeepEqual(orig, &rt) {
		t.Fatalf("round-trip mismatch:\n  orig=%#v\n  rt=%#v", orig, &rt)
	}

	// Re-encode should be byte-for-byte identical
	var buf2 bytes.Buffer
	if err := rt.encode(&buf2); err != nil {
		t.Fatalf("re-encode failed: %v", err)
	}
	if !bytes.Equal(enc, buf2.Bytes()) {
		t.Fatalf("encode not deterministic: first=%x second=%x", enc, buf2.Bytes())
	}
}

func TestStateSyncTx_Encode_SkipsNilAndPreservesOrder(t *testing.T) {
	// Build a tx with a nil in the middle; encode should drop it.
	withNil := &StateSyncTx{
		StateSyncData: []*StateSyncData{
			{
				ID:       1,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001001"),
				Data:     []byte("alpha"),
				TxHash:   common.HexToHash("0x1111"),
			},
			nil, // should be filtered out by encode
			{
				ID:       2,
				Contract: common.HexToAddress("0x0000000000000000000000000000000000001002"),
				Data:     []byte("beta"),
				TxHash:   common.HexToHash("0x2222"),
			},
		},
	}

	var buf bytes.Buffer
	if err := withNil.encode(&buf); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	var rt StateSyncTx
	if err := rt.decode(buf.Bytes()); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Expect: 2 entries (nil dropped), same order of the non-nil elements.
	if len(rt.StateSyncData) != 2 {
		t.Fatalf("expected 2 entries after round-trip, got %d", len(rt.StateSyncData))
	}
	if got, want := rt.StateSyncData[0].ID, uint64(1); got != want {
		t.Fatalf("first entry ID: got %d want %d", got, want)
	}
	if got, want := rt.StateSyncData[1].ID, uint64(2); got != want {
		t.Fatalf("second entry ID: got %d want %d", got, want)
	}
}

func TestStateSyncTx_EncodeDecode_Empty(t *testing.T) {
	var empty StateSyncTx // nil slice
	var buf bytes.Buffer

	if err := empty.encode(&buf); err != nil {
		t.Fatalf("encode(empty) failed: %v", err)
	}

	var rt StateSyncTx
	if err := rt.decode(buf.Bytes()); err != nil {
		t.Fatalf("decode(empty) failed: %v", err)
	}
	if rt.StateSyncData == nil || len(rt.StateSyncData) != 0 {
		t.Fatalf("expected empty slice after decode, got %#v", rt.StateSyncData)
	}
}

func TestStateSyncTx_Decode_Garbage(t *testing.T) {
	var rt StateSyncTx
	if err := rt.decode([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatalf("expected decode error on garbage, got nil")
	}
}

func TestStateSyncTx_Encode_DeterministicAcrossCopies(t *testing.T) {
	// Use TxData.copy() from the implementation to build a logically identical clone.
	orig := makeBase()
	clone := orig.copy().(*StateSyncTx)

	var b1, b2 bytes.Buffer
	if err := orig.encode(&b1); err != nil {
		t.Fatalf("encode(orig) failed: %v", err)
	}
	if err := clone.encode(&b2); err != nil {
		t.Fatalf("encode(clone) failed: %v", err)
	}
	if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
		t.Fatalf("determinism failed across copies: enc1=%x enc2=%x", b1.Bytes(), b2.Bytes())
	}
}
