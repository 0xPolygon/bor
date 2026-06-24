package nativectf

import (
	"github.com/holiman/uint256"
)

// OutKind classifies an output stack slot produced by a substituted ladder block.
type OutKind uint8

const (
	OutEntry  OutKind = iota // copy the saved entry-stack value at Depth
	OutResult                // the ModSqrtCandidate result
	OutConst                 // a literal constant pushed inside the block
)

// OutSlot describes one slot of the block's output stack (bottom-to-top).
type OutSlot struct {
	Kind  OutKind
	Depth int            // for OutEntry: entry-stack depth (0 = top at block entry)
	Const *uint256.Int   // for OutConst
}

// LadderMeta is the substitution contract for one recognized jump-free ladder
// block, computed by AnalyzeLadder via abstract interpretation.
type LadderMeta struct {
	EndPC     uint64    // PC of the block terminator (JUMP/JUMPI); interpreter resumes here
	GasStatic uint64    // exact static gas of the block's opcodes (valid when Pure)
	Pure      bool      // no memory/storage/log/call/create/copy/keccak ops
	BaseDepth int       // entry-stack depth of x (input to ModSqrtCandidate)
	In        int       // number of top stack items the block consumes/replaces
	Out       []OutSlot // new top items, bottom-to-top (len = output top height)
}

// BuildLadderTable scans a contract's code and returns a map from each
// recognized ladder block's start (JUMPDEST) PC to its substitution contract.
// Most contracts contain no ladder, so the result is usually empty. Callers
// should pre-filter with a cheap bytes.Contains before invoking this on every
// contract (see table.go / the per-codehash cache in Task 4).
func BuildLadderTable(code []byte) map[uint64]LadderMeta {
	var table map[uint64]LadderMeta
	for pc := 0; pc < len(code); pc++ {
		if code[pc] != 0x5b { // JUMPDEST
			continue
		}
		if m, ok := AnalyzeLadder(code, uint64(pc)); ok {
			if table == nil {
				table = make(map[uint64]LadderMeta)
			}
			table[uint64(pc)] = m
		}
	}
	return table
}

// minLadderMULMODs is the structural gate: the BN254 getCollectionId ladder is a
// single jump-free block with ~323 MULMODs. No ordinary contract has a jump-free
// run with this many MULMODs, so this alone is a strong, cheap recognizer.
// Task 3 adds the universal-body fingerprint on top.
const minLadderMULMODs = 300

// EVM constant gas for the opcodes a pure ladder may contain. Any opcode not in
// this set (other than the block terminators) makes the block unrecognizable
// (we cannot prove gas-exactness), so AnalyzeLadder rejects it.
func constGas(op byte) (uint64, bool) {
	switch {
	case op == 0x5b: // JUMPDEST
		return 1, true
	case op == 0x50: // POP
		return 2, true
	case op == 0x5f: // PUSH0
		return 2, true
	case op >= 0x60 && op <= 0x7f: // PUSH1..PUSH32
		return 3, true
	case op >= 0x80 && op <= 0x8f: // DUP1..DUP16
		return 3, true
	case op >= 0x90 && op <= 0x9f: // SWAP1..SWAP16
		return 3, true
	case op == 0x09, op == 0x08: // MULMOD, ADDMOD
		return 8, true
	case op == 0x01, op == 0x03: // ADD, SUB
		return 3, true
	case op == 0x02: // MUL
		return 5, true
	case op == 0x04, op == 0x06, op == 0x05, op == 0x07: // DIV, MOD, SDIV, SMOD
		return 5, true
	case op == 0x10, op == 0x11, op == 0x12, op == 0x13, op == 0x14, op == 0x15: // LT,GT,SLT,SGT,EQ,ISZERO
		return 3, true
	case op == 0x16, op == 0x17, op == 0x18, op == 0x19: // AND,OR,XOR,NOT
		return 3, true
	case op == 0x1b, op == 0x1c, op == 0x1d: // SHL,SHR,SAR
		return 3, true
	case op == 0x35: // CALLDATALOAD (pure read, constant gas)
		return 3, true
	}
	return 0, false
}

// isBinaryArith reports whether op is a 2-pop/1-push arithmetic opcode with the
// constant gas priced in constGas (so a pure block containing it stays gas-exact).
func isBinaryArith(op byte) bool {
	switch op {
	case 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, // ADD,MUL,SUB,DIV,SDIV,MOD,SMOD
		0x10, 0x11, 0x12, 0x13, 0x14, // LT,GT,SLT,SGT,EQ
		0x16, 0x17, 0x18, // AND,OR,XOR
		0x1b, 0x1c, 0x1d: // SHL,SHR,SAR
		return true
	}
	return false
}

// isBlockEnd reports whether op terminates a straight-line (jump-free) block.
func isBlockEnd(op byte) bool {
	switch op {
	case 0x56, 0x57, // JUMP, JUMPI
		0x00, 0xf3, 0xfd, 0xfe, 0xff, // STOP, RETURN, REVERT, INVALID, SELFDESTRUCT
		0x5b: // JUMPDEST starts a new block
		return true
	}
	return false
}

// isImpure reports whether op has a side effect or memory/storage interaction
// that makes a static substitution unsafe (also implies non-constant gas here).
func isImpure(op byte) bool {
	switch op {
	case 0x51, 0x52, 0x53, // MLOAD, MSTORE, MSTORE8
		0x54, 0x55, // SLOAD, SSTORE
		0x20,                   // KECCAK256 (reads memory)
		0x37, 0x39, 0x3c, 0x3e, // CALLDATACOPY, CODECOPY, EXTCODECOPY, RETURNDATACOPY
		0x5e,                                     // MCOPY
		0xa0, 0xa1, 0xa2, 0xa3, 0xa4,             // LOG0..LOG4
		0xf0, 0xf1, 0xf2, 0xf4, 0xf5, 0xfa, 0xff: // CREATE/CALL family, SELFDESTRUCT
		return true
	}
	return false
}

const seedEntries = 32 // abstract entry-stack depth modeled (block reaches <=7 deep)

type aKind uint8

const (
	aEntry aKind = iota
	aConst
	aComputed
)

type aslot struct {
	kind  aKind
	idx   int          // entry depth for aEntry
	value *uint256.Int // for aConst
}

// AnalyzeLadder abstract-interprets the jump-free block beginning at jumpdestPC
// and, if it is a recognized BN254 ladder, returns its substitution contract.
// It is allocation-light and bails out cheaply on non-ladder blocks.
func AnalyzeLadder(code []byte, jumpdestPC uint64) (LadderMeta, bool) {
	n := uint64(len(code))
	if jumpdestPC >= n || code[jumpdestPC] != 0x5b {
		return LadderMeta{}, false
	}

	// Seed an abstract stack: stack[0] is the deepest entry slot, stack[len-1]
	// is the top. Entry depth of position j is (seedEntries-1-j).
	stack := make([]aslot, seedEntries)
	for j := 0; j < seedEntries; j++ {
		stack[j] = aslot{kind: aEntry, idx: seedEntries - 1 - j}
	}

	var gas uint64
	pure := true
	mulmods := 0
	baseDepth := -1

	pop := func() (aslot, bool) {
		if len(stack) == 0 {
			return aslot{}, false
		}
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return s, true
	}
	push := func(s aslot) { stack = append(stack, s) }

	// popN pops n abstract slots, reporting underflow.
	popN := func(cnt int) bool {
		for k := 0; k < cnt; k++ {
			if _, ok := pop(); !ok {
				return false
			}
		}
		return true
	}

	i := jumpdestPC
	for i < n {
		op := code[i]
		if op != 0x5b && isBlockEnd(op) { // terminator (the starting JUMPDEST is consumed below)
			break
		}
		if i != jumpdestPC && op == 0x5b { // a later JUMPDEST starts a new block
			break
		}
		if isImpure(op) {
			pure = false // GasStatic is only trusted for pure blocks (see doc)
		}
		if g, ok := constGas(op); ok {
			gas += g
		} else if !isImpure(op) {
			// A non-impure opcode we cannot price (e.g. EXP, GAS — dynamic) would
			// break gas-exactness of a "pure" block: reject the whole block.
			return LadderMeta{}, false
		}

		switch {
		case op == 0x5b: // JUMPDEST (no stack effect)
			i++
		case op == 0x5f: // PUSH0
			push(aslot{kind: aConst, value: new(uint256.Int)})
			i++
		case op >= 0x60 && op <= 0x7f: // PUSHn
			nbytes := int(op - 0x5f)
			v := new(uint256.Int)
			if i+1+uint64(nbytes) <= n {
				v.SetBytes(code[i+1 : i+1+uint64(nbytes)])
			}
			push(aslot{kind: aConst, value: v})
			i += 1 + uint64(nbytes)
		case op >= 0x80 && op <= 0x8f: // DUPn
			d := int(op - 0x7f)
			if len(stack) < d {
				return LadderMeta{}, false
			}
			push(stack[len(stack)-d])
			i++
		case op >= 0x90 && op <= 0x9f: // SWAPn
			d := int(op - 0x8f)
			if len(stack) < d+1 {
				return LadderMeta{}, false
			}
			top := len(stack) - 1
			stack[top], stack[top-d] = stack[top-d], stack[top]
			i++
		case op == 0x50: // POP
			if !popN(1) {
				return LadderMeta{}, false
			}
			i++
		case op == 0x09, op == 0x08: // MULMOD, ADDMOD (pop3 push1)
			a, ok1 := pop()
			if !ok1 || !popN(2) {
				return LadderMeta{}, false
			}
			if op == 0x09 && mulmods == 0 && a.kind == aEntry {
				baseDepth = a.idx // x is the first operand of the first MULMOD (x*x)
			}
			if op == 0x09 {
				mulmods++
			}
			push(aslot{kind: aComputed})
			i++
		case op == 0x15, op == 0x19, op == 0x35, op == 0x51, op == 0x54:
			// ISZERO, NOT, CALLDATALOAD, MLOAD, SLOAD (pop1 push1)
			if !popN(1) {
				return LadderMeta{}, false
			}
			push(aslot{kind: aComputed})
			i++
		case op == 0x52, op == 0x53, op == 0x55: // MSTORE, MSTORE8, SSTORE (pop2 push0)
			if !popN(2) {
				return LadderMeta{}, false
			}
			i++
		case op == 0x39, op == 0x37, op == 0x3e, op == 0x5e: // CODECOPY/CALLDATACOPY/RETURNDATACOPY/MCOPY (pop3 push0)
			if !popN(3) {
				return LadderMeta{}, false
			}
			i++
		case op == 0x20: // KECCAK256 (pop2 push1)
			if !popN(2) {
				return LadderMeta{}, false
			}
			push(aslot{kind: aComputed})
			i++
		case isBinaryArith(op): // ADD,SUB,MUL,DIV,MOD,LT,GT,EQ,AND,OR,XOR,SHL,SHR,... (pop2 push1)
			if !popN(2) {
				return LadderMeta{}, false
			}
			push(aslot{kind: aComputed})
			i++
		default:
			return LadderMeta{}, false // unknown opcode -> cannot model stack safely
		}
	}

	if mulmods < minLadderMULMODs || baseDepth < 0 {
		return LadderMeta{}, false
	}
	endPC := i

	// Identify the untouched bottom: the longest prefix of the final stack that is
	// still the original entry slots in order. Everything above it is the new top.
	t := 0
	for t < len(stack) && t < seedEntries {
		s := stack[t]
		if s.kind == aEntry && s.idx == seedEntries-1-t {
			t++
			continue
		}
		break
	}
	in := seedEntries - t
	if in < 0 || in > seedEntries {
		return LadderMeta{}, false
	}

	out := make([]OutSlot, 0, len(stack)-t)
	computed := 0
	for j := t; j < len(stack); j++ {
		s := stack[j]
		switch s.kind {
		case aEntry:
			if s.idx >= in { // references an untouched slot -> model too shallow
				return LadderMeta{}, false
			}
			out = append(out, OutSlot{Kind: OutEntry, Depth: s.idx})
		case aComputed:
			out = append(out, OutSlot{Kind: OutResult})
			computed++
		case aConst:
			out = append(out, OutSlot{Kind: OutConst, Const: s.value})
		}
	}
	if computed != 1 || baseDepth >= in {
		return LadderMeta{}, false
	}

	return LadderMeta{
		EndPC:     endPC,
		GasStatic: gas,
		Pure:      pure,
		BaseDepth: baseDepth,
		In:        in,
		Out:       out,
	}, true
}
