// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

// Recorded mainnet frame + prestate, as produced by collect-ctf-frames.sh
// (callTracer top frame + prestateTracer).
type recordedFrame struct {
	Tx    string `json:"tx"`
	Block uint64 `json:"block"`
	Frame struct {
		From    common.Address `json:"from"`
		To      common.Address `json:"to"`
		Input   hexutil.Bytes  `json:"input"`
		Gas     hexutil.Uint64 `json:"gas"`
		Value   *hexutil.Big   `json:"value"`
		Output  hexutil.Bytes  `json:"output"`
		GasUsed hexutil.Uint64 `json:"gasUsed"`
		Error   *string        `json:"error"`
	} `json:"frame"`
	Prestate map[common.Address]struct {
		Balance *hexutil.Big                `json:"balance"`
		Nonce   uint64                      `json:"nonce"`
		Code    hexutil.Bytes               `json:"code"`
		Storage map[common.Hash]common.Hash `json:"storage"`
	} `json:"prestate"`
}

// loadFrames reads the recorded-frames jsonl named by AOT_FRAMES (or the
// default drop location) and skips the test if absent.
func loadFrames(t testing.TB) []recordedFrame {
	path := os.Getenv("AOT_FRAMES")
	if path == "" {
		path = "../aotexp/ctf-frames.jsonl"
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Skipf("recorded frames not available: %v (set AOT_FRAMES)", err)
	}
	defer fh.Close()
	var frames []recordedFrame
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<27)
	for sc.Scan() {
		var f recordedFrame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("bad frame line: %v", err)
		}
		frames = append(frames, f)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		t.Skip("frame file empty")
	}
	return frames
}

// frameStateDB materializes the recorded prestate into a fresh StateDB.
func frameStateDB(t testing.TB, f *recordedFrame) *state.StateDB {
	statedb, err := state.New(common.Hash{}, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	for addr, acc := range f.Prestate {
		statedb.CreateAccount(addr)
		if acc.Balance != nil {
			bal, _ := uint256.FromBig(acc.Balance.ToInt())
			statedb.SetBalance(addr, bal, tracing.BalanceChangeUnspecified)
		}
		statedb.SetNonce(addr, acc.Nonce, tracing.NonceChangeUnspecified)
		if len(acc.Code) > 0 {
			statedb.SetCode(addr, acc.Code, tracing.CodeChangeUnspecified)
		}
		for k, v := range acc.Storage {
			statedb.SetState(addr, k, v)
		}
	}
	return statedb
}

// runFrame executes one recorded frame and returns (ret, gasLeft, err).
func runFrame(t testing.TB, f *recordedFrame, aot bool) ([]byte, uint64, error) {
	statedb := frameStateDB(t, f)
	cfg := &Config{
		State:       statedb,
		GasLimit:    uint64(f.Frame.Gas),
		Origin:      f.Frame.From,
		BlockNumber: new(big.Int).SetUint64(f.Block),
		Time:        1_752_000_000,
	}
	if f.Frame.Value != nil {
		cfg.Value = f.Frame.Value.ToInt()
	}
	cfg.EVMConfig = vm.Config{EnableAOT: aot}
	return Call(f.Frame.To, f.Frame.Input, cfg)
}

// TestAOTRealFrames replays recorded mainnet frames through both paths and
// requires identical (ret, gas, err). It also reports — informationally —
// how often the replay reproduces the recorded on-chain output (replay
// context differs from mainnet in warm/cold sets, so this is not asserted).
func TestAOTRealFrames(t *testing.T) {
	frames := loadFrames(t)
	var onchainMatch, mismatches int
	for i := range frames {
		f := &frames[i]
		retI, gasI, errI := runFrame(t, f, false)
		retA, gasA, errA := runFrame(t, f, true)
		if !bytes.Equal(retI, retA) || gasI != gasA || fmt.Sprint(errI) != fmt.Sprint(errA) {
			mismatches++
			t.Errorf("frame %d tx=%s: interp(gasLeft=%d err=%v ret=%x) != aot(gasLeft=%d err=%v ret=%x)",
				i, f.Tx, gasI, errI, firstN(retI, 24), gasA, errA, firstN(retA, 24))
			if mismatches > 10 {
				t.Fatal("too many mismatches")
			}
		}
		if errI == nil && bytes.Equal(retI, f.Frame.Output) {
			onchainMatch++
		}
	}
	t.Logf("%d frames; interp==aot for all but %d; on-chain output reproduced for %d/%d (informational)",
		len(frames), mismatches, onchainMatch, len(frames))
}

// BenchmarkAOTFrameSetup measures the harness-only cost of rebuilding the
// prestate StateDB per iteration (no execution). Used to compute the
// net-of-harness speedup: (interp - setup) / (aot - setup).
func BenchmarkAOTFrameSetup(b *testing.B) {
	frames := loadFrames(b)
	for i := 0; i < b.N; i++ {
		f := &frames[i%len(frames)]
		statedb := frameStateDB(b, f)
		_ = statedb
	}
}

// BenchmarkAOTRealFrames times both paths over the recorded frames.
// The prestate StateDB is rebuilt per iteration for both modes identically,
// so the delta between the two sub-benchmarks is pure execution.
func BenchmarkAOTRealFrames(b *testing.B) {
	frames := loadFrames(b)
	for _, mode := range []struct {
		name string
		aot  bool
	}{{"interp", false}, {"aot", true}} {
		b.Run(mode.name, func(b *testing.B) {
			var gasTot uint64
			for i := 0; i < b.N; i++ {
				f := &frames[i%len(frames)]
				_, gasLeft, _ := runFrame(b, f, mode.aot)
				gasTot += uint64(f.Frame.Gas) - gasLeft
			}
			b.ReportMetric(float64(gasTot)/float64(b.N), "gas/op")
		})
	}
}
