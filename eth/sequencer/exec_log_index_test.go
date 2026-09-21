package sequencer

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// A speculative block that opens on its predecessor's parked StateDB inherits
// that StateDB's log counter, which never resets, so served receipts carried
// the previous blocks' log counts in logIndex. Numbering belongs to the block.
func TestRecordedLogsAreNumberedPerBlock(t *testing.T) {
	ex := startExecHarness(t)
	statedb, err := ex.chain.StateAt(ex.chain.CurrentBlock().Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	// Block N emitted three logs through this StateDB.
	for i := 0; i < 3; i++ {
		statedb.AddLog(&types.Log{Address: common.Address{0xaa}})
	}

	// Block N+1 opens on the same, parked StateDB; the EVM stamps its first
	// log with the carried counter.
	first := &types.Log{Address: common.Address{0xbb}}
	statedb.AddLog(first)
	if first.Index != 3 {
		t.Fatalf("StateDB stamped Index %d, want 3 from the carried counter", first.Index)
	}

	env := &blockEnv{header: &types.Header{Number: big.NewInt(2)}, statedb: statedb}
	tx := types.NewTx(&types.LegacyTx{Gas: 21_000, GasPrice: big.NewInt(1)})
	if _, _, err := env.recordAppliedTransaction(tx, &types.Receipt{Logs: []*types.Log{first}}, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if first.Index != 0 {
		t.Fatalf("served logIndex = %d, want 0 for the first log of the block", first.Index)
	}

	second := []*types.Log{{Address: common.Address{0xcc}}, {Address: common.Address{0xcc}}}
	for _, l := range second {
		statedb.AddLog(l)
	}
	if _, _, err := env.recordAppliedTransaction(tx, &types.Receipt{Logs: second}, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if second[0].Index != 1 || second[1].Index != 2 {
		t.Fatalf("served logIndex = %d,%d, want 1,2 continuing the block", second[0].Index, second[1].Index)
	}
}

// A continuation from a served prefix resumes numbering after the prefix.
func TestPrefixEnvContinuesLogNumbering(t *testing.T) {
	prefix := []*types.Receipt{{Logs: []*types.Log{{}, {}}}, {Logs: []*types.Log{{}}}}
	env := &blockEnv{header: &types.Header{}, logCount: countLogs(prefix)}

	l := &types.Log{}
	tx := types.NewTx(&types.LegacyTx{Gas: 21_000, GasPrice: big.NewInt(1)})
	if _, _, err := env.recordAppliedTransaction(tx, &types.Receipt{Logs: []*types.Log{l}}, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if l.Index != 3 {
		t.Fatalf("logIndex = %d, want 3 after a three-log prefix", l.Index)
	}
}
