package nativectf_test

// Oracle: run the REAL canonical CTF bytecode via the interpreter and confirm that
// every BN254 ladder block it executes produces exactly ModSqrtCandidate(x) for the
// x it consumed. A tracer is attached purely to observe (it also forces the native
// fast-path to fall back, so we observe the genuine interpreter result). This proves
// the native math equals the in-situ ladder — across the real try-and-increment loop.

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/holiman/uint256"
)

// resultDepthFromTop returns the depth-from-top of the result slot in meta.Out.
func resultDepthFromTop(meta nativectf.LadderMeta) (int, bool) {
	for ri := range meta.Out {
		if meta.Out[ri].Kind == nativectf.OutResult {
			return len(meta.Out) - 1 - ri, true
		}
	}
	return 0, false
}

type ladderObs struct {
	table   map[uint64]nativectf.LadderMeta
	pending bool
	endPC   uint64
	rDepth  int
	x       uint256.Int
	pairs   [][2]uint256.Int // {x, observedResult}
}

func (o *ladderObs) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if o.pending && pc == o.endPC {
		sd := scope.StackData()
		if len(sd) > o.rDepth {
			o.pairs = append(o.pairs, [2]uint256.Int{o.x, sd[len(sd)-1-o.rDepth]})
		}
		o.pending = false
	}
	if o.pending {
		return
	}
	if m, ok := o.table[pc]; ok {
		sd := scope.StackData()
		if len(sd) > m.BaseDepth {
			if rd, ok := resultDepthFromTop(m); ok {
				o.x = sd[len(sd)-1-m.BaseDepth]
				o.endPC, o.rDepth, o.pending = m.EndPC, rd, true
			}
		}
	}
}

func TestLadderOracle_CanonicalResultMatchesNative(t *testing.T) {
	o := newOracle(t)
	code := ctfCode(t)
	obs := &ladderObs{table: nativectf.BuildLadderTable(code)}
	if len(obs.table) == 0 {
		t.Fatal("no ladder block found in canonical CTF code")
	}
	o.cfg.EVMConfig = vm.Config{Tracer: &tracing.Hooks{OnOpcode: obs.onOpcode}}

	maxU := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	var zero [32]byte
	checked := 0
	for i := 0; i < 200; i++ {
		var ci [32]byte
		rand.Read(ci[:])
		idx, _ := rand.Int(rand.Reader, maxU)
		obs.pending = false
		before := len(obs.pairs)
		o.call(zero, ci, idx) // executes getCollectionId -> runs the ladder
		for _, p := range obs.pairs[before:] {
			x := p[0]
			want := nativectf.ModSqrtCandidate(&x)
			if !want.Eq(&p[1]) {
				t.Fatalf("ladder result mismatch: x=%x native=%x interp=%x", x.Bytes32(), want.Bytes32(), p[1].Bytes32())
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("tracer observed zero ladder executions; oracle proved nothing")
	}
	t.Logf("verified %d in-situ ladder executions == ModSqrtCandidate(x)", checked)
}
