package rawdb

import (
	"crypto/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
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

func BenchmarkWriteWitness_DB(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			db := NewMemoryDatabase()
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
			dir := b.TempDir()
			db := NewMemoryDatabase()
			ws := NewFSWitnessStore(dir, db)
			b.ResetTimer()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				ws.WriteWitness(benchHash(i), payload)
			}
		})
	}
}

func BenchmarkReadWitness_DB(b *testing.B) {
	for _, size := range []int{100 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		payload := makePayload(size)
		b.Run(sizeLabel(size), func(b *testing.B) {
			db := NewMemoryDatabase()
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
			dir := b.TempDir()
			db := NewMemoryDatabase()
			ws := NewFSWitnessStore(dir, db)
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

func BenchmarkDeleteWitness_DB(b *testing.B) {
	b.Run("delete", func(b *testing.B) {
		db := NewMemoryDatabase()
		ws := NewDBWitnessStore(db)
		payload := makePayload(1024)
		for i := 0; i < b.N; i++ {
			hash := benchHash(i)
			ws.WriteWitness(hash, payload)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ws.DeleteWitness(benchHash(i))
		}
	})
}

func BenchmarkDeleteWitness_FS(b *testing.B) {
	b.Run("delete", func(b *testing.B) {
		dir := b.TempDir()
		db := NewMemoryDatabase()
		ws := NewFSWitnessStore(dir, db)
		payload := makePayload(1024)
		for i := 0; i < b.N; i++ {
			hash := benchHash(i)
			ws.WriteWitness(hash, payload)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ws.DeleteWitness(benchHash(i))
		}
	})
}

func sizeLabel(size int) string {
	switch {
	case size >= 1024*1024:
		return string(rune('0'+size/(1024*1024))) + "MB"
	default:
		return string(rune('0'+size/1024)) + "KB"
	}
}
