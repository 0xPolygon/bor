package state

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	"github.com/holiman/uint256"
)

// These benchmarks default to the path-based scheme (pathdb) since that's
// what Bor mainnet nodes run. Path-based state has fundamentally different
// read performance characteristics than hash-based:
//
//   - Storage reads go through the flat snapshot reader (direct key lookup
//     into pathdb's state cache or Pebble snapshot). NO trie traversal.
//   - Trie depth does NOT affect SLOAD cost in pathdb.
//   - The relevant cache is pathdb.Config.StateCleanSize (default 16 MB),
//     not the trie clean cache.
//   - Trie writes still require traversal to recompute the state root,
//     so SSTORE commit cost scales with dirty-set size, not trie size.
//
// For a XEN-scale contract with hundreds of millions of storage slots, the
// worst case is: the slot's flat snapshot entry is not in the state cache
// and must be fetched from Pebble's on-disk LSM tree. That's what the
// disk-backed "cold" benchmarks below measure.

type storageSetup struct {
	diskDB  ethdb.Database
	trieDB  *triedb.Database
	stateDB *CachingDB
	root    common.Hash
	addr    common.Address
	keys    []common.Hash
	tmpDir  string
}

func (s *storageSetup) cleanup() {
	if s.trieDB != nil {
		s.trieDB.Close()
	}
	if s.diskDB != nil {
		s.diskDB.Close()
	}
	if s.tmpDir != "" {
		os.RemoveAll(s.tmpDir)
	}
}

func (s *storageSetup) openFreshState(b *testing.B) *StateDB {
	b.Helper()
	state, err := New(s.root, s.stateDB)
	if err != nil {
		b.Fatal(err)
	}
	return state
}

type storageOpts struct {
	numSlots  int
	scheme    string // rawdb.PathScheme (default) or rawdb.HashScheme
	useDiskDB bool   // true = Pebble on tmpdir; false = memorydb
	batchSize int    // slots per intermediate commit (0 = single commit)

	// pathdb knobs
	trieCleanSize  int // default = 16 MB (pathdb.Defaults.TrieCleanSize)
	stateCleanSize int // default = 16 MB (pathdb.Defaults.StateCleanSize); set -1 to disable

	// hashdb knobs (ignored in path mode)
	hashCleanCacheSize int
}

func setupStorage(b *testing.B, opts storageOpts) *storageSetup {
	b.Helper()

	scheme := opts.scheme
	if scheme == "" {
		scheme = rawdb.PathScheme
	}

	var (
		kvDB   ethdb.Database
		tmpDir string
		err    error
	)

	if opts.useDiskDB {
		tmpDir, err = os.MkdirTemp("", "sload-bench-*")
		if err != nil {
			b.Fatal(err)
		}
		pdb, err := pebble.New(tmpDir, 16, 64, "", false)
		if err != nil {
			os.RemoveAll(tmpDir)
			b.Fatal(err)
		}
		kvDB = rawdb.NewDatabase(pdb)
	} else {
		kvDB = rawdb.NewMemoryDatabase()
	}

	cfg := &triedb.Config{}
	if scheme == rawdb.PathScheme {
		pcfg := *pathdb.Defaults
		pcfg.NoAsyncFlush = true // deterministic: setup returns only after flush
		if opts.trieCleanSize != 0 {
			pcfg.TrieCleanSize = opts.trieCleanSize
		}
		if opts.stateCleanSize == -1 {
			pcfg.StateCleanSize = 0
		} else if opts.stateCleanSize != 0 {
			pcfg.StateCleanSize = opts.stateCleanSize
		}
		cfg.PathDB = &pcfg
	} else {
		cfg.HashDB = &hashdb.Config{CleanCacheSize: opts.hashCleanCacheSize}
	}

	tdb := triedb.NewDatabase(kvDB, cfg)
	sdb := NewDatabase(tdb, nil)

	addr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	keys := make([]common.Hash, opts.numSlots)
	for i := range keys {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(i))
		keys[i] = crypto.Keccak256Hash(buf[:])
	}

	batchSize := opts.batchSize
	if batchSize <= 0 {
		batchSize = opts.numSlots
	}

	var root common.Hash
	parentRoot := types.EmptyRootHash

	for start := 0; start < opts.numSlots; start += batchSize {
		end := start + batchSize
		if end > opts.numSlots {
			end = opts.numSlots
		}

		state, err := New(parentRoot, sdb)
		if err != nil {
			b.Fatal(err)
		}

		if start == 0 {
			state.SetNonce(addr, 1, tracing.NonceChangeUnspecified)
			state.SetBalance(addr, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
			state.SetCode(addr, []byte{0x60, 0x00}, tracing.CodeChangeUnspecified)
		}

		for i := start; i < end; i++ {
			state.SetState(addr, keys[i], common.BigToHash(common.Big1))
		}

		root, err = state.Commit(uint64(start/batchSize+1), false, false)
		if err != nil {
			b.Fatal(err)
		}
		parentRoot = root
	}

	// Flush all diff layers to the disk layer. For pathdb this collapses
	// the layer tree onto the persistent snapshot, so subsequent reads
	// exercise the disk layer state cache + Pebble path (not in-memory
	// diff layers). For hashdb this flushes the dirty map to disk.
	if err := tdb.Commit(root, false); err != nil {
		b.Fatal(err)
	}

	return &storageSetup{
		diskDB:  kvDB,
		trieDB:  tdb,
		stateDB: sdb,
		root:    root,
		addr:    addr,
		keys:    keys,
		tmpDir:  tmpDir,
	}
}

func randomHash() common.Hash {
	var h common.Hash
	rand.Read(h[:])
	return h
}

// ============================================================================
// PART 1: Path-scheme (pathdb) benchmarks — the mainnet configuration
// ============================================================================

// BenchmarkSloadPath_NumSlots measures cold SLOAD cost vs number of slots
// in the storage "trie" on path-based state with a real Pebble database.
//
// Note: in pathdb, SLOAD does NOT traverse the trie — the slot is fetched
// flat from the state snapshot. So we expect roughly flat cost across
// slot counts, with variance coming from Pebble index depth and state
// cache pressure.
func BenchmarkSloadPath_NumSlots(b *testing.B) {
	for _, numSlots := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("slots_%d", numSlots), func(b *testing.B) {
			setup := setupStorage(b, storageOpts{
				numSlots:       numSlots,
				useDiskDB:      true,
				stateCleanSize: -1, // disable state cache: force Pebble reads
				batchSize:      200_000,
			})
			defer setup.cleanup()
			target := setup.keys[numSlots/2]

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				state := setup.openFreshState(b)
				state.GetState(setup.addr, target)
			}
		})
	}
}

// BenchmarkSloadPath_CacheState measures hot/warm/cold SLOAD on a 1M-slot
// path-based state backed by Pebble, with the state clean cache DISABLED
// so that "cold" actually goes to disk.
func BenchmarkSloadPath_CacheState(b *testing.B) {
	const numSlots = 1_000_000
	setup := setupStorage(b, storageOpts{
		numSlots:       numSlots,
		useDiskDB:      true,
		stateCleanSize: -1,
		batchSize:      200_000,
	})
	defer setup.cleanup()
	target := setup.keys[numSlots/2]

	b.Run("hot_origin_cache", func(b *testing.B) {
		// Repeated reads of the same slot hit stateObject.originStorage —
		// just a Go map lookup, never touches pathdb at all.
		state := setup.openFreshState(b)
		state.GetState(setup.addr, target)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state.GetState(setup.addr, target)
		}
	})

	b.Run("warm_state_cache", func(b *testing.B) {
		// Different keys, but reuse the same StateDB/reader. Without
		// the state clean cache, each distinct key goes through Pebble.
		state := setup.openFreshState(b)
		state.GetState(setup.addr, target)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state.GetState(setup.addr, setup.keys[i%numSlots])
		}
	})

	b.Run("cold_from_disk", func(b *testing.B) {
		// Fresh StateDB every iteration. Nothing cached, full path:
		// StateDB.GetState → stateObject → flatReader → pathdb diskLayer
		// → ReadStorageSnapshot → Pebble Get.
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state := setup.openFreshState(b)
			state.GetState(setup.addr, target)
		}
	})
}

// BenchmarkSloadPath_StateCache compares SLOAD latency on a 1M-slot
// path-based state across different state clean cache sizes. In real
// Bor nodes, this cache is sized via config — insufficient cache relative
// to hot storage working set is a real operational risk.
func BenchmarkSloadPath_StateCache(b *testing.B) {
	const numSlots = 1_000_000

	for _, cacheMB := range []int{-1, 1, 16, 128, 512} {
		name := fmt.Sprintf("cache_%dMB", cacheMB)
		if cacheMB == -1 {
			name = "cache_none"
		}

		b.Run(name, func(b *testing.B) {
			opts := storageOpts{
				numSlots:  numSlots,
				useDiskDB: true,
				batchSize: 200_000,
			}
			if cacheMB == -1 {
				opts.stateCleanSize = -1
			} else {
				opts.stateCleanSize = cacheMB * 1024 * 1024
			}

			setup := setupStorage(b, opts)
			defer setup.cleanup()
			target := setup.keys[numSlots/2]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := setup.openFreshState(b)
				state.GetState(setup.addr, target)
			}
		})
	}
}

// BenchmarkSloadPath_Burst simulates a transaction doing many unique cold
// SLOADs against a massive storage trie. Each slot is a separate Pebble
// read (state cache disabled), modelling the realistic worst case where
// the working set exceeds cache.
func BenchmarkSloadPath_Burst(b *testing.B) {
	const numSlots = 1_000_000

	for _, burstSize := range []int{1, 10, 50, 100, 500} {
		b.Run(fmt.Sprintf("burst_%d", burstSize), func(b *testing.B) {
			setup := setupStorage(b, storageOpts{
				numSlots:       numSlots,
				useDiskDB:      true,
				stateCleanSize: -1,
				batchSize:      200_000,
			})
			defer setup.cleanup()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := setup.openFreshState(b)
				for j := 0; j < burstSize; j++ {
					state.GetState(setup.addr, setup.keys[(i*burstSize+j)%numSlots])
				}
			}
		})
	}
}

// BenchmarkSloadPath_Miss measures SLOAD of a non-existent key on a
// 1M-slot path-based state. In pathdb the lookup returns empty quickly
// (single Pebble read that returns nothing), no trie traversal.
func BenchmarkSloadPath_Miss(b *testing.B) {
	for _, numSlots := range []int{1_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("slots_%d", numSlots), func(b *testing.B) {
			setup := setupStorage(b, storageOpts{
				numSlots:       numSlots,
				useDiskDB:      true,
				stateCleanSize: -1,
				batchSize:      200_000,
			})
			defer setup.cleanup()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := setup.openFreshState(b)
				state.GetState(setup.addr, randomHash())
			}
		})
	}
}

// BenchmarkSstorePath_OpType measures different SSTORE operations on a
// 1M-slot path-based state. SSTORE reads the original value (for gas
// metering) then updates the dirty map; commit cost is NOT measured here.
func BenchmarkSstorePath_OpType(b *testing.B) {
	const numSlots = 1_000_000
	setup := setupStorage(b, storageOpts{
		numSlots:       numSlots,
		useDiskDB:      true,
		stateCleanSize: -1,
		batchSize:      200_000,
	})
	defer setup.cleanup()

	b.Run("new_slot", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state := setup.openFreshState(b)
			state.SetState(setup.addr, randomHash(), common.BigToHash(common.Big1))
		}
	})

	b.Run("update_existing", func(b *testing.B) {
		target := setup.keys[numSlots/2]
		newVal := common.BigToHash(common.Big2)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state := setup.openFreshState(b)
			state.SetState(setup.addr, target, newVal)
		}
	})

	b.Run("clear_to_zero", func(b *testing.B) {
		target := setup.keys[numSlots/2]
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state := setup.openFreshState(b)
			state.SetState(setup.addr, target, common.Hash{})
		}
	})
}

// BenchmarkSloadPath_MultiContract simulates a block touching storage
// across many different contracts, each with its own large storage trie.
// Under pathdb, each contract's slots are in the same flat snapshot,
// keyed by (accountHash, slotHash) — no per-contract trie open cost.
func BenchmarkSloadPath_MultiContract(b *testing.B) {
	const slotsPerContract = 100_000

	for _, numContracts := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("contracts_%d", numContracts), func(b *testing.B) {
			tmpDir, err := os.MkdirTemp("", "sload-multi-*")
			if err != nil {
				b.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			pdb, err := pebble.New(tmpDir, 16, 64, "", false)
			if err != nil {
				b.Fatal(err)
			}
			kvDB := rawdb.NewDatabase(pdb)
			defer kvDB.Close()

			pcfg := *pathdb.Defaults
			pcfg.NoAsyncFlush = true
			pcfg.StateCleanSize = 0
			tdb := triedb.NewDatabase(kvDB, &triedb.Config{PathDB: &pcfg})
			defer tdb.Close()
			sdb := NewDatabase(tdb, nil)

			type contractInfo struct {
				addr common.Address
				keys []common.Hash
			}
			contracts := make([]contractInfo, numContracts)

			parentRoot := types.EmptyRootHash
			var root common.Hash

			for c := 0; c < numContracts; c++ {
				var addrBuf [20]byte
				binary.BigEndian.PutUint64(addrBuf[12:], uint64(c+1))
				addr := common.BytesToAddress(addrBuf[:])

				keys := make([]common.Hash, slotsPerContract)
				for i := range keys {
					var buf [8]byte
					binary.BigEndian.PutUint64(buf[:], uint64(c*slotsPerContract+i))
					keys[i] = crypto.Keccak256Hash(buf[:])
				}
				contracts[c] = contractInfo{addr: addr, keys: keys}

				for start := 0; start < slotsPerContract; start += 50_000 {
					end := start + 50_000
					if end > slotsPerContract {
						end = slotsPerContract
					}
					state, err := New(parentRoot, sdb)
					if err != nil {
						b.Fatal(err)
					}
					if start == 0 {
						state.SetNonce(addr, 1, tracing.NonceChangeUnspecified)
						state.SetBalance(addr, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
						state.SetCode(addr, []byte{0x60, 0x00}, tracing.CodeChangeUnspecified)
					}
					for i := start; i < end; i++ {
						state.SetState(addr, keys[i], common.BigToHash(common.Big1))
					}
					root, err = state.Commit(uint64(c*1000+start/50_000+1), false, false)
					if err != nil {
						b.Fatal(err)
					}
					parentRoot = root
				}
			}
			if err := tdb.Commit(root, false); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				st, err := New(root, sdb)
				if err != nil {
					b.Fatal(err)
				}
				for _, c := range contracts {
					st.GetState(c.addr, c.keys[slotsPerContract/2])
				}
			}
		})
	}
}

// ============================================================================
// PART 2: Path vs Hash head-to-head
//
// These benchmarks run the same workload on both pathdb (mainnet) and
// hashdb (legacy) to quantify the difference. Under hashdb, SLOAD requires
// trie traversal; under pathdb, it's a flat lookup.
// ============================================================================

// BenchmarkSloadSchemeComparison compares cold SLOAD latency on a 1M-slot
// storage trie under pathdb vs hashdb, both backed by Pebble with no
// clean cache. This is the apples-to-apples difference in read path.
func BenchmarkSloadSchemeComparison(b *testing.B) {
	const numSlots = 1_000_000

	cases := []struct {
		name string
		opts storageOpts
	}{
		{
			name: "pathdb_no_statecache",
			opts: storageOpts{
				numSlots:       numSlots,
				scheme:         rawdb.PathScheme,
				useDiskDB:      true,
				stateCleanSize: -1,
				batchSize:      200_000,
			},
		},
		{
			name: "pathdb_default_16MB",
			opts: storageOpts{
				numSlots:  numSlots,
				scheme:    rawdb.PathScheme,
				useDiskDB: true,
				batchSize: 200_000,
			},
		},
		{
			name: "hashdb_no_cleancache",
			opts: storageOpts{
				numSlots:           numSlots,
				scheme:             rawdb.HashScheme,
				useDiskDB:          true,
				hashCleanCacheSize: 0,
				batchSize:          200_000,
			},
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			setup := setupStorage(b, tc.opts)
			defer setup.cleanup()
			target := setup.keys[numSlots/2]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := setup.openFreshState(b)
				state.GetState(setup.addr, target)
			}
		})
	}
}
