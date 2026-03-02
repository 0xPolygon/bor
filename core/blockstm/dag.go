package blockstm

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"time"

	"github.com/heimdalr/dag"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	hasReadDepCallCounter = metrics.NewRegisteredCounter("blockstm/hasreaddep/calls", nil)
	readsMapSizeHist      = metrics.NewRegisteredHistogram("blockstm/hasreaddep/reads/size", nil, metrics.NewExpDecaySample(1028, 0.015))
	dagBuildTimer         = metrics.NewRegisteredTimer("blockstm/dag/build", nil)
	errNonSequentialIndex = errors.New("non-sequential transaction index")
)

const defaultBitsetWidth = 512

type DAG struct {
	*dag.DAG
}

type TxDep struct {
	Index         int
	ReadList      []ReadDescriptor
	FullWriteList [][]WriteDescriptor
}

// HasReadDep checks if there are any read dependencies between two transactions.
// Performance optimization: Based on production metrics showing ~3M calls with median
// size of 15 and 95th percentile of 94, we avoid map allocation for small inputs.
// This optimization leverages the fact that for small lists, linear search is faster
// than map construction due to avoiding allocation overhead and better cache locality.
func HasReadDep(txFrom TxnOutput, txTo TxnInput) bool {
	hasReadDepCallCounter.Inc(1)

	// Cutoff determined by benchmarking: below this size, nested loops outperform map lookup
	// due to avoiding allocation overhead. With median=15, this captures >50% of calls.
	const smallCutoff = 512
	if len(txTo) <= smallCutoff {
		// For small inputs, use direct comparison (O(n*m) but with better constants)
		for _, rd := range txFrom {
			for _, v := range txTo {
				if rd.Path == v.Path {
					return true
				}
			}
		}
		return false
	}

	// For larger inputs, use map for O(n+m) complexity
	// Using struct{} instead of bool saves memory (0 bytes vs 1 byte per entry)
	// since we only need set membership, not associated values
	reads := make(map[Key]struct{})
	for _, v := range txTo {
		reads[v.Path] = struct{}{}
	}

	readsMapSizeHist.Update(int64(len(reads)))

	for _, rd := range txFrom {
		if _, ok := reads[rd.Path]; ok {
			return true
		}
	}

	return false
}

func BuildDAG(deps TxnInputOutput) (d DAG) {
	d = DAG{dag.NewDAG()}
	ids := make(map[int]string)

	for i := len(deps.inputs) - 1; i > 0; i-- {
		txTo := deps.inputs[i]

		var txToId string

		if _, ok := ids[i]; ok {
			txToId = ids[i]
		} else {
			txToId, _ = d.AddVertex(i)
			ids[i] = txToId
		}

		for j := i - 1; j >= 0; j-- {
			txFrom := deps.allOutputs[j]

			if HasReadDep(txFrom, txTo) {
				var txFromId string
				if _, ok := ids[j]; ok {
					txFromId = ids[j]
				} else {
					txFromId, _ = d.AddVertex(j)
					ids[j] = txFromId
				}

				err := d.AddEdge(txFromId, txToId)
				if err != nil {
					log.Warn("Failed to add edge", "from", txFromId, "to", txToId, "err", err)
				}
			}
		}
	}

	return
}

func depsHelper(dependencies map[int]map[int]bool, txFrom TxnOutput, txTo TxnInput, i int, j int) map[int]map[int]bool {
	if HasReadDep(txFrom, txTo) {
		dependencies[i][j] = true

		for k := range dependencies[i] {
			_, foundDep := dependencies[j][k]

			if foundDep {
				delete(dependencies[i], k)
			}
		}
	}

	return dependencies
}

func UpdateDeps(deps map[int]map[int]bool, t TxDep) map[int]map[int]bool {
	txTo := t.ReadList

	deps[t.Index] = map[int]bool{}

	for j := 0; j <= t.Index-1; j++ {
		txFrom := t.FullWriteList[j]

		deps = depsHelper(deps, txFrom, txTo, t.Index, j)
	}

	return deps
}

func GetDep(deps TxnInputOutput) map[int]map[int]bool {
	newDependencies := map[int]map[int]bool{}

	for i := 1; i < len(deps.inputs); i++ {
		txTo := deps.inputs[i]

		newDependencies[i] = map[int]bool{}

		for j := 0; j <= i-1; j++ {
			txFrom := deps.allOutputs[j]

			newDependencies = depsHelper(newDependencies, txFrom, txTo, i, j)
		}
	}

	return newDependencies
}

// Find the longest execution path in the DAG
func (d DAG) LongestPath(stats map[int]ExecutionStat) ([]int, uint64) {
	prev := make(map[int]int, len(d.GetVertices()))

	for i := 0; i < len(d.GetVertices()); i++ {
		prev[i] = -1
	}

	pathWeights := make(map[int]uint64, len(d.GetVertices()))

	maxPath := 0
	maxPathWeight := uint64(0)

	idxToId := make(map[int]string, len(d.GetVertices()))

	for k, i := range d.GetVertices() {
		idxToId[i.(int)] = k
	}

	for i := 0; i < len(idxToId); i++ {
		parents, _ := d.GetParents(idxToId[i])

		if len(parents) > 0 {
			for _, p := range parents {
				weight := pathWeights[p.(int)] + stats[i].End - stats[i].Start
				if weight > pathWeights[i] {
					pathWeights[i] = weight
					prev[i] = p.(int)
				}
			}
		} else {
			pathWeights[i] = stats[i].End - stats[i].Start
		}

		if pathWeights[i] > maxPathWeight {
			maxPath = i
			maxPathWeight = pathWeights[i]
		}
	}

	path := make([]int, 0)
	for i := maxPath; i != -1; i = prev[i] {
		path = append(path, i)
	}

	// Reverse the path so the transactions are in the ascending order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path, maxPathWeight
}

func (d DAG) Report(stats map[int]ExecutionStat, out func(string)) {
	longestPath, weight := d.LongestPath(stats)

	serialWeight := uint64(0)

	for i := 0; i < len(d.GetVertices()); i++ {
		serialWeight += stats[i].End - stats[i].Start
	}

	makeStrs := func(ints []int) (ret []string) {
		for _, v := range ints {
			ret = append(ret, fmt.Sprint(v))
		}

		return
	}

	out("Longest execution path:")
	out(fmt.Sprintf("(%v) %v", len(longestPath), strings.Join(makeStrs(longestPath), "->")))

	out(fmt.Sprintf("Longest path ideal execution time: %v of %v (serial total), %v%%", time.Duration(weight),
		time.Duration(serialWeight), fmt.Sprintf("%.1f", float64(weight)*100.0/float64(serialWeight))))
}

// TxBitset is a word-parallel bitset for tracking transaction dependencies.
// All bitsets operated on together must have the same len(words).
type TxBitset struct {
	words []uint64
}

func newTxBitset(numTx int) TxBitset {
	n := (numTx + 63) / 64
	return TxBitset{words: make([]uint64, n)}
}

func (b *TxBitset) Set(i int) {
	b.words[i/64] |= 1 << (uint(i) % 64)
}

func (b *TxBitset) Test(i int) bool {
	return b.words[i/64]&(1<<(uint(i)%64)) != 0
}

func (b *TxBitset) Or(other *TxBitset) {
	for w := range b.words {
		b.words[w] |= other.words[w]
	}
}

func (b *TxBitset) AndNot(other *TxBitset) {
	for w := range b.words {
		b.words[w] &^= other.words[w]
	}
}

func (b *TxBitset) ForEach(fn func(int)) {
	for w, word := range b.words {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			fn(w*64 + bit)
			word &= word - 1
		}
	}
}

func (b *TxBitset) ToSlice() []int {
	var result []int

	b.ForEach(func(i int) {
		result = append(result, i)
	})

	return result
}

// TxReadWriteSet holds a single transaction's read and write sets for dependency tracking.
type TxReadWriteSet struct {
	Index     int
	ReadList  []ReadDescriptor
	WriteList []WriteDescriptor
}

// DepsBuilder builds a transitive-reduced dependency DAG incrementally.
// Uses an inverted index (key → latest writer) for O(R) dependency lookups per tx,
// and bitsets for O(N/64) transitive reduction via word-parallel set operations.
// Transactions must be added in sequential order (0, 1, 2, ...).
type DepsBuilder struct {
	lastWriter map[Key]int   // inverted index: state key → latest tx that wrote it
	directDeps []TxBitset    // per-tx direct dependencies (after transitive reduction)
	reachable  []TxBitset    // per-tx transitive closure of all dependencies
	width      int           // bitset width in transactions (grows dynamically)
	numTx      int           // count of transactions added
	err        error         // error
	elapsed    time.Duration // time spent building the DAG
}

func NewDepsBuilder() *DepsBuilder {
	return &DepsBuilder{
		lastWriter: make(map[Key]int, 256),
		width:      defaultBitsetWidth,
		directDeps: make([]TxBitset, 0, defaultBitsetWidth),
		reachable:  make([]TxBitset, 0, defaultBitsetWidth),
	}
}

// ensureCapacity doubles the bitset width until it can hold the needed index,
// and widens all existing bitsets to match.
func (db *DepsBuilder) ensureCapacity(needed int) {
	newWidth := db.width
	for newWidth <= needed {
		newWidth *= 2
	}

	oldWords := (db.width + 63) / 64
	newWords := (newWidth + 63) / 64

	if newWords > oldWords {
		extra := newWords - oldWords
		for i := range db.directDeps {
			db.directDeps[i].words = append(db.directDeps[i].words, make([]uint64, extra)...)
			db.reachable[i].words = append(db.reachable[i].words, make([]uint64, extra)...)
		}
	}

	db.width = newWidth
}

// AddTransaction records a transaction's read/write sets and computes its
// reduced dependency set. Must be called with sequential indices (0, 1, 2, ...).
func (db *DepsBuilder) AddTransaction(index int, readList []ReadDescriptor, writeList []WriteDescriptor) error {
	start := time.Now()
	defer func() { db.elapsed += time.Since(start) }()

	if db.err != nil {
		return db.err
	}

	// Also rejects negative indices: numTx starts at 0 and only increments.
	if index != db.numTx {
		db.err = fmt.Errorf("%w: got %d, expected %d", errNonSequentialIndex, index, db.numTx)
		return db.err
	}

	if index >= db.width {
		db.ensureCapacity(index)
	}

	db.directDeps = append(db.directDeps, newTxBitset(db.width))
	db.reachable = append(db.reachable, newTxBitset(db.width))
	db.numTx = index + 1

	// Only the latest writer per key matters; earlier writers are already covered by transitivity.
	for _, rd := range readList {
		if writer, ok := db.lastWriter[rd.Path]; ok && writer < index {
			db.directDeps[index].Set(writer)
		}
	}

	// reachable[j] is complete for all j < index.
	db.directDeps[index].ForEach(func(j int) {
		db.reachable[index].Or(&db.reachable[j])
	})

	// Remove edges already reachable via another path, leaving the minimal direct dependency set.
	db.directDeps[index].AndNot(&db.reachable[index])

	// Update reachability to include the remaining direct deps
	db.reachable[index].Or(&db.directDeps[index])

	// Update inverted index
	for _, wd := range writeList {
		db.lastWriter[wd.Path] = index
	}

	return nil
}

// GetDeps returns the reduced dependency graph as a map for backward compatibility
// with the existing serialization path. Returns nil if the builder encountered an error.
func (db *DepsBuilder) GetDeps() map[int]map[int]bool {
	start := time.Now()
	defer func() { dagBuildTimer.Update(db.elapsed + time.Since(start)) }()

	if db.err != nil {
		return nil
	}

	result := make(map[int]map[int]bool, db.numTx)

	for i := 0; i < db.numTx; i++ {
		inner := make(map[int]bool)
		db.directDeps[i].ForEach(func(j int) {
			inner[j] = true
		})
		result[i] = inner
	}

	return result
}
