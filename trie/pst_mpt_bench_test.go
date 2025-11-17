package trie

import (
	"fmt"
	mathrand "math/rand"
	"runtime"
	"testing"
)

func genKVsDeterministic(count int, seed int64) []KVPair {
	r := mathrand.New(mathrand.NewSource(seed))
	seen := make(map[string]struct{}, count*2)
	kvs := make([]KVPair, 0, count)
	for len(kvs) < count {
		k := make([]byte, 32)
		_, _ = r.Read(k)
		if _, ok := seen[string(k)]; ok {
			continue
		}
		seen[string(k)] = struct{}{}
		sz := 1 + r.Intn(128)
		val := make([]byte, sz)
		_, _ = r.Read(val)
		kvs = append(kvs, KVPair{Key: k, Value: val})
	}
	return kvs
}

func BenchmarkParallelTrieBuild(b *testing.B) {
	for _, size := range []int{10000, 50000, 100000} {
		kvs := genKVsDeterministic(size, 4242)
		workers := runtime.NumCPU()
		b.Run(fmt.Sprintf("size-%d/pst-buildonly", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				pst := NewParallelSparseTrie()
				for j := range kvs {
					pst.Insert(kvs[j].Key, kvs[j].Value)
				}
				b.StartTimer()
				if _, _, err := pst.Build(workers); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("size-%d/trie-commitonly", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tr := NewEmpty(nil)
				for j := range kvs {
					if err := tr.Update(kvs[j].Key, kvs[j].Value); err != nil {
						b.Fatal(err)
					}
				}
				tr.Hash()
				b.StartTimer()
				tr.Commit(false)
			}
		})
	}
}

func BenchmarkTrie_ParallelSparseTrie(b *testing.B) {
	for _, size := range []int{10000, 50000, 100000} {
		kvs := genKVsDeterministic(size, 9898)
		b.Run(fmt.Sprintf("size-%d/pst-insert", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				pst := NewParallelSparseTrie()
				b.StartTimer()
				for j := range kvs {
					pst.Insert(kvs[j].Key, kvs[j].Value)
				}
				b.StopTimer()
			}
		})
		b.Run(fmt.Sprintf("size-%d/trie-update", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tr := NewEmpty(nil)
				b.StartTimer()
				for j := range kvs {
					if err := tr.Update(kvs[j].Key, kvs[j].Value); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkTrie_Delete(b *testing.B) {
	for _, size := range []int{10000, 50000, 100000} {
		kvs := genKVsDeterministic(size, 3434)
		delIdx := make([]int, 0, len(kvs)/8+1)
		for i := 0; i < len(kvs); i += 8 {
			delIdx = append(delIdx, i)
		}
		b.Run(fmt.Sprintf("size-%d/pst-delete", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				pst := NewParallelSparseTrie()
				for j := range kvs {
					pst.Insert(kvs[j].Key, kvs[j].Value)
				}
				b.StartTimer()
				for _, idx := range delIdx {
					pst.Delete(kvs[idx].Key)
				}
				b.StopTimer()
			}
		})
		b.Run(fmt.Sprintf("size-%d/trie-delete", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tr := NewEmpty(nil)
				// Setup: updates outside timed section
				for j := range kvs {
					if err := tr.Update(kvs[j].Key, kvs[j].Value); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				for _, idx := range delIdx {
					if err := tr.Delete(kvs[idx].Key); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
			}
		})
	}
}
