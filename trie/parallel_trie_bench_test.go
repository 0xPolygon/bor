package trie

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// generateAccounts creates deterministic addresses and RLP-encoded accounts.
func generateAccounts(size int, seed int64) (addresses [][20]byte, accounts []*types.StateAccount) {
	r := rand.New(rand.NewSource(seed))
	addresses = make([][20]byte, size)
	for i := 0; i < len(addresses); i++ {
		var a [20]byte
		r.Read(a[:])
		addresses[i] = a
	}
	accounts = make([]*types.StateAccount, len(addresses))
	for i := 0; i < len(accounts); i++ {
		nonce := uint64(r.Int63())
		root := types.EmptyRootHash
		code := crypto.Keccak256(nil)
		// random balance up to 32 bytes
		numBytes := uint32(r.Int31n(33)) // 0..32
		balanceBytes := make([]byte, numBytes)
		r.Read(balanceBytes)
		balance := new(uint256.Int).SetBytes(balanceBytes)
		accounts[i] = &types.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			Root:     root,
			CodeHash: code,
		}
	}
	return
}

// BenchmarkAccountTrie_Update benchmarks account trie updates (legacy vs parallel).
func BenchmarkAccountTrie_Update(b *testing.B) {
	for _, size := range []int{100, 300, 1000, 3000} {
		addresses, accounts := generateAccounts(size, 0)
		b.Run(fmt.Sprintf("size-%d/legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				legacy, err := NewStateTrie(StateTrieID(types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for j := 0; j < len(addresses); j++ {
					if err := legacy.UpdateAccount(common.BytesToAddress(addresses[j][:]), accounts[j], 0); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(fmt.Sprintf("size-%d/parallel", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				parallel, err := NewParallelSparseTrie(StateTrieID(types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for j := 0; j < len(addresses); j++ {
					if err := parallel.UpdateAccount(common.BytesToAddress(addresses[j][:]), accounts[j], 0); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkAccountTrie_Commit benchmarks account trie commit (legacy vs parallel).
func BenchmarkAccountTrie_Commit(b *testing.B) {
	for _, size := range []int{100, 300, 1000, 3000} {
		addresses, accounts := generateAccounts(size, 0)
		b.Run(fmt.Sprintf("size-%d/legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				legacy, err := NewStateTrie(StateTrieID(types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < len(addresses); j++ {
					if err := legacy.UpdateAccount(common.BytesToAddress(addresses[j][:]), accounts[j], 0); err != nil {
						b.Fatal(err)
					}
				}
				legacy.Hash()
				b.StartTimer()
				legacy.Commit(false)
			}
		})
		b.Run(fmt.Sprintf("size-%d/parallel", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				parallel, err := NewParallelSparseTrie(StateTrieID(types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < len(addresses); j++ {
					if err := parallel.UpdateAccount(common.BytesToAddress(addresses[j][:]), accounts[j], 0); err != nil {
						b.Fatal(err)
					}
				}
				parallel.Hash()
				b.StartTimer()
				parallel.Commit(false)
			}
		})
	}
}

// BenchmarkStorageTrie_Update benchmarks storage trie updates (legacy vs parallel).
func BenchmarkStorageTrie_Update(b *testing.B) {
	for _, size := range []int{50, 200, 500, 1000} {
		b.Run(fmt.Sprintf("size-%d/legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				legacy, err := NewStateTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for j := 0; j < size; j++ {
					var slot common.Hash
					var val common.Hash
					rand.Read(slot[:])
					rand.Read(val[:])
					if err := legacy.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(fmt.Sprintf("size-%d/parallel", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				parallel, err := NewParallelSparseTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for j := 0; j < size; j++ {
					var slot common.Hash
					var val common.Hash
					rand.Read(slot[:])
					rand.Read(val[:])
					if err := parallel.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkStorageTrie_UpdateDelete benchmarks storage trie updates with deletes (legacy vs parallel).
func BenchmarkStorageTrie_UpdateDelete(b *testing.B) {
	for _, size := range []int{50, 200, 500, 1000} {
		b.Run(fmt.Sprintf("size-%d/legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				legacy, err := NewStateTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				slots := make([]common.Hash, size)
				for j := 0; j < size; j++ {
					rand.Read(slots[j][:])
					var val common.Hash
					rand.Read(val[:])
					if err := legacy.UpdateStorage(common.BytesToAddress(addr[:]), slots[j][:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				// Delete every 17th slot (matching integration test pattern)
				for j := 0; j < size; j += 17 {
					if err := legacy.DeleteStorage(common.BytesToAddress(addr[:]), slots[j][:]); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(fmt.Sprintf("size-%d/parallel", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				parallel, err := NewParallelSparseTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				slots := make([]common.Hash, size)
				for j := 0; j < size; j++ {
					rand.Read(slots[j][:])
					var val common.Hash
					rand.Read(val[:])
					if err := parallel.UpdateStorage(common.BytesToAddress(addr[:]), slots[j][:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				// Delete every 17th slot (matching integration test pattern)
				for j := 0; j < size; j += 17 {
					if err := parallel.DeleteStorage(common.BytesToAddress(addr[:]), slots[j][:]); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkStorageTrie_Commit benchmarks storage trie commit (legacy vs parallel).
func BenchmarkStorageTrie_Commit(b *testing.B) {
	for _, size := range []int{50, 200, 500, 1000} {
		b.Run(fmt.Sprintf("size-%d/legacy", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				legacy, err := NewStateTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < size; j++ {
					var slot common.Hash
					var val common.Hash
					rand.Read(slot[:])
					rand.Read(val[:])
					if err := legacy.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
					// Occasionally delete (matching integration test pattern)
					if j%17 == 0 {
						if err := legacy.DeleteStorage(common.BytesToAddress(addr[:]), slot[:]); err != nil {
							b.Fatal(err)
						}
					}
				}
				legacy.Hash()
				b.StartTimer()
				legacy.Commit(false)
			}
		})
		b.Run(fmt.Sprintf("size-%d/parallel", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				tdb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
				var addr [20]byte
				rand.New(rand.NewSource(int64(i))).Read(addr[:])
				addrHash := crypto.Keccak256Hash(addr[:])
				parallel, err := NewParallelSparseTrie(StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < size; j++ {
					var slot common.Hash
					var val common.Hash
					rand.Read(slot[:])
					rand.Read(val[:])
					if err := parallel.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
						b.Fatal(err)
					}
					// Occasionally delete (matching integration test pattern)
					if j%17 == 0 {
						if err := parallel.DeleteStorage(common.BytesToAddress(addr[:]), slot[:]); err != nil {
							b.Fatal(err)
						}
					}
				}
				parallel.Hash()
				b.StartTimer()
				parallel.Commit(false)
			}
		})
	}
}
