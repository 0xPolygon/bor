package blockstm

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBitset_SetAndTest(t *testing.T) {
	b := newTxBitset(256)

	b.Set(0)
	b.Set(63)
	b.Set(64)
	b.Set(127)
	b.Set(255)

	assert.True(t, b.Test(0))
	assert.True(t, b.Test(63))
	assert.True(t, b.Test(64))
	assert.True(t, b.Test(127))
	assert.True(t, b.Test(255))

	assert.False(t, b.Test(1))
	assert.False(t, b.Test(62))
	assert.False(t, b.Test(128))
}

func TestBitset_Or(t *testing.T) {
	a := newTxBitset(128)
	b := newTxBitset(128)

	a.Set(0)
	a.Set(10)
	b.Set(10)
	b.Set(20)

	a.Or(&b)

	assert.True(t, a.Test(0))
	assert.True(t, a.Test(10))
	assert.True(t, a.Test(20))
	assert.False(t, a.Test(5))
}

func TestBitset_AndNot(t *testing.T) {
	a := newTxBitset(128)
	b := newTxBitset(128)

	a.Set(0)
	a.Set(10)
	a.Set(20)
	b.Set(10)

	a.AndNot(&b)

	assert.True(t, a.Test(0))
	assert.False(t, a.Test(10))
	assert.True(t, a.Test(20))
}

func TestBitset_ForEach(t *testing.T) {
	b := newTxBitset(256)
	expected := []int{3, 64, 100, 200}

	for _, v := range expected {
		b.Set(v)
	}

	var got []int
	b.ForEach(func(i int) {
		got = append(got, i)
	})

	assert.Equal(t, expected, got)
}

func TestBitset_ToSlice(t *testing.T) {
	b := newTxBitset(128)
	b.Set(5)
	b.Set(63)
	b.Set(64)
	b.Set(100)

	assert.Equal(t, []int{5, 63, 64, 100}, b.ToSlice())
}

func makeKey(addr int, slot int) Key {
	return NewStateKey(
		common.BigToAddress(big.NewInt(int64(addr))),
		common.BigToHash(big.NewInt(int64(slot))),
	)
}

func TestDepsBuilder_NoDeps(t *testing.T) {
	db := NewDepsBuilder()

	db.AddTransaction(0,
		[]ReadDescriptor{{Path: makeKey(0, 0)}},
		[]WriteDescriptor{{Path: makeKey(0, 1)}},
	)
	db.AddTransaction(1,
		[]ReadDescriptor{{Path: makeKey(1, 0)}},
		[]WriteDescriptor{{Path: makeKey(1, 1)}},
	)
	db.AddTransaction(2,
		[]ReadDescriptor{{Path: makeKey(2, 0)}},
		[]WriteDescriptor{{Path: makeKey(2, 1)}},
	)

	deps := db.GetDeps()
	assert.Empty(t, deps[0])
	assert.Empty(t, deps[1])
	assert.Empty(t, deps[2])
}

func TestDepsBuilder_LinearChain(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)
	keyB := makeKey(0, 1)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(1, []ReadDescriptor{{Path: keyA}}, []WriteDescriptor{{Path: keyB}})
	db.AddTransaction(2, []ReadDescriptor{{Path: keyB}}, nil)

	deps := db.GetDeps()
	assert.Equal(t, map[int]bool{0: true}, deps[1])
	assert.Equal(t, map[int]bool{1: true}, deps[2])
}

func TestDepsBuilder_TransitiveReduction(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)
	keyB := makeKey(0, 1)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(1, []ReadDescriptor{{Path: keyA}}, []WriteDescriptor{{Path: keyB}})
	db.AddTransaction(2, []ReadDescriptor{{Path: keyA}, {Path: keyB}}, nil)

	deps := db.GetDeps()
	assert.Equal(t, map[int]bool{1: true}, deps[2])
}

func TestDepsBuilder_DiamondDeps(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)
	keyB := makeKey(0, 1)
	keyC := makeKey(1, 0)
	keyD := makeKey(1, 1)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}, {Path: keyB}})
	db.AddTransaction(1, []ReadDescriptor{{Path: keyA}}, []WriteDescriptor{{Path: keyC}})
	db.AddTransaction(2, []ReadDescriptor{{Path: keyB}}, []WriteDescriptor{{Path: keyD}})
	db.AddTransaction(3, []ReadDescriptor{{Path: keyC}, {Path: keyD}}, nil)

	deps := db.GetDeps()
	assert.Equal(t, map[int]bool{0: true}, deps[1])
	assert.Equal(t, map[int]bool{0: true}, deps[2])
	assert.Equal(t, map[int]bool{1: true, 2: true}, deps[3])
}

func TestDepsBuilder_LatestWriterWins(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(1, nil, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(2, []ReadDescriptor{{Path: keyA}}, nil)

	deps := db.GetDeps()
	assert.Empty(t, deps[0])
	assert.Empty(t, deps[1])
	assert.Equal(t, map[int]bool{1: true}, deps[2])
}

func TestDepsBuilder_ReadAndWriteSameKey(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(1, []ReadDescriptor{{Path: keyA}}, []WriteDescriptor{{Path: keyA}})
	db.AddTransaction(2, []ReadDescriptor{{Path: keyA}}, nil)

	deps := db.GetDeps()
	assert.Equal(t, map[int]bool{0: true}, deps[1])
	assert.Equal(t, map[int]bool{1: true}, deps[2])
}

func TestDepsBuilder_Empty(t *testing.T) {
	db := NewDepsBuilder()
	deps := db.GetDeps()
	assert.Empty(t, deps)
}

func TestDepsBuilder_SingleTx(t *testing.T) {
	db := NewDepsBuilder()
	db.AddTransaction(0,
		[]ReadDescriptor{{Path: makeKey(0, 0)}},
		[]WriteDescriptor{{Path: makeKey(0, 1)}},
	)

	deps := db.GetDeps()
	assert.Len(t, deps, 1)
	assert.Empty(t, deps[0])
}

func TestDepsBuilder_NilReadWriteSets(t *testing.T) {
	db := NewDepsBuilder()
	db.AddTransaction(0, nil, nil)
	db.AddTransaction(1, nil, nil)

	deps := db.GetDeps()
	assert.Empty(t, deps[0])
	assert.Empty(t, deps[1])
}

func TestDepsBuilder_DynamicGrow(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	// Exceed the default bitset width (512) to force a grow
	n := defaultBitsetWidth + 100
	for i := 0; i < n; i++ {
		db.AddTransaction(i, nil, []WriteDescriptor{{Path: keyA}})
	}
	// Last tx reads the key, should depend on tx n-2 (the latest writer before it)
	db.AddTransaction(n, []ReadDescriptor{{Path: keyA}}, nil)

	deps := db.GetDeps()
	assert.Equal(t, map[int]bool{n - 1: true}, deps[n])
}

func TestDepsBuilder_AllReadSameKey(t *testing.T) {
	// Tx0 writes A, then txs 1..9 all read A.
	// Each tx i>0 depends on tx 0; transitive reduction keeps only the
	// direct edge since there's no chain between the readers.
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	for i := 1; i < 10; i++ {
		db.AddTransaction(i, []ReadDescriptor{{Path: keyA}}, nil)
	}

	deps := db.GetDeps()
	for i := 1; i < 10; i++ {
		assert.Equal(t, map[int]bool{0: true}, deps[i], "tx %d should depend only on tx 0", i)
	}
}

func TestDepsBuilder_WriteChain(t *testing.T) {
	// Each tx writes A, next tx reads A. Forms a linear chain: 0→1→2→...
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	n := 20
	db.AddTransaction(0, nil, []WriteDescriptor{{Path: keyA}})
	for i := 1; i < n; i++ {
		db.AddTransaction(i, []ReadDescriptor{{Path: keyA}}, []WriteDescriptor{{Path: keyA}})
	}

	deps := db.GetDeps()
	for i := 1; i < n; i++ {
		assert.Equal(t, map[int]bool{i - 1: true}, deps[i],
			"tx %d should depend only on tx %d (latest writer)", i, i-1)
	}
}

func TestDepsBuilder_NonSequentialReturnsError(t *testing.T) {
	db := NewDepsBuilder()
	require.NoError(t, db.AddTransaction(0, nil, nil))

	err := db.AddTransaction(5, nil, nil) // skip indices 1-4
	require.ErrorIs(t, err, errNonSequentialIndex)

	// Subsequent calls should return the same error
	require.ErrorIs(t, db.AddTransaction(1, nil, nil), errNonSequentialIndex)

	// GetDeps should return nil on a failed builder
	assert.Nil(t, db.GetDeps())
}

func TestDepsBuilder_IndexAtCapacityBoundary(t *testing.T) {
	db := NewDepsBuilder()
	keyA := makeKey(0, 0)

	for i := 0; i < 513; i++ {
		readList := []ReadDescriptor{}
		writeList := []WriteDescriptor{{Path: keyA}}
		if i > 0 {
			readList = []ReadDescriptor{{Path: keyA}}
		}
		require.NoError(t, db.AddTransaction(i, readList, writeList))
	}

	deps := db.GetDeps()
	require.NotNil(t, deps)
	assert.Len(t, deps, 513)
	assert.Equal(t, map[int]bool{511: true}, deps[512])
}

func TestDepsBuilder_EmptyBlock(t *testing.T) {
	db := NewDepsBuilder()
	deps := db.GetDeps()
	assert.NotNil(t, deps)
	assert.Empty(t, deps)
}

func TestBitset_EmptyBitset(t *testing.T) {
	b := newTxBitset(64)

	assert.False(t, b.Test(0))
	assert.False(t, b.Test(63))
	assert.Nil(t, b.ToSlice())

	count := 0
	b.ForEach(func(int) { count++ })
	assert.Equal(t, 0, count)
}

func TestBitset_SingleWord(t *testing.T) {
	b := newTxBitset(64)
	b.Set(0)
	b.Set(31)
	b.Set(63)

	assert.Equal(t, []int{0, 31, 63}, b.ToSlice())
}

func TestDepsBuilder_CorrectDAG(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for trial := 0; trial < 20; trial++ {
		numTx := 10 + rng.Intn(50)
		numKeys := 5 + rng.Intn(20)

		keys := make([]Key, numKeys)
		for i := range keys {
			keys[i] = makeKey(rng.Intn(10), i)
		}

		type txSets struct {
			reads  []ReadDescriptor
			writes []WriteDescriptor
		}

		txs := make([]txSets, numTx)
		for i := range txs {
			seen := make(map[Key]bool)
			for j := 0; j < rng.Intn(8); j++ {
				k := keys[rng.Intn(numKeys)]
				if !seen[k] {
					txs[i].reads = append(txs[i].reads, ReadDescriptor{Path: k})
					seen[k] = true
				}
			}

			seen = make(map[Key]bool)
			for j := 0; j < 1+rng.Intn(5); j++ {
				k := keys[rng.Intn(numKeys)]
				if !seen[k] {
					txs[i].writes = append(txs[i].writes, WriteDescriptor{Path: k})
					seen[k] = true
				}
			}
		}

		db := NewDepsBuilder()
		for i := 0; i < numTx; i++ {
			db.AddTransaction(i, txs[i].reads, txs[i].writes)
		}
		newDeps := db.GetDeps()

		// Compute raw latest-writer deps without transitive reduction
		latestWriter := make(map[Key]int)
		rawDeps := make([]map[int]bool, numTx)

		for i := 0; i < numTx; i++ {
			rawDeps[i] = make(map[int]bool)
			for _, rd := range txs[i].reads {
				if writer, ok := latestWriter[rd.Path]; ok && writer < i {
					rawDeps[i][writer] = true
				}
			}
			for _, wd := range txs[i].writes {
				latestWriter[wd.Path] = i
			}
		}

		for i := 0; i < numTx; i++ {
			for j := range newDeps[i] {
				require.True(t, j < i, "trial=%d tx=%d: dep %d violates acyclicity", trial, i, j)
				require.True(t, rawDeps[i][j], "trial=%d tx=%d: dep %d not a raw dep", trial, i, j)
			}
		}

		// Verify every raw dep is transitively reachable
		reachable := make([]map[int]bool, numTx)
		for i := 0; i < numTx; i++ {
			reachable[i] = make(map[int]bool)
			for j := range newDeps[i] {
				reachable[i][j] = true
				for k := range reachable[j] {
					reachable[i][k] = true
				}
			}
		}

		for i := 0; i < numTx; i++ {
			for j := range rawDeps[i] {
				require.True(t, reachable[i][j],
					"trial=%d tx=%d: raw dep %d not reachable", trial, i, j)
			}
		}
	}
}

func benchSetup(numTx, numKeys, readsPerTx, writesPerTx int) ([][]ReadDescriptor, [][]WriteDescriptor) {
	rng := rand.New(rand.NewSource(12345))

	keys := make([]Key, numKeys)
	for i := range keys {
		keys[i] = makeKey(rng.Intn(100), i)
	}

	reads := make([][]ReadDescriptor, numTx)
	writes := make([][]WriteDescriptor, numTx)

	for i := 0; i < numTx; i++ {
		seen := make(map[Key]bool)
		for j := 0; j < readsPerTx; j++ {
			k := keys[rng.Intn(numKeys)]
			if !seen[k] {
				reads[i] = append(reads[i], ReadDescriptor{Path: k})
				seen[k] = true
			}
		}

		seen = make(map[Key]bool)
		for j := 0; j < writesPerTx; j++ {
			k := keys[rng.Intn(numKeys)]
			if !seen[k] {
				writes[i] = append(writes[i], WriteDescriptor{Path: k})
				seen[k] = true
			}
		}
	}

	return reads, writes
}

func updateDepsAll(reads [][]ReadDescriptor, writes [][]WriteDescriptor) map[int]map[int]bool {
	deps := map[int]map[int]bool{}
	fullWriteList := make([][]WriteDescriptor, 0, len(reads))

	for i := 0; i < len(reads); i++ {
		fullWriteList = append(fullWriteList, writes[i])
		deps = UpdateDeps(deps, TxDep{
			Index:         i,
			ReadList:      reads[i],
			FullWriteList: fullWriteList,
		})
	}

	return deps
}

func depsBuilderAll(reads [][]ReadDescriptor, writes [][]WriteDescriptor) map[int]map[int]bool {
	db := NewDepsBuilder()

	for i := 0; i < len(reads); i++ {
		db.AddTransaction(i, reads[i], writes[i])
	}

	return db.GetDeps()
}

// TestDepsBuilder_EquivalenceWithUpdateDeps checks that DepsBuilder and UpdateDeps agree.
// They diverge only on blind overwrites (a tx writes a key it never read); DepsBuilder tracks
// only the latest writer while UpdateDeps retains all writers until transitive reduction catches them.
// DepsBuilder output is always a subset of UpdateDeps output.
func TestDepsBuilder_EquivalenceWithUpdateDeps(t *testing.T) {
	t.Run("linear_read_write_chain", func(t *testing.T) {
		keyA, keyB, keyC := makeKey(0, 0), makeKey(0, 1), makeKey(0, 2)

		reads := [][]ReadDescriptor{
			nil,
			{{Path: keyA}},
			{{Path: keyB}},
			{{Path: keyC}},
		}
		writes := [][]WriteDescriptor{
			{{Path: keyA}},
			{{Path: keyB}},
			{{Path: keyC}},
			nil,
		}

		assert.Equal(t, updateDepsAll(reads, writes), depsBuilderAll(reads, writes))
	})

	t.Run("diamond_dependency", func(t *testing.T) {
		// Tx0→{Tx1,Tx2}→Tx3 fan-out/fan-in topology.
		keyA, keyB, keyC := makeKey(0, 0), makeKey(0, 1), makeKey(0, 2)

		reads := [][]ReadDescriptor{
			nil,
			{{Path: keyA}},
			{{Path: keyA}},
			{{Path: keyB}, {Path: keyC}},
		}
		writes := [][]WriteDescriptor{
			{{Path: keyA}},
			{{Path: keyB}},
			{{Path: keyC}},
			nil,
		}

		assert.Equal(t, updateDepsAll(reads, writes), depsBuilderAll(reads, writes))
	})

	t.Run("writer_reads_before_writing", func(t *testing.T) {
		// Common balance-update pattern: Tx1 reads K then writes K, creating Tx1→Tx0.
		// Tx2 reads K — Tx0 is transitively pruned in both algorithms.
		keyK := makeKey(0, 0)

		reads := [][]ReadDescriptor{
			nil,
			{{Path: keyK}},
			{{Path: keyK}},
		}
		writes := [][]WriteDescriptor{
			{{Path: keyK}},
			{{Path: keyK}},
			nil,
		}

		got := depsBuilderAll(reads, writes)
		assert.Equal(t, updateDepsAll(reads, writes), got)
		assert.Equal(t, map[int]bool{1: true}, got[2])
	})

	t.Run("divergence_blind_overwrite", func(t *testing.T) {
		// Tx1 blindly overwrites K without reading it; Tx2 reads K.
		// UpdateDeps: no Tx1→Tx0 dep exists to trigger transitive reduction, so deps[2]={0,1}.
		// DepsBuilder: lastWriter[K]=1, so deps[2]={1} — Tx0's write is overwritten and irrelevant.
		keyK := makeKey(0, 0)

		reads := [][]ReadDescriptor{
			nil,
			nil, // blind overwrite
			{{Path: keyK}},
		}
		writes := [][]WriteDescriptor{
			{{Path: keyK}},
			{{Path: keyK}},
			nil,
		}

		old := updateDepsAll(reads, writes)
		new := depsBuilderAll(reads, writes)

		assert.Equal(t, map[int]bool{0: true, 1: true}, old[2])
		assert.Equal(t, map[int]bool{1: true}, new[2])

		// DepsBuilder is always a subset of UpdateDeps.
		for tx, deps := range new {
			for dep := range deps {
				assert.True(t, old[tx][dep], "DepsBuilder dep %d→%d missing from UpdateDeps", tx, dep)
			}
		}
	})

	t.Run("randomised_equivalence", func(t *testing.T) {
		// 50 random transactions each reading before writing (no blind overwrites).
		// Both algorithms must agree on every transaction.
		rng := rand.New(rand.NewSource(42))
		numTx := 50
		keys := make([]Key, 10)
		for i := range keys {
			keys[i] = makeKey(0, i)
		}

		reads := make([][]ReadDescriptor, numTx)
		writes := make([][]WriteDescriptor, numTx)

		for i := 0; i < numTx; i++ {
			k := keys[rng.Intn(len(keys))]
			reads[i] = []ReadDescriptor{{Path: k}}
			writes[i] = []WriteDescriptor{{Path: k}}
		}

		assert.Equal(t, updateDepsAll(reads, writes), depsBuilderAll(reads, writes))
	})
}

func BenchmarkUpdateDeps(b *testing.B) {
	for _, numTx := range []int{100, 500} {
		reads, writes := benchSetup(numTx, 50, 15, 5)

		b.Run(fmt.Sprintf("N=%d", numTx), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for iter := 0; iter < b.N; iter++ {
				deps := map[int]map[int]bool{}
				fullWriteList := make([][]WriteDescriptor, 0, numTx)

				for i := 0; i < numTx; i++ {
					fullWriteList = append(fullWriteList, writes[i])
					deps = UpdateDeps(deps, TxDep{
						Index:         i,
						ReadList:      reads[i],
						FullWriteList: fullWriteList,
					})
				}
			}
		})
	}
}

func BenchmarkDepsBuilder(b *testing.B) {
	for _, numTx := range []int{100, 500} {
		reads, writes := benchSetup(numTx, 50, 15, 5)

		b.Run(fmt.Sprintf("N=%d", numTx), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for iter := 0; iter < b.N; iter++ {
				db := NewDepsBuilder()

				for i := 0; i < numTx; i++ {
					db.AddTransaction(i, reads[i], writes[i])
				}

				db.GetDeps()
			}
		})
	}
}
