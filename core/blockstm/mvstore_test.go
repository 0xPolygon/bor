package blockstm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func mvKey(addr byte) Key { return NewAddressKey(common.Address{addr}) }

// TestMVStore_WriteInc verifies incarnation tracking: the latest write at
// the same txIdx replaces the prior version in place.
func TestMVStore_WriteInc(t *testing.T) {
	s := NewMVStore()
	k := mvKey(1)

	s.WriteInc(k, 2, 0, uint64(10))
	s.WriteInc(k, 2, 1, uint64(20))

	v, writer, inc, found, _ := s.ReadVersionFull(k, 5)
	if !found || v.(uint64) != 20 || writer != 2 || inc != 1 {
		t.Fatalf("ReadVersionFull: got (%v, %d, %d, %v), want (20, 2, 1, true)", v, writer, inc, found)
	}
}

// TestMVStore_ReadVersionFull pins the committed-entry case (estimate=false)
// and the post-MarkEstimate case (estimate=true). The legacy provisional
// flag was removed alongside WriteProvisional.
func TestMVStore_ReadVersionFull(t *testing.T) {
	s := NewMVStore()
	k := mvKey(1)
	s.WriteInc(k, 3, 0, uint64(7))

	_, _, _, found, est := s.ReadVersionFull(k, 10)
	if !found || est {
		t.Fatalf("committed entry: found=%v est=%v; want (true,false)", found, est)
	}

	// MarkEstimate flips the flag.
	s.MarkEstimate(3, []Key{k})
	_, _, _, _, est = s.ReadVersionFull(k, 10)
	if !est {
		t.Fatalf("estimate-flagged entry: est=%v; want true", est)
	}
}

// TestMVStore_Delete removes a specific (key,txIdx) entry.
func TestMVStore_Delete(t *testing.T) {
	s := NewMVStore()
	k := mvKey(1)
	s.WriteInc(k, 2, 0, uint64(1))
	s.WriteInc(k, 4, 0, uint64(2))

	s.Delete(k, 2)

	// Reader at 10 now sees only tx 4.
	v, writer, _, found, _ := s.ReadVersionFull(k, 10)
	if !found || v.(uint64) != 2 || writer != 4 {
		t.Fatalf("after delete: got (%v, %d, _, %v), want (2, 4, true)", v, writer, found)
	}
	// Deleting a missing txIdx is a no-op.
	s.Delete(k, 99)
}

// TestMVStore_EstimateLifecycle covers MarkEstimate + CleanupEstimate.
// After MarkEstimate, ReadVersionFull reports estimate=true; CleanupEstimate
// removes entries still flagged estimate, but not ones that have since been
// re-written (i.e., WriteInc clears the estimate flag).
func TestMVStore_EstimateLifecycle(t *testing.T) {
	s := NewMVStore()
	keep := mvKey(1)
	drop := mvKey(2)
	s.WriteInc(keep, 2, 0, uint64(1))
	s.WriteInc(drop, 2, 0, uint64(2))

	s.MarkEstimate(2, []Key{keep, drop})
	if !s.IsEstimate(keep, 2) || !s.IsEstimate(drop, 2) {
		t.Fatalf("MarkEstimate: flags not set")
	}

	// Re-execute: writer overwrites `keep` at new incarnation, but not `drop`.
	s.WriteInc(keep, 2, 1, uint64(11))
	s.CleanupEstimate(2, []Key{keep, drop})

	v, writer, inc, found, _ := s.ReadVersionFull(keep, 5)
	if !found || v.(uint64) != 11 || writer != 2 || inc != 1 {
		t.Fatalf("keep after cleanup: got (%v, %d, %d, %v), want (11,2,1,true)", v, writer, inc, found)
	}
	if _, _, _, found, _ := s.ReadVersionFull(drop, 5); found {
		t.Fatalf("drop after cleanup: expected entry removed (was still estimate)")
	}
	// IsEstimate on missing entry is false.
	if s.IsEstimate(mvKey(99), 2) {
		t.Fatalf("IsEstimate on missing entry must be false")
	}
}

// TestMVStore_BloomFastPath sanity-checks that unseen keys bypass the shard
// lock — verified indirectly by returning not-found on keys never written.
func TestMVStore_BloomFastPath(t *testing.T) {
	s := NewMVStore()
	s.WriteInc(mvKey(1), 2, 0, uint64(1))

	// Different key: bloom filter should miss.
	if _, _, _, found, _ := s.ReadVersionFull(mvKey(200), 10); found {
		t.Fatalf("ReadVersionFull on unwritten key returned found=true")
	}
}

// TestBloomHashes_TypeByteContributes pins that the type byte k[53] (which
// distinguishes addressType / stateType / subpathType keys) actually
// influences at least one of the three hash dimensions. Pre-fix the type
// byte was XORed into h3's <<24 slot, which the 15-bit bloomMask discarded
// — so address-only and subpath keys with the same address produced
// identical (h1, h2, h3) triples and shared bloom slots.
func TestBloomHashes_TypeByteContributes(t *testing.T) {
	addr := common.Address{0xab, 0xcd}
	addrKey := NewAddressKey(addr)
	subKey := NewSubpathKey(addr, 0) // type byte differs; subpath byte zero matches addr-only

	a1, a2, a3 := bloomHashes(addrKey)
	b1, b2, b3 := bloomHashes(subKey)
	if a1 == b1 && a2 == b2 && a3 == b3 {
		t.Fatalf("type byte must influence at least one bloom hash; got identical (%d,%d,%d) for addressType and subpathType keys with the same address",
			a1, a2, a3)
	}
}

// TestBloomHashes_HighBytesContribute pins that the bytes shifted into
// bits 16-31 (k[2..3], k[18..19], k[10..11], k[30..31]) actually influence
// the masked output. Pre-fix the mask discarded those bits, so changing
// any of these bytes left every hash unchanged. Post-fix the XOR-fold
// mixes the upper-half bits back into the kept low 15.
func TestBloomHashes_HighBytesContribute(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Key)
	}{
		{"k[3] in h1 high slot", func(k *Key) { k[3] = 0xff }},
		{"k[19] in h2 high slot", func(k *Key) { k[19] = 0xff }},
		{"k[11] in h3 high slot", func(k *Key) { k[11] = 0xff }},
		{"k[31] in h3 high slot", func(k *Key) { k[31] = 0xff }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := NewStateKey(common.Address{0xaa}, common.Hash{0xbb})
			h1, h2, h3 := bloomHashes(base)

			mut := base
			c.mutate(&mut)
			m1, m2, m3 := bloomHashes(mut)

			if h1 == m1 && h2 == m2 && h3 == m3 {
				t.Fatalf("mutating %s left every hash unchanged: (%d,%d,%d) — upper bits discarded by mask",
					c.name, h1, h2, h3)
			}
		})
	}
}
