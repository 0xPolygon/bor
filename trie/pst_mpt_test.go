package trie

import (
	"crypto/rand"
	mathrand "math/rand"
	"testing"
)

func mustRandBytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return b
}

func genUniqueKeys(count int) [][]byte {
	seen := make(map[string]struct{}, count*2)
	out := make([][]byte, 0, count)
	for len(out) < count {
		k := mustRandBytes(32)
		if _, ok := seen[string(k)]; ok {
			continue
		}
		seen[string(k)] = struct{}{}
		out = append(out, k)
	}
	return out
}

func genKVs(count int) []KVPair {
	keys := genUniqueKeys(count)
	kvs := make([]KVPair, 0, count)
	r := mathrand.New(mathrand.NewSource(42))
	for _, k := range keys {
		sz := 1 + r.Intn(128)
		if sz > 4096 {
			sz = 4096
		}
		val := make([]byte, sz)
		_, _ = r.Read(val)
		kvs = append(kvs, KVPair{Key: k, Value: val})
	}
	return kvs
}

func TestParallelBuild_MatchesTrieCommit_AllInserts(t *testing.T) {
	kvs := genKVs(600)

	// Build via Trie
	tr := NewEmpty(nil)
	for i := range kvs {
		if err := tr.Update(kvs[i].Key, kvs[i].Value); err != nil {
			t.Fatalf("trie update failed: %v", err)
		}
	}
	tr.Hash()
	wantRoot, _ := tr.Commit(false)

	// Build via Parallel
	haveRoot, _, err := BuildAccountTrieParallel(kvs, 16)
	if err != nil {
		t.Fatalf("parallel build failed: %v", err)
	}
	if haveRoot != wantRoot {
		t.Fatalf("root mismatch: have %x want %x", haveRoot, wantRoot)
	}

}

func TestParallelBuild_MatchesTrieCommit_InsertsDeletes(t *testing.T) {
	kvs := genKVs(700)

	tr := NewEmpty(nil)
	survivors := make([]KVPair, 0, len(kvs))
	for i := range kvs {
		if err := tr.Update(kvs[i].Key, kvs[i].Value); err != nil {
			t.Fatalf("trie update failed: %v", err)
		}
	}
	// Delete around 1/8th deterministically.
	for i, kv := range kvs {
		if i%8 == 0 {
			if err := tr.Delete(kv.Key); err != nil {
				t.Fatalf("trie delete failed: %v", err)
			}
		} else {
			survivors = append(survivors, kv)
		}
	}
	tr.Hash()
	wantRoot, _ := tr.Commit(false)

	pst := NewParallelSparseTrie()
	for i := range kvs {
		pst.Insert(kvs[i].Key, kvs[i].Value)
	}
	for i := range kvs {
		if i%8 == 0 {
			pst.Delete(kvs[i].Key)
		}
	}
	haveRoot, _, err := pst.Build(12)
	if err != nil {
		t.Fatalf("pst build failed: %v", err)
	}
	if haveRoot != wantRoot {
		t.Fatalf("root mismatch: have %x want %x", haveRoot, wantRoot)
	}

}
