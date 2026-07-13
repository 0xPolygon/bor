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
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
)

// ---------------------------------------------------------------------------
// Dynamic opcode-stream profiler for superinstruction (opcode fusion) sizing.
//
// Replays the recorded mainnet frames (core/vm/aotexp/*.jsonl) through the
// interpreter with an OnOpcode hook and records the executed opcode stream,
// segmented into STATICALLY-CONTIGUOUS runs: a new segment starts whenever
// the call depth changes (enter/exit of a nested frame) or the pc does not
// follow sequentially from the previous opcode (i.e. a taken JUMP/JUMPI).
// Only pairs/triples inside a segment are candidates for fusion — a
// superinstruction fuses opcodes that are adjacent in the static bytecode.
// A not-taken JUMPI stays inside its segment (a fused JUMPI_X can fall
// through into X); a taken jump breaks the segment.
//
// From the segments it computes bigram/trigram frequencies, basic-block
// statistics, greedy non-overlapping pair-coverage curves, and a dispatch-
// overhead savings model. Results go to the test log and a raw-data file.
// ---------------------------------------------------------------------------

// loadFramesFrom reads a recorded-frames jsonl at an explicit path.
func loadFramesFrom(t testing.TB, path string) []recordedFrame {
	fh, err := os.Open(path)
	if err != nil {
		t.Skipf("recorded frames not available: %v", err)
	}
	defer fh.Close()
	var frames []recordedFrame
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<27)
	for sc.Scan() {
		var f recordedFrame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("bad frame line in %s: %v", path, err)
		}
		frames = append(frames, f)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

// pushImmLen returns the immediate-data length of op (PUSH1..PUSH32).
func pushImmLen(op byte) uint64 {
	if op >= 0x60 && op <= 0x7f {
		return uint64(op) - 0x5f
	}
	return 0
}

// streamCollector segments the dynamic opcode stream into statically
// contiguous runs.
type streamCollector struct {
	segments  [][]byte
	cur       []byte
	lastDepth int
	nextPC    uint64
	active    bool
	breaks    uint64 // segment breaks due to control transfer / depth change
}

func (c *streamCollector) onOpcode(pc uint64, op byte, gas, cost uint64, _ tracing.OpContext, _ []byte, depth int, _ error) {
	if !c.active || depth != c.lastDepth || pc != c.nextPC {
		c.flush()
		if c.active {
			c.breaks++
		}
		c.active = true
	}
	c.cur = append(c.cur, op)
	c.nextPC = pc + 1 + pushImmLen(op)
	c.lastDepth = depth
}

func (c *streamCollector) flush() {
	if len(c.cur) > 0 {
		c.segments = append(c.segments, c.cur)
		c.cur = nil
	}
}

// isBlockTerminator: opcodes that end a basic block (control transfer or halt).
func isBlockTerminator(op byte) bool {
	switch vm.OpCode(op) {
	case vm.JUMP, vm.JUMPI, vm.STOP, vm.RETURN, vm.REVERT, vm.SELFDESTRUCT, vm.INVALID,
		vm.CALL, vm.CALLCODE, vm.DELEGATECALL, vm.STATICCALL, vm.CREATE, vm.CREATE2:
		return true
	}
	return false
}

func opName(op byte) string {
	s := vm.OpCode(op).String()
	if strings.Contains(s, "not defined") {
		return fmt.Sprintf("INVALID_0x%02x", op)
	}
	return s
}

func nameSeq(ops []byte) string {
	parts := make([]string, len(ops))
	for i, b := range ops {
		parts[i] = opName(b)
	}
	return strings.Join(parts, " ")
}

// TestOpcodeFusionProfile replays all frames from both fixtures and reports
// the superinstruction opportunity analysis. Run with:
//
//	go test -run TestOpcodeFusionProfile -v -timeout 30m ./core/vm/runtime/
//
// Set FUSION_OUT to override the raw-data output path.
func TestOpcodeFusionProfile(t *testing.T) {
	fixtures := []struct {
		name string
		path string
	}{
		{"conditional", "../aotexp/conditional-frames.jsonl"},
		{"ctf", "../aotexp/ctf-frames.jsonl"},
	}

	col := &streamCollector{}
	tracer := &tracing.Hooks{OnOpcode: col.onOpcode}

	perFixtureOps := map[string]uint64{}
	var totalFrames int
	for _, fx := range fixtures {
		frames := loadFramesFrom(t, fx.path)
		before := uint64(0)
		for _, s := range col.segments {
			before += uint64(len(s))
		}
		before += uint64(len(col.cur))
		for i := range frames {
			f := &frames[i]
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
			cfg.EVMConfig = vm.Config{Tracer: tracer}
			_, _, _ = Call(f.Frame.To, f.Frame.Input, cfg)
			// End of frame: force a segment break so streams never span frames.
			col.flush()
			col.active = false
		}
		after := uint64(0)
		for _, s := range col.segments {
			after += uint64(len(s))
		}
		perFixtureOps[fx.name] = after - before
		totalFrames += len(frames)
	}
	col.flush()

	// ---- totals ----
	var totalOps uint64
	for _, s := range col.segments {
		totalOps += uint64(len(s))
	}
	if totalOps == 0 {
		t.Skip("no opcodes recorded")
	}

	// ---- bigrams / trigrams ----
	var bigrams [65536]uint64
	trigrams := map[uint32]uint64{}
	for _, s := range col.segments {
		for i := 0; i+1 < len(s); i++ {
			bigrams[uint32(s[i])<<8|uint32(s[i+1])]++
		}
		for i := 0; i+2 < len(s); i++ {
			trigrams[uint32(s[i])<<16|uint32(s[i+1])<<8|uint32(s[i+2])]++
		}
	}
	type ngram struct {
		key   uint32
		count uint64
	}
	var bigList []ngram
	for k, c := range bigrams {
		if c > 0 {
			bigList = append(bigList, ngram{uint32(k), c})
		}
	}
	sort.Slice(bigList, func(i, j int) bool { return bigList[i].count > bigList[j].count })
	var triList []ngram
	for k, c := range trigrams {
		triList = append(triList, ngram{k, c})
	}
	sort.Slice(triList, func(i, j int) bool { return triList[i].count > triList[j].count })

	// ---- basic blocks ----
	type blockStat struct {
		count uint64
		ops   []byte
	}
	blocks := map[string]*blockStat{}
	var blockLens []int
	var totalBlockInstances uint64
	emitBlock := func(ops []byte) {
		if len(ops) == 0 {
			return
		}
		k := string(ops)
		st := blocks[k]
		if st == nil {
			st = &blockStat{ops: append([]byte(nil), ops...)}
			blocks[k] = st
		}
		st.count++
		blockLens = append(blockLens, len(ops))
		totalBlockInstances++
	}
	for _, s := range col.segments {
		start := 0
		for i := 0; i < len(s); i++ {
			if vm.OpCode(s[i]) == vm.JUMPDEST && i > start {
				emitBlock(s[start:i])
				start = i
			}
			if isBlockTerminator(s[i]) {
				emitBlock(s[start : i+1])
				start = i + 1
			}
		}
		if start < len(s) {
			emitBlock(s[start:])
		}
	}
	sort.Ints(blockLens)
	pct := func(p float64) int {
		if len(blockLens) == 0 {
			return 0
		}
		idx := int(p * float64(len(blockLens)-1))
		return blockLens[idx]
	}
	var lenSum uint64
	for _, l := range blockLens {
		lenSum += uint64(l)
	}
	meanLen := float64(lenSum) / float64(len(blockLens))

	type rankedBlock struct {
		st *blockStat
	}
	var blockList []rankedBlock
	for _, st := range blocks {
		blockList = append(blockList, rankedBlock{st})
	}
	sort.Slice(blockList, func(i, j int) bool {
		return blockList[i].st.count > blockList[j].st.count
	})
	var top20BlockOps uint64
	for i := 0; i < 20 && i < len(blockList); i++ {
		st := blockList[i].st
		top20BlockOps += st.count * uint64(len(st.ops))
	}

	// ---- greedy non-overlapping pair coverage ----
	coverAt := func(k int) (matches, covered uint64) {
		set := map[uint32]bool{}
		for i := 0; i < k && i < len(bigList); i++ {
			set[bigList[i].key] = true
		}
		for _, s := range col.segments {
			for i := 0; i+1 < len(s); {
				if set[uint32(s[i])<<8|uint32(s[i+1])] {
					matches++
					covered += 2
					i += 2
				} else {
					i++
				}
			}
		}
		return
	}
	coverageKs := []int{8, 16, 32, 64}
	type covRes struct {
		k                int
		matches, covered uint64
	}
	var covs []covRes
	for _, k := range coverageKs {
		m, c := coverAt(k)
		covs = append(covs, covRes{k, m, c})
	}

	// ---- dispatch-overhead savings model ----
	// Dispatch overhead ~= 45% of EVM time, spread uniformly over totalOps
	// dispatch rounds. A fused pair match removes 1 round; a whole-block
	// superinstruction removes len-1 rounds per block instance.
	const dispatchShare = 0.45
	savePairs := func(matches uint64) float64 {
		return dispatchShare * float64(matches) / float64(totalOps)
	}
	blockRemoved := totalOps - totalBlockInstances
	saveBlocks := dispatchShare * float64(blockRemoved) / float64(totalOps)

	// ---- log summary ----
	t.Logf("frames replayed: %d (conditional=%d ops, ctf=%d ops)", totalFrames, perFixtureOps["conditional"], perFixtureOps["ctf"])
	t.Logf("total dynamic opcodes: %d in %d statically-contiguous segments (%d control-transfer breaks)", totalOps, len(col.segments), col.breaks)
	t.Logf("top 20 pairs:")
	for i := 0; i < 20 && i < len(bigList); i++ {
		n := bigList[i]
		t.Logf("  %2d. %-28s %10d  (%.2f%% of dynamic ops as pair-starts)", i+1,
			nameSeq([]byte{byte(n.key >> 8), byte(n.key)}), n.count, 100*float64(2*n.count)/float64(totalOps))
	}
	t.Logf("top 20 triples:")
	for i := 0; i < 20 && i < len(triList); i++ {
		n := triList[i]
		t.Logf("  %2d. %-40s %10d  (%.2f%%)", i+1,
			nameSeq([]byte{byte(n.key >> 16), byte(n.key >> 8), byte(n.key)}), n.count, 100*float64(3*n.count)/float64(totalOps))
	}
	t.Logf("basic blocks: %d dynamic instances, %d unique; len p50=%d p90=%d mean=%.2f",
		totalBlockInstances, len(blocks), pct(0.50), pct(0.90), meanLen)
	t.Logf("top-20 unique blocks cover %d ops = %.2f%% of dynamic opcodes", top20BlockOps, 100*float64(top20BlockOps)/float64(totalOps))
	for i := 0; i < 10 && i < len(blockList); i++ {
		st := blockList[i].st
		t.Logf("  block %2d: count=%d len=%d  [%s]", i+1, st.count, len(st.ops), nameSeq(st.ops))
	}
	for _, c := range covs {
		t.Logf("greedy coverage top-%2d pairs: matches=%d covered=%d (%.2f%% of dynamic ops)", c.k, c.matches, c.covered, 100*float64(c.covered)/float64(totalOps))
	}
	for _, c := range covs {
		if c.k == 16 || c.k == 64 {
			t.Logf("estimated EVM-time savings, top-%d pairs: %.2f%%", c.k, 100*savePairs(c.matches))
		}
	}
	t.Logf("estimated EVM-time savings, whole-basic-block dispatch: %.2f%% (removes %d of %d dispatch rounds)", 100*saveBlocks, blockRemoved, totalOps)

	// ---- raw data file ----
	outPath := os.Getenv("FUSION_OUT")
	if outPath == "" {
		outPath = "../../../fusion-profile-raw.txt"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Dynamic opcode fusion profile — raw data\n")
	fmt.Fprintf(&sb, "frames=%d total_ops=%d segments=%d breaks=%d\n", totalFrames, totalOps, len(col.segments), col.breaks)
	fmt.Fprintf(&sb, "per_fixture_ops conditional=%d ctf=%d\n\n", perFixtureOps["conditional"], perFixtureOps["ctf"])
	fmt.Fprintf(&sb, "## top 100 pairs (count, %% of dynamic ops as 2*count/total)\n")
	for i := 0; i < 100 && i < len(bigList); i++ {
		n := bigList[i]
		fmt.Fprintf(&sb, "%3d %10d %6.3f%% %s\n", i+1, n.count, 100*float64(2*n.count)/float64(totalOps),
			nameSeq([]byte{byte(n.key >> 8), byte(n.key)}))
	}
	fmt.Fprintf(&sb, "\n## top 100 triples\n")
	for i := 0; i < 100 && i < len(triList); i++ {
		n := triList[i]
		fmt.Fprintf(&sb, "%3d %10d %6.3f%% %s\n", i+1, n.count, 100*float64(3*n.count)/float64(totalOps),
			nameSeq([]byte{byte(n.key >> 16), byte(n.key >> 8), byte(n.key)}))
	}
	fmt.Fprintf(&sb, "\n## top 50 basic blocks (count, len, ops)\n")
	for i := 0; i < 50 && i < len(blockList); i++ {
		st := blockList[i].st
		fmt.Fprintf(&sb, "%3d count=%8d len=%3d ops=%.2f%% [%s]\n", i+1, st.count, len(st.ops),
			100*float64(st.count*uint64(len(st.ops)))/float64(totalOps), nameSeq(st.ops))
	}
	fmt.Fprintf(&sb, "\n## coverage curve (greedy non-overlapping)\n")
	for _, c := range covs {
		fmt.Fprintf(&sb, "top-%d matches=%d covered=%d coverage=%.4f savings=%.4f\n",
			c.k, c.matches, c.covered, float64(c.covered)/float64(totalOps), savePairs(c.matches))
	}
	fmt.Fprintf(&sb, "block model: instances=%d unique=%d p50=%d p90=%d mean=%.2f removed=%d savings=%.4f\n",
		totalBlockInstances, len(blocks), pct(0.50), pct(0.90), meanLen, blockRemoved, saveBlocks)
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write raw data: %v", err)
	}
	t.Logf("raw data written to %s", outPath)
}
