package commitment

import (
	"encoding/hex"
	"math/big"
	"testing"
)

// Pinned known-answer vectors: any change to the fold, the canonical
// encodings, Domain, or FormatVersion must show up here as a deliberate
// vector update (and a FormatVersion bump).
const (
	vecSeedMainnet = "65f936615eef373e5d2c50572b6b8361521ae9111f0d960c77654eb956bb78ae"
	vecSeedAmoy    = "91055066e10d67c28b348cef4e21a7a9a986624d8f8eca68d5a78e4424631d86"
	vecOpen        = "842397ec7d66e4c1ea7110d79bc8d246fa803aaea459fc9da88eb25c394a5231"
	vecBlock       = "1930951575fd8fba6b3978984c8d297c0391c414385172a78ab7c9a79c3cf09f"
	vecSeal        = "4d48cf4e3422406e236802ca2374a45e0f6c8785a865743a906494b0bb57ca4d"
)

var (
	testOpen = OpenContext{
		Number:     77000001,
		Timestamp:  1750000000,
		ParentHash: [32]byte{0x11, 0x22, 0x33},
		GasLimit:   45000000,
		BaseFee:    big.NewInt(25000000000),
	}
	testTx1        = []byte{0xf8, 0x6b, 0x01}
	testTx2        = []byte{0x02, 0xf8, 0x71}
	testSealedHash = [32]byte{0xaa, 0xbb}
)

func hexHead(t *testing.T, s string) Head {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad vector %q: %v", s, err)
	}

	var h Head

	copy(h[:], b)

	return h
}

func TestSeed(t *testing.T) {
	tests := []struct {
		name    string
		chainID uint64
		want    string
	}{
		{"mainnet", 137, vecSeedMainnet},
		{"amoy", 80002, vecSeedAmoy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Seed(tt.chainID); got != hexHead(t, tt.want) {
				t.Errorf("Seed(%d) = %x, want %s", tt.chainID, got, tt.want)
			}
		})
	}

	if Seed(137) == Seed(80002) {
		t.Error("seeds for different chain ids collide")
	}
}

func TestBlockLifecycle(t *testing.T) {
	cOpen, err := FoldOpen(Seed(137), testOpen)
	if err != nil {
		t.Fatalf("FoldOpen: %v", err)
	}

	if cOpen != hexHead(t, vecOpen) {
		t.Errorf("C_open = %x, want %s", cOpen, vecOpen)
	}

	cBlock := FoldTxs(cOpen, [][]byte{testTx1, testTx2})
	if cBlock != hexHead(t, vecBlock) {
		t.Errorf("C_block = %x, want %s", cBlock, vecBlock)
	}

	cSeal := FoldSeal(cBlock, testSealedHash)
	if cSeal != hexHead(t, vecSeal) {
		t.Errorf("C_seal = %x, want %s", cSeal, vecSeal)
	}
}

// The chain must not depend on how transactions are batched into records:
// folding per transaction is normative.
func TestFoldTxsBatchingInvariance(t *testing.T) {
	start := Seed(137)
	txs := [][]byte{testTx1, testTx2, {0x01}, {0x02, 0x03}}

	oneBatch := FoldTxs(start, txs)

	perTx := start
	for _, tx := range txs {
		perTx = FoldTx(perTx, tx)
	}

	split := FoldTxs(FoldTxs(start, txs[:1]), txs[1:])

	if oneBatch != perTx || oneBatch != split {
		t.Errorf("batching changed the head: batch=%x perTx=%x split=%x", oneBatch, perTx, split)
	}
}

func TestTagSeparation(t *testing.T) {
	head := Seed(137)
	item := testSealedHash[:]

	tags := map[string]byte{"open": TagOpen, "tx": TagTx, "seal": TagSeal}
	seen := map[Head]string{}

	for name, tag := range tags {
		h := Fold(head, tag, item)
		if prev, ok := seen[h]; ok {
			t.Errorf("tags %s and %s collide on the same item", prev, name)
		}

		seen[h] = name
	}
}

func TestFoldOpenFieldSensitivity(t *testing.T) {
	base, err := FoldOpen(Seed(137), testOpen)
	if err != nil {
		t.Fatalf("FoldOpen: %v", err)
	}

	mutate := []struct {
		name string
		fn   func(*OpenContext)
	}{
		{"number", func(o *OpenContext) { o.Number++ }},
		{"timestamp", func(o *OpenContext) { o.Timestamp++ }},
		{"parent_hash", func(o *OpenContext) { o.ParentHash[0] ^= 1 }},
		{"gas_limit", func(o *OpenContext) { o.GasLimit++ }},
		{"base_fee", func(o *OpenContext) { o.BaseFee = big.NewInt(25000000001) }},
	}

	for _, tt := range mutate {
		t.Run(tt.name, func(t *testing.T) {
			open := testOpen
			tt.fn(&open)

			got, err := FoldOpen(Seed(137), open)
			if err != nil {
				t.Fatalf("FoldOpen: %v", err)
			}

			if got == base {
				t.Errorf("mutating %s did not change the head", tt.name)
			}
		})
	}
}

// The canonical byte layouts, restated as manually assembled preimages —
// executable spec for independent implementations.
func TestPreimageLayout(t *testing.T) {
	be64 := func(v uint64) []byte {
		return []byte{
			byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
			byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
		}
	}

	t.Run("seed", func(t *testing.T) {
		pre := []byte(Domain)
		pre = append(pre, be64(137)...)
		pre = append(pre, 0x00, 0x00, 0x00, byte(FormatVersion))

		if got, want := Seed(137), keccak256(pre); got != want {
			t.Errorf("Seed = %x, preimage spec = %x", got, want)
		}
	})

	t.Run("fold", func(t *testing.T) {
		head := Seed(137)
		item := []byte{0x01, 0x02, 0x03}

		pre := append([]byte{}, head[:]...)
		pre = append(pre, TagTx)
		pre = append(pre, item...)

		if got, want := Fold(head, TagTx, item), keccak256(pre); got != want {
			t.Errorf("Fold = %x, preimage spec = %x", got, want)
		}
	})

	t.Run("open_item", func(t *testing.T) {
		item := be64(testOpen.Number)
		item = append(item, be64(testOpen.Timestamp)...)
		item = append(item, testOpen.ParentHash[:]...)
		item = append(item, be64(testOpen.GasLimit)...)

		var fee [32]byte

		testOpen.BaseFee.FillBytes(fee[:])
		item = append(item, fee[:]...)

		got, err := FoldOpen(Seed(137), testOpen)
		if err != nil {
			t.Fatalf("FoldOpen: %v", err)
		}

		if want := Fold(Seed(137), TagOpen, item); got != want {
			t.Errorf("FoldOpen = %x, preimage spec = %x", got, want)
		}
	})

	t.Run("seal_item", func(t *testing.T) {
		head := hexHead(t, vecBlock)
		if got, want := FoldSeal(head, testSealedHash), Fold(head, TagSeal, testSealedHash[:]); got != want {
			t.Errorf("FoldSeal = %x, preimage spec = %x", got, want)
		}
	})
}

func TestHeadBytesCopies(t *testing.T) {
	head := Seed(137)
	got := head.Bytes()
	head[0] ^= 1

	if got[0] == head[0] {
		t.Error("Bytes aliases the head's storage")
	}
}

func TestSealedHash(t *testing.T) {
	// The well-known keccak256 empty-input digest — pins the hash function
	// itself (legacy Keccak, not NIST SHA-3).
	const emptyKeccak = "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"

	if got := SealedHash(nil); Head(got) != hexHead(t, emptyKeccak) {
		t.Errorf("SealedHash(nil) = %x, want %s", got, emptyKeccak)
	}
}

func TestFoldTxsEmptyInputs(t *testing.T) {
	head := Seed(137)

	// Zero transactions fold nothing — the reason an empty Record is
	// structurally invalid at the ingress: it would not advance the head.
	if FoldTxs(head, nil) != head {
		t.Error("FoldTxs with no transactions changed the head")
	}

	// An empty byte string is still one folded item and advances the head.
	if FoldTx(head, nil) == head {
		t.Error("FoldTx of an empty transaction did not advance the head")
	}
}

func TestFoldOpenInvalidBaseFee(t *testing.T) {
	tests := []struct {
		name string
		fee  *big.Int
	}{
		{"nil", nil},
		{"negative", big.NewInt(-1)},
		{"too_wide", new(big.Int).Lsh(big.NewInt(1), 256)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open := testOpen
			open.BaseFee = tt.fee

			if _, err := FoldOpen(Seed(137), open); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
