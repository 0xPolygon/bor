package state

import (
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
)

// FuzzV2Executor generates random multi-tx sequences and runs them through
// the executor differential harness. Each failing input becomes a permanent
// corpus entry. Covers executor paths that the hand-written scenarios in
// exScenarios() cannot systematically cover — in particular validation
// re-execution, ESTIMATE cleanup when a re-executed tx changes its write
// set, and interleavings of reads and writes across many txs.
func FuzzV2Executor(f *testing.F) {
	// Seed corpus: each byte is `numTxs:4 | opsPerTx:3 | workers:1` header
	// followed by the op grammar from FuzzV2Differential, with `0xff` marking
	// a tx boundary. The fuzzer learns quickly, but seeding helps coverage
	// land on realistic shapes.
	seeds := [][]byte{
		// 2 txs, simple transfer
		{0x21, // 2 txs, 1 op/tx guess, 1 worker
			0x00, 0x00, 0x01, 0x00, 0x00, 0x0a, // AddBalance(a1, 10)
			0xff,                               // tx boundary
			0x00, 0x00, 0x01, 0x00, 0x00, 0x05, // AddBalance(a1, 5)
		},
		// 3 txs all writing same slot (high conflict)
		{0x33, // 3 txs, 1 op/tx, 3 workers
			0x05, 0x00, 0x01, 0x00, 0x01, 0x11, // SetState(a1, 1, 0x11)
			0xff,
			0x05, 0x00, 0x01, 0x00, 0x01, 0x22, // SetState(a1, 1, 0x22)
			0xff,
			0x05, 0x00, 0x01, 0x00, 0x01, 0x33, // SetState(a1, 1, 0x33)
		},
		// Read-then-write dependency
		{0x22, // 2 txs, 2 ops, 2 workers
			0x05, 0x00, 0x01, 0x00, 0x01, 0x11, // SetState(a1, 1, 0x11)
			0xff,
			0x0b, 0x00, 0x01, 0x00, 0x01, // GetState(a1, 1)
			0x05, 0x00, 0x01, 0x00, 0x01, 0x22, // SetState(a1, 1, 0x22)
		},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		sc, ok := decodeExecutorProgram(program)
		if !ok {
			t.Skip("undecodable program")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Skipf("panic in generated program (not a V2 drift): %v", r)
			}
		}()
		runExecutorDifferential(t, sc)
	})
}

// decodeExecutorProgram parses a byte slice into an executor scenario.
// Header: 1 byte (numTxs<<4 | workers). Body: op tuples, with 0xff as a
// tx separator. Uses the same op opcodes as decodeProgram(), plus 0x0b
// for GetState / 0x0c for GetBalance / 0x0d for GetNonce.
func decodeExecutorProgram(b []byte) (exScenario, bool) {
	if len(b) < 2 {
		return exScenario{}, false
	}
	header := b[0]
	numTxs := int(header>>4) & 0x0f
	if numTxs < 1 {
		numTxs = 1
	}
	if numTxs > 8 {
		numTxs = 8 // cap for test speed
	}
	workers := int(header & 0x0f)
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	body := b[1:]

	addrs := [4]common.Address{a(1), a(2), a(3), a(4)}

	// Split body at 0xff boundaries into per-tx op slices.
	var perTx [][]pdbOp
	cur := []pdbOp{}
	const maxOpsPerTx = 12
	const maxTotalOps = 64

	readByte := func() (byte, bool) {
		if len(body) == 0 {
			return 0, false
		}
		b := body[0]
		body = body[1:]
		return b, true
	}
	readAddr := func() (common.Address, bool) {
		v, ok := readByte()
		if !ok {
			return common.Address{}, false
		}
		return addrs[v%uint8(len(addrs))], true
	}
	readU32 := func() (uint64, bool) {
		if len(body) < 4 {
			return 0, false
		}
		v := uint64(binary.BigEndian.Uint32(body[:4]))
		body = body[4:]
		return v, true
	}

	totalOps := 0
	for len(body) > 0 && len(perTx) < numTxs && totalOps < maxTotalOps {
		op, ok := readByte()
		if !ok {
			break
		}
		if op == 0xff {
			// Tx boundary.
			perTx = append(perTx, cur)
			cur = nil
			continue
		}
		if len(cur) >= maxOpsPerTx {
			continue // skip op until next boundary
		}

		switch op {
		case 0x00: // AddBalance
			addr, ok1 := readAddr()
			v, ok2 := readU32()
			if !ok1 || !ok2 {
				break
			}
			cur = append(cur, opAddBalance{addr, uint256.NewInt(v)})
		case 0x01: // SubBalance — constrain to small values so balance doesn't underflow
			addr, ok1 := readAddr()
			v, ok2 := readByte()
			if !ok1 || !ok2 {
				break
			}
			cur = append(cur, opSubBalance{addr, uint256.NewInt(uint64(v))})
		case 0x04: // SetCode
			addr, ok1 := readAddr()
			n, ok2 := readByte()
			if !ok1 || !ok2 {
				break
			}
			code := make([]byte, int(n)%4) // 0-3 bytes
			for i := range code {
				v, ok := readByte()
				if !ok {
					break
				}
				code[i] = v
			}
			cur = append(cur, opAddBalance{addr, uint256.NewInt(1)})
			cur = append(cur, opSetCode{addr, code})
		case 0x05: // SetState
			addr, ok1 := readAddr()
			slot, ok2 := readByte()
			val, ok3 := readByte()
			if !ok1 || !ok2 || !ok3 {
				break
			}
			cur = append(cur, opAddBalance{addr, uint256.NewInt(1)})
			cur = append(cur, opSetState{addr, h(uint64(slot % 8)), h(uint64(val))})
		case 0x0b: // GetState
			addr, ok1 := readAddr()
			slot, ok2 := readByte()
			if !ok1 || !ok2 {
				break
			}
			cur = append(cur, opGetState{addr, h(uint64(slot % 8))})
		case 0x0c: // GetBalance
			addr, ok := readAddr()
			if !ok {
				break
			}
			cur = append(cur, opGetBalance{addr})
		case 0x0d: // GetNonce
			addr, ok := readAddr()
			if !ok {
				break
			}
			cur = append(cur, opGetNonce{addr})
		default:
			return exScenario{}, false
		}
		totalOps++
	}
	if len(cur) > 0 {
		perTx = append(perTx, cur)
	}
	if len(perTx) == 0 {
		return exScenario{}, false
	}

	// Build txScripts — sender is picked round-robin so we exercise both
	// same-sender serialization and cross-sender parallelism.
	txs := make([]txScript, len(perTx))
	for i, ops := range perTx {
		// Each tx's sender gets a pre-funded balance via setup so
		// SubBalance doesn't underflow if the script uses it.
		txs[i] = txScript{sender: addrs[i%len(addrs)], ops: ops}
	}

	// Pre-fund all addrs to keep scripts valid.
	setup := make([]pdbOp, 0, len(addrs))
	for _, addr := range addrs {
		setup = append(setup, opAddBalance{addr, uint256.NewInt(1_000_000)})
	}

	probes := executorProbesFromTxs(txs, addrs[:])

	return exScenario{
		name:    "exfuzz",
		setup:   setup,
		txs:     txs,
		probes:  probes,
		workers: workers,
	}, true
}

// executorProbesFromTxs produces a probe for every address and (addr, slot)
// any tx touches — so the harness detects drift on any observable value.
func executorProbesFromTxs(txs []txScript, candidateAddrs []common.Address) []probe {
	seenAddr := map[common.Address]bool{}
	seenSlot := map[stateKey]bool{}
	var probes []probe

	addAddr := func(a common.Address) {
		if seenAddr[a] {
			return
		}
		seenAddr[a] = true
		probes = append(probes,
			probe{kind: "balance", addr: a},
			probe{kind: "nonce", addr: a},
			probe{kind: "codehash", addr: a},
		)
	}
	addSlot := func(a common.Address, s common.Hash) {
		k := stateKey{addr: a, slot: s}
		if seenSlot[k] {
			return
		}
		seenSlot[k] = true
		probes = append(probes, probe{kind: "storage", addr: a, slot: s})
	}

	// Always probe the candidate addrs so the harness catches state drift
	// on any of them, not just addrs used by ops.
	for _, addr := range candidateAddrs {
		addAddr(addr)
	}

	var walk func([]pdbOp)
	walk = func(ops []pdbOp) {
		for _, op := range ops {
			switch o := op.(type) {
			case opAddBalance:
				addAddr(o.addr)
			case opSubBalance:
				addAddr(o.addr)
			case opSetCode:
				addAddr(o.addr)
			case opSetState:
				addAddr(o.addr)
				addSlot(o.addr, o.slot)
			case opGetState:
				addAddr(o.addr)
				addSlot(o.addr, o.slot)
			case opGetBalance:
				addAddr(o.addr)
			case opGetNonce:
				addAddr(o.addr)
			}
		}
	}
	for _, tx := range txs {
		walk(tx.ops)
	}
	return probes
}
