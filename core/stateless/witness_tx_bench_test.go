package stateless

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

// benchNodes builds a per-transaction read set of the shape a mainnet
// transaction produces: a handful of trie nodes and a code blob or two.
func benchNodes(tx, n int) map[string][]byte {
	nodes := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		v := []byte(fmt.Sprintf("node-%d-%d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", tx, i))
		nodes[string(v[:8])] = v
	}
	return nodes
}

func newBenchWitness() *Witness {
	return &Witness{
		context: &types.Header{Number: big.NewInt(1000)},
		Headers: []*types.Header{{Number: big.NewInt(999)}},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}
}

// BenchmarkWitnessUnscoped is today's behaviour: every read lands straight in
// the witness.
func BenchmarkWitnessUnscoped(b *testing.B) {
	code := make([]byte, 4096)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w := newBenchWitness()
		for tx := 0; tx < 200; tx++ {
			w.AddState(benchNodes(tx, 30))
			w.AddCode(code)
		}
	}
}

// BenchmarkWitnessScoped is the new behaviour: each transaction stages its
// reads and commits them on success.
func BenchmarkWitnessScoped(b *testing.B) {
	code := make([]byte, 4096)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w := newBenchWitness()
		for tx := 0; tx < 200; tx++ {
			w.BeginTx()
			w.AddState(benchNodes(tx, 30))
			w.AddCode(code)
			w.CommitTx()
		}
	}
}

// BenchmarkWitnessScopedWithDrops mixes in the dropped transactions the scope
// exists for -- one in ten attempts is abandoned.
func BenchmarkWitnessScopedWithDrops(b *testing.B) {
	code := make([]byte, 4096)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w := newBenchWitness()
		for tx := 0; tx < 200; tx++ {
			w.BeginTx()
			w.AddState(benchNodes(tx, 30))
			w.AddCode(code)

			if tx%10 == 9 {
				w.DiscardTx()
			} else {
				w.CommitTx()
			}
		}
	}
}
