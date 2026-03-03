package rawdb

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
)

// benchHash returns a deterministic hash for benchmarking.
func benchHash(i int) common.Hash {
	var h common.Hash
	h[0] = byte(i >> 24)
	h[1] = byte(i >> 16)
	h[2] = byte(i >> 8)
	h[3] = byte(i)
	return h
}

// makePayload creates a random byte slice of the given size.
func makePayload(size int) []byte {
	buf := make([]byte, size)
	rand.Read(buf)
	return buf
}

// newPebbleDB creates a real on-disk Pebble database for benchmarking.
func newPebbleDB(b *testing.B) ethdb.Database {
	b.Helper()
	pdb, err := pebble.New(b.TempDir(), 256, 16, "", false)
	if err != nil {
		b.Fatalf("cannot create pebble database: %v", err)
	}
	b.Cleanup(func() { pdb.Close() })
	return NewDatabase(pdb)
}

// preloadDB fills the database with background data to simulate a realistic
// Pebble LSM tree with multiple levels and active compaction pressure.
// Writes 500 entries of the given size under non-witness keys.
func preloadDB(b *testing.B, db ethdb.Database, entrySize int) {
	b.Helper()
	payload := makePayload(entrySize)
	for i := 0; i < 500; i++ {
		// Use a different key prefix to add general DB pressure
		// without colliding with witness keys.
		key := fmt.Appendf(nil, "preload-%06d", i)
		if err := db.Put(key, payload); err != nil {
			b.Fatalf("preload write failed: %v", err)
		}
	}
}

func sizeLabel(size int) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%dMB", size/(1024*1024))
	default:
		return fmt.Sprintf("%dKB", size/1024)
	}
}

// --- Write benchmarks ---

func BenchmarkWriteWitness_DB(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			db := newPebbleDB(b)
			preloadDB(b, db, size)
			ws := NewDBWitnessStore(db)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				ws.WriteWitness(benchHash(i), payload)
			}
		})
	}
}

func BenchmarkWriteWitness_FS(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			witnessDir := b.TempDir()
			db := newPebbleDB(b)
			ws := NewFSWitnessStore(witnessDir, db)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				ws.WriteWitness(benchHash(i), payload)
			}
		})
	}
}

// --- Read benchmarks ---

func BenchmarkReadWitness_DB(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			db := newPebbleDB(b)
			preloadDB(b, db, size)
			ws := NewDBWitnessStore(db)
			hash := benchHash(0)
			ws.WriteWitness(hash, payload)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				ws.ReadWitness(hash)
			}
		})
	}
}

func BenchmarkReadWitness_FS(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			witnessDir := b.TempDir()
			db := newPebbleDB(b)
			ws := NewFSWitnessStore(witnessDir, db)
			hash := benchHash(0)
			ws.WriteWitness(hash, payload)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				ws.ReadWitness(hash)
			}
		})
	}
}

// --- Delete benchmarks ---

func BenchmarkDeleteWitness_DB(b *testing.B) {
	b.Run("delete", func(b *testing.B) {
		db := newPebbleDB(b)
		ws := NewDBWitnessStore(db)
		payload := makePayload(1024)
		for i := 0; i < b.N; i++ {
			ws.WriteWitness(benchHash(i), payload)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ws.DeleteWitness(benchHash(i))
		}
	})
}

func BenchmarkDeleteWitness_FS(b *testing.B) {
	b.Run("delete", func(b *testing.B) {
		witnessDir := b.TempDir()
		db := newPebbleDB(b)
		ws := NewFSWitnessStore(witnessDir, db)
		payload := makePayload(1024)
		for i := 0; i < b.N; i++ {
			ws.WriteWitness(benchHash(i), payload)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ws.DeleteWitness(benchHash(i))
		}
	})
}
