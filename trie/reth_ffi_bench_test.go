package trie_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// generateTestData creates test data for benchmarks
func generateTestData(numAccounts int) ([][20]byte, [][]byte) {
	r := rand.New(rand.NewSource(42))
	addresses := make([][20]byte, numAccounts)
	blobs := make([][]byte, numAccounts)

	for i := 0; i < numAccounts; i++ {
		var addr [20]byte
		r.Read(addr[:])
		addresses[i] = addr

		nonce := uint64(r.Int63())
		balance := uint256.NewInt(uint64(r.Int63()))
		data, _ := rlp.EncodeToBytes(&types.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			Root:     types.EmptyRootHash,
			CodeHash: crypto.Keccak256(nil),
		})
		blobs[i] = data
	}
	return addresses, blobs
}

// BenchmarkNativeMPT benchmarks the native MPT implementation
// Includes a series of updates and deletes to simulate realistic usage
func BenchmarkNativeMPT(b *testing.B) {
	const numAccounts = 20000
	const numUpdates = 5000 // Number of accounts to update
	const numDeletes = 2000 // Number of accounts to delete
	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)

	// Generate test data once
	addresses, blobs := generateTestData(numAccounts)

	b.ReportAllocs()
	var totalTime time.Duration
	var updateTime, deleteTime, hashTime time.Duration

	for i := 0; i < b.N; i++ {
		start := time.Now()
		legacy, err := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
		if err != nil {
			b.Fatal(err)
		}

		// Initial updates
		updateStart := time.Now()
		for j := 0; j < numAccounts; j++ {
			addr := common.BytesToAddress(addresses[j][:])
			acc := new(types.StateAccount)
			if err := rlp.DecodeBytes(blobs[j], acc); err != nil {
				b.Fatal(err)
			}
			if err := legacy.UpdateAccount(addr, acc, 0); err != nil {
				b.Fatal(err)
			}
		}
		updateTime += time.Since(updateStart)

		// Additional updates (modify existing accounts)
		updateStart = time.Now()
		for j := 0; j < numUpdates; j++ {
			idx := j % numAccounts
			addr := common.BytesToAddress(addresses[idx][:])
			acc := new(types.StateAccount)
			rlp.DecodeBytes(blobs[idx], acc)
			acc.Nonce++ // Modify account
			if err := legacy.UpdateAccount(addr, acc, 0); err != nil {
				b.Fatal(err)
			}
		}
		updateTime += time.Since(updateStart)

		// Deletes
		deleteStart := time.Now()
		for j := 0; j < numDeletes; j++ {
			idx := j % numAccounts
			addr := common.BytesToAddress(addresses[idx][:])
			if err := legacy.DeleteAccount(addr); err != nil {
				b.Fatal(err)
			}
		}
		deleteTime += time.Since(deleteStart)

		// Hash computation
		hashStart := time.Now()
		legacy.Hash()
		legacy.Commit(false)
		hashTime += time.Since(hashStart)

		totalTime += time.Since(start)
	}

	avgTotal := totalTime / time.Duration(b.N)
	avgUpdate := updateTime / time.Duration(b.N)
	avgDelete := deleteTime / time.Duration(b.N)
	avgHash := hashTime / time.Duration(b.N)

	fmt.Printf("\n=== Native-MPT Results ===\n")
	fmt.Printf("Iterations: %d\n", b.N)
	fmt.Printf("Total time: %v (avg: %v, %.2f ms)\n", totalTime, avgTotal, float64(avgTotal.Nanoseconds())/1e6)
	fmt.Printf("  - Updates: %v (avg: %v, %.2f ms)\n", updateTime, avgUpdate, float64(avgUpdate.Nanoseconds())/1e6)
	fmt.Printf("  - Deletes: %v (avg: %v, %.2f ms)\n", deleteTime, avgDelete, float64(avgDelete.Nanoseconds())/1e6)
	fmt.Printf("  - Hash: %v (avg: %v, %.2f ms)\n", hashTime, avgHash, float64(avgHash.Nanoseconds())/1e6)
	fmt.Printf("==========================\n\n")
}

// BenchmarkRethPST_FFI benchmarks Reth PST via FFI
// Includes a series of updates and deletes to simulate realistic usage
func BenchmarkRethPST_FFI(b *testing.B) {
	const numAccounts = 20000
	const numUpdates = 5000 // Number of accounts to update
	const numDeletes = 2000 // Number of accounts to delete

	// Generate test data once
	addresses, blobs := generateTestData(numAccounts)

	b.ReportAllocs()
	var totalTime time.Duration
	var updateTime, deleteTime, hashTime time.Duration

	for i := 0; i < b.N; i++ {
		start := time.Now()
		rethPST, err := trie.NewRethPST()
		if err != nil {
			b.Fatal(err)
		}

		// Initial updates
		updateStart := time.Now()
		for j := 0; j < numAccounts; j++ {
			addr := common.BytesToAddress(addresses[j][:])
			hk := crypto.Keccak256(addr.Bytes())
			acc := new(types.StateAccount)
			if err := rlp.DecodeBytes(blobs[j], acc); err != nil {
				b.Fatal(err)
			}
			encAcc, err := rlp.EncodeToBytes(acc)
			if err != nil {
				b.Fatal(err)
			}
			if err := rethPST.UpdateLeaf(hk, encAcc); err != nil {
				b.Fatal(err)
			}
		}
		updateTime += time.Since(updateStart)

		// Additional updates (modify existing accounts)
		updateStart = time.Now()
		for j := 0; j < numUpdates; j++ {
			idx := j % numAccounts
			addr := common.BytesToAddress(addresses[idx][:])
			hk := crypto.Keccak256(addr.Bytes())
			acc := new(types.StateAccount)
			rlp.DecodeBytes(blobs[idx], acc)
			acc.Nonce++ // Modify account
			encAcc, _ := rlp.EncodeToBytes(acc)
			if err := rethPST.UpdateLeaf(hk, encAcc); err != nil {
				b.Fatal(err)
			}
		}
		updateTime += time.Since(updateStart)

		// Deletes (update with empty value)
		deleteStart := time.Now()
		for j := 0; j < numDeletes; j++ {
			idx := j % numAccounts
			addr := common.BytesToAddress(addresses[idx][:])
			hk := crypto.Keccak256(addr.Bytes())
			// Delete by updating with empty value
			if err := rethPST.UpdateLeaf(hk, []byte{}); err != nil {
				b.Fatal(err)
			}
		}
		deleteTime += time.Since(deleteStart)

		// Hash computation
		hashStart := time.Now()
		root, rootErr := rethPST.Root()
		if rootErr != nil {
			b.Fatal(rootErr)
		}
		_ = root
		hashTime += time.Since(hashStart)

		rethPST.Free()
		totalTime += time.Since(start)
	}

	avgTotal := totalTime / time.Duration(b.N)
	avgUpdate := updateTime / time.Duration(b.N)
	avgDelete := deleteTime / time.Duration(b.N)
	avgHash := hashTime / time.Duration(b.N)

	fmt.Printf("\n=== Reth-PST-FFI Results ===\n")
	fmt.Printf("Iterations: %d\n", b.N)
	fmt.Printf("Total time: %v (avg: %v, %.2f ms)\n", totalTime, avgTotal, float64(avgTotal.Nanoseconds())/1e6)
	fmt.Printf("  - Updates: %v (avg: %v, %.2f ms)\n", updateTime, avgUpdate, float64(avgUpdate.Nanoseconds())/1e6)
	fmt.Printf("  - Deletes: %v (avg: %v, %.2f ms)\n", deleteTime, avgDelete, float64(avgDelete.Nanoseconds())/1e6)
	fmt.Printf("  - Hash: %v (avg: %v, %.2f ms)\n", hashTime, avgHash, float64(avgHash.Nanoseconds())/1e6)
	fmt.Printf("============================\n\n")
}

// BenchmarkRethFFI_vs_NativeMPT compares Reth PST (via FFI) with native MPT
// This is kept for backward compatibility - runs both benchmarks together
func BenchmarkRethFFI_vs_NativeMPT(b *testing.B) {
	const numAccounts = 20000
	const numUpdates = 5000 // Number of accounts to update
	const numDeletes = 2000 // Number of accounts to delete
	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)

	// Generate test data once
	addresses, blobs := generateTestData(numAccounts)

	// Benchmark Native MPT (Legacy)
	b.Run("Native-MPT", func(b *testing.B) {
		b.ReportAllocs()
		var totalTime time.Duration
		var updateTime, deleteTime, hashTime time.Duration

		for i := 0; i < b.N; i++ {
			start := time.Now()
			legacy, err := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
			if err != nil {
				b.Fatal(err)
			}

			// Initial updates
			updateStart := time.Now()
			for j := 0; j < numAccounts; j++ {
				addr := common.BytesToAddress(addresses[j][:])
				acc := new(types.StateAccount)
				if err := rlp.DecodeBytes(blobs[j], acc); err != nil {
					b.Fatal(err)
				}
				if err := legacy.UpdateAccount(addr, acc, 0); err != nil {
					b.Fatal(err)
				}
			}
			updateTime += time.Since(updateStart)

			// Additional updates (modify existing accounts)
			updateStart = time.Now()
			for j := 0; j < numUpdates; j++ {
				idx := j % numAccounts
				addr := common.BytesToAddress(addresses[idx][:])
				acc := new(types.StateAccount)
				rlp.DecodeBytes(blobs[idx], acc)
				acc.Nonce++ // Modify account
				if err := legacy.UpdateAccount(addr, acc, 0); err != nil {
					b.Fatal(err)
				}
			}
			updateTime += time.Since(updateStart)

			// Deletes
			deleteStart := time.Now()
			for j := 0; j < numDeletes; j++ {
				idx := j % numAccounts
				addr := common.BytesToAddress(addresses[idx][:])
				if err := legacy.DeleteAccount(addr); err != nil {
					b.Fatal(err)
				}
			}
			deleteTime += time.Since(deleteStart)

			// Hash computation
			hashStart := time.Now()
			legacy.Hash()
			legacy.Commit(false)
			hashTime += time.Since(hashStart)

			totalTime += time.Since(start)
		}

		avgTotal := totalTime / time.Duration(b.N)
		avgUpdate := updateTime / time.Duration(b.N)
		avgDelete := deleteTime / time.Duration(b.N)
		avgHash := hashTime / time.Duration(b.N)

		fmt.Printf("\n=== Native-MPT Results ===\n")
		fmt.Printf("Iterations: %d\n", b.N)
		fmt.Printf("Total time: %v (avg: %v, %.2f ms)\n", totalTime, avgTotal, float64(avgTotal.Nanoseconds())/1e6)
		fmt.Printf("  - Updates: %v (avg: %v, %.2f ms)\n", updateTime, avgUpdate, float64(avgUpdate.Nanoseconds())/1e6)
		fmt.Printf("  - Deletes: %v (avg: %v, %.2f ms)\n", deleteTime, avgDelete, float64(avgDelete.Nanoseconds())/1e6)
		fmt.Printf("  - Hash: %v (avg: %v, %.2f ms)\n", hashTime, avgHash, float64(avgHash.Nanoseconds())/1e6)
		fmt.Printf("==========================\n\n")
	})

	// Benchmark Reth PST via FFI
	b.Run("Reth-PST-FFI", func(b *testing.B) {
		b.ReportAllocs()
		var totalTime time.Duration
		var updateTime, deleteTime, hashTime time.Duration

		for i := 0; i < b.N; i++ {
			start := time.Now()
			rethPST, err := trie.NewRethPST()
			if err != nil {
				b.Fatal(err)
			}

			// Initial updates
			updateStart := time.Now()
			for j := 0; j < numAccounts; j++ {
				addr := common.BytesToAddress(addresses[j][:])
				hk := crypto.Keccak256(addr.Bytes())
				acc := new(types.StateAccount)
				if err := rlp.DecodeBytes(blobs[j], acc); err != nil {
					b.Fatal(err)
				}
				encAcc, err := rlp.EncodeToBytes(acc)
				if err != nil {
					b.Fatal(err)
				}
				if err := rethPST.UpdateLeaf(hk, encAcc); err != nil {
					b.Fatal(err)
				}
			}
			updateTime += time.Since(updateStart)

			// Additional updates (modify existing accounts)
			updateStart = time.Now()
			for j := 0; j < numUpdates; j++ {
				idx := j % numAccounts
				addr := common.BytesToAddress(addresses[idx][:])
				hk := crypto.Keccak256(addr.Bytes())
				acc := new(types.StateAccount)
				rlp.DecodeBytes(blobs[idx], acc)
				acc.Nonce++ // Modify account
				encAcc, _ := rlp.EncodeToBytes(acc)
				if err := rethPST.UpdateLeaf(hk, encAcc); err != nil {
					b.Fatal(err)
				}
			}
			updateTime += time.Since(updateStart)

			// Deletes (update with empty value)
			deleteStart := time.Now()
			for j := 0; j < numDeletes; j++ {
				idx := j % numAccounts
				addr := common.BytesToAddress(addresses[idx][:])
				hk := crypto.Keccak256(addr.Bytes())
				// Delete by updating with empty value
				if err := rethPST.UpdateLeaf(hk, []byte{}); err != nil {
					b.Fatal(err)
				}
			}
			deleteTime += time.Since(deleteStart)

			// Hash computation
			hashStart := time.Now()
			root, rootErr := rethPST.Root()
			if rootErr != nil {
				b.Fatal(rootErr)
			}
			_ = root
			hashTime += time.Since(hashStart)

			rethPST.Free()
			totalTime += time.Since(start)
		}

		avgTotal := totalTime / time.Duration(b.N)
		avgUpdate := updateTime / time.Duration(b.N)
		avgDelete := deleteTime / time.Duration(b.N)
		avgHash := hashTime / time.Duration(b.N)

		fmt.Printf("\n=== Reth-PST-FFI Results ===\n")
		fmt.Printf("Iterations: %d\n", b.N)
		fmt.Printf("Total time: %v (avg: %v, %.2f ms)\n", totalTime, avgTotal, float64(avgTotal.Nanoseconds())/1e6)
		fmt.Printf("  - Updates: %v (avg: %v, %.2f ms)\n", updateTime, avgUpdate, float64(avgUpdate.Nanoseconds())/1e6)
		fmt.Printf("  - Deletes: %v (avg: %v, %.2f ms)\n", deleteTime, avgDelete, float64(avgDelete.Nanoseconds())/1e6)
		fmt.Printf("  - Hash: %v (avg: %v, %.2f ms)\n", hashTime, avgHash, float64(avgHash.Nanoseconds())/1e6)
		fmt.Printf("============================\n\n")
	})
}
