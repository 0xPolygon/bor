package vm

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// ---------------------------------------------------------------------------
// Bytecode construction helpers
// ---------------------------------------------------------------------------

func cc(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func p1(v byte) []byte    { return []byte{byte(PUSH1), v} }
func op1(o OpCode) []byte { return []byte{byte(o)} }

func p32(v *uint256.Int) []byte {
	b := v.Bytes32()
	out := make([]byte, 33)
	out[0] = byte(PUSH32)
	copy(out[1:], b[:])
	return out
}

// retSeq: PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
var retSeq = cc(p1(0), op1(MSTORE), p1(32), p1(0), op1(RETURN))

func withRet(code []byte) []byte { return cc(code, retSeq) }

// binary: push b, push a, OP → result, then MSTORE+RETURN
func binSmall(a, b byte, op OpCode) []byte { return withRet(cc(p1(b), p1(a), op1(op))) }

// binary with big values
func binBig(a, b *uint256.Int, op OpCode) []byte { return withRet(cc(p32(b), p32(a), op1(op))) }

// ternary: push c, push b, push a, OP → result, then MSTORE+RETURN
func ternSmall(a, b, c byte, op OpCode) []byte {
	return withRet(cc(p1(c), p1(b), p1(a), op1(op)))
}

// unary with small value
func unSmall(a byte, op OpCode) []byte { return withRet(cc(p1(a), op1(op))) }

// unary with big value
func unBig(a *uint256.Int, op OpCode) []byte { return withRet(cc(p32(a), op1(op))) }

// ---------------------------------------------------------------------------
// Error classification – compares error *category* so that minor string
// differences between paths don't cause false positives.
// ---------------------------------------------------------------------------

func classifyErr(err error) string {
	if err == nil {
		return ""
	}
	var su *ErrStackUnderflow
	if errors.As(err, &su) {
		return "stack_underflow"
	}
	var so *ErrStackOverflow
	if errors.As(err, &so) {
		return "stack_overflow"
	}
	var ioc *ErrInvalidOpCode
	if errors.As(err, &ioc) {
		return "invalid_opcode"
	}
	if errors.Is(err, ErrOutOfGas) {
		return "out_of_gas"
	}
	if errors.Is(err, ErrInvalidJump) {
		return "invalid_jump"
	}
	if errors.Is(err, ErrExecutionReverted) {
		return "execution_reverted"
	}
	return "other:" + err.Error()
}

// ---------------------------------------------------------------------------
// Chain config that enables all forks through Cancun (no Verkle / EIP-4762).
// ---------------------------------------------------------------------------

var diffChainConfig = &params.ChainConfig{
	ChainID:             big.NewInt(1),
	HomesteadBlock:      new(big.Int),
	DAOForkBlock:        new(big.Int),
	EIP150Block:         new(big.Int),
	EIP155Block:         new(big.Int),
	EIP158Block:         new(big.Int),
	ByzantiumBlock:      new(big.Int),
	ConstantinopleBlock: new(big.Int),
	PetersburgBlock:     new(big.Int),
	IstanbulBlock:       new(big.Int),
	MuirGlacierBlock:    new(big.Int),
	BerlinBlock:         new(big.Int),
	LondonBlock:         new(big.Int),
	ArrowGlacierBlock:   new(big.Int),
	GrayGlacierBlock:    new(big.Int),
	MergeNetsplitBlock:  new(big.Int),
	ShanghaiBlock:       new(big.Int),
	CancunBlock:         new(big.Int),
	PragueBlock:         new(big.Int),
	OsakaBlock:          new(big.Int),
	// VerkleBlock intentionally nil — enabling it would activate EIP-4762
	// and force both paths into the slow Run() loop, defeating the test.
}

// execPath runs bytecode through the EVM. When switchDispatch is true the
// interpreter takes the runSwitch fast path; when false it uses the
// traditional Run() loop.
func execPath(code []byte, gas uint64, switchDispatch bool) ([]byte, uint64, error) {
	addr := common.BytesToAddress([]byte("contract"))
	caller := common.BytesToAddress([]byte("caller"))

	db, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	db.CreateAccount(addr)
	db.SetCode(addr, code, tracing.CodeChangeUnspecified)
	db.CreateAccount(caller)
	db.Finalise(true)

	bctx := BlockContext{
		CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int) {},
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(1),
		GasLimit:    gas,
		BaseFee:     big.NewInt(1),
		BlobBaseFee: big.NewInt(1),
		Random:      &common.Hash{},
	}

	rules := diffChainConfig.Rules(bctx.BlockNumber, bctx.Random != nil, bctx.Time)
	db.Prepare(rules, caller, common.Address{}, &addr, ActivePrecompiles(rules), nil)

	cfg := Config{EnableSwitchDispatch: switchDispatch}

	evm := NewEVM(bctx, db, diffChainConfig, cfg)
	evm.SetTxContext(TxContext{
		Origin:   caller,
		GasPrice: big.NewInt(1),
	})
	return evm.Call(caller, addr, nil, gas, new(uint256.Int))
}

// runDiff runs code through both interpreter paths and asserts that the
// return data, remaining gas, and error category are identical.
func runDiff(t *testing.T, code []byte, gas uint64) {
	t.Helper()

	retFast, gasFast, errFast := execPath(code, gas, true)
	retSlow, gasSlow, errSlow := execPath(code, gas, false)

	cFast, cSlow := classifyErr(errFast), classifyErr(errSlow)
	if cFast != cSlow {
		t.Fatalf("error class mismatch: fast=%q (%v) slow=%q (%v)", cFast, errFast, cSlow, errSlow)
	}
	if !bytes.Equal(retFast, retSlow) {
		t.Fatalf("return data mismatch:\n  fast(%d): %x\n  slow(%d): %x",
			len(retFast), retFast, len(retSlow), retSlow)
	}
	if gasFast != gasSlow {
		t.Fatalf("gas mismatch: fast=%d slow=%d", gasFast, gasSlow)
	}
}

// ---------------------------------------------------------------------------
// Commonly used uint256 values
// ---------------------------------------------------------------------------

var (
	maxU256  = new(uint256.Int).SetAllOne()                               // 2^256 − 1
	minI256  = new(uint256.Int).Lsh(uint256.NewInt(1), 255)               // 2^255  (most-negative signed)
	negOne   = new(uint256.Int).Sub(new(uint256.Int), uint256.NewInt(1))  // −1  in two's complement
	neg10    = new(uint256.Int).Sub(new(uint256.Int), uint256.NewInt(10)) // −10
	negThree = new(uint256.Int).Sub(new(uint256.Int), uint256.NewInt(3))  // −3
)

// ---------------------------------------------------------------------------
// TestDispatchDifferential — the main differential test suite.
// ---------------------------------------------------------------------------

func TestDispatchDifferential(t *testing.T) {
	t.Parallel()
	const G = uint64(100_000)

	type dc struct {
		name string
		code []byte
		gas  uint64
	}

	cases := []dc{
		// ============================================================
		//  STOP
		// ============================================================
		{"STOP/happy", op1(STOP), G},
		{"STOP/implicit_end", []byte{}, G},

		// ============================================================
		//  ADD  (a + b)
		// ============================================================
		{"ADD/happy/3+5", binSmall(3, 5, ADD), G},
		{"ADD/happy/0+7", binSmall(0, 7, ADD), G},
		{"ADD/happy/255+1", binSmall(255, 1, ADD), G},
		{"ADD/happy/overflow", binBig(uint256.NewInt(1), maxU256, ADD), G},
		{"ADD/err/underflow_empty", cc(op1(ADD), op1(STOP)), G},
		{"ADD/err/underflow_one", cc(p1(5), op1(ADD), op1(STOP)), G},
		{"ADD/err/out_of_gas", binSmall(3, 5, ADD), 2},

		// ============================================================
		//  MUL  (a * b)
		// ============================================================
		{"MUL/happy/3*5", binSmall(3, 5, MUL), G},
		{"MUL/happy/0*7", binSmall(0, 7, MUL), G},
		{"MUL/happy/1*255", binSmall(1, 255, MUL), G},
		{"MUL/happy/overflow", binBig(uint256.NewInt(2), maxU256, MUL), G},
		{"MUL/err/underflow", cc(op1(MUL), op1(STOP)), G},

		// ============================================================
		//  SUB  (a − b  where a = top)
		// ============================================================
		{"SUB/happy/10-3", binSmall(10, 3, SUB), G},
		{"SUB/happy/5-0", binSmall(5, 0, SUB), G},
		{"SUB/happy/wrap", binSmall(3, 10, SUB), G},
		{"SUB/err/underflow", cc(op1(SUB), op1(STOP)), G},

		// ============================================================
		//  DIV  (a / b)
		// ============================================================
		{"DIV/happy/10/3", binSmall(10, 3, DIV), G},
		{"DIV/happy/255/1", binSmall(255, 1, DIV), G},
		{"DIV/happy/zero_divisor", binSmall(10, 0, DIV), G},
		{"DIV/err/underflow", cc(op1(DIV), op1(STOP)), G},

		// ============================================================
		//  SDIV  (signed a / b)
		// ============================================================
		{"SDIV/happy/10/3", binSmall(10, 3, SDIV), G},
		{"SDIV/happy/neg10/3", binBig(neg10, uint256.NewInt(3), SDIV), G},
		{"SDIV/happy/10/neg3", binBig(uint256.NewInt(10), negThree, SDIV), G},
		{"SDIV/happy/zero_divisor", binSmall(10, 0, SDIV), G},
		{"SDIV/edge/min_div_neg1", binBig(minI256, negOne, SDIV), G},
		{"SDIV/err/underflow", cc(op1(SDIV), op1(STOP)), G},

		// ============================================================
		//  MOD  (a % b)
		// ============================================================
		{"MOD/happy/10%3", binSmall(10, 3, MOD), G},
		{"MOD/happy/zero_mod", binSmall(10, 0, MOD), G},
		{"MOD/happy/7%7", binSmall(7, 7, MOD), G},
		{"MOD/err/underflow", cc(op1(MOD), op1(STOP)), G},

		// ============================================================
		//  SMOD  (signed a % b)
		// ============================================================
		{"SMOD/happy/10%3", binSmall(10, 3, SMOD), G},
		{"SMOD/happy/neg10%3", binBig(neg10, uint256.NewInt(3), SMOD), G},
		{"SMOD/happy/zero_mod", binSmall(10, 0, SMOD), G},
		{"SMOD/err/underflow", cc(op1(SMOD), op1(STOP)), G},

		// ============================================================
		//  ADDMOD  ((a + b) % N)
		// ============================================================
		{"ADDMOD/happy/(10+10)%8", ternSmall(10, 10, 8, ADDMOD), G},
		{"ADDMOD/happy/zero_mod", ternSmall(10, 10, 0, ADDMOD), G},
		{"ADDMOD/happy/overflow",
			withRet(cc(p1(8), p32(maxU256), p1(1), op1(ADDMOD))), G},
		{"ADDMOD/err/underflow", cc(p1(1), p1(2), op1(ADDMOD), op1(STOP)), G},

		// ============================================================
		//  MULMOD  ((a * b) % N)
		// ============================================================
		{"MULMOD/happy/(10*10)%8", ternSmall(10, 10, 8, MULMOD), G},
		{"MULMOD/happy/zero_mod", ternSmall(10, 10, 0, MULMOD), G},
		{"MULMOD/err/underflow", cc(p1(1), p1(2), op1(MULMOD), op1(STOP)), G},

		// ============================================================
		//  EXP  (a ** b)  — non-inlined, goes through default path
		// ============================================================
		{"EXP/happy/2**10", binSmall(2, 10, EXP), G},
		{"EXP/happy/2**0", binSmall(2, 0, EXP), G},
		{"EXP/happy/0**0", binSmall(0, 0, EXP), G},
		{"EXP/happy/0**5", binSmall(0, 5, EXP), G},
		{"EXP/happy/3**3", binSmall(3, 3, EXP), G},
		{"EXP/happy/big_exp", binBig(uint256.NewInt(2), uint256.NewInt(255), EXP), G},
		{"EXP/err/underflow_empty", cc(op1(EXP), op1(STOP)), G},
		{"EXP/err/underflow_one", cc(p1(5), op1(EXP), op1(STOP)), G},
		{"EXP/err/out_of_gas", binSmall(2, 10, EXP), 5},

		// ============================================================
		//  SIGNEXTEND
		// ============================================================
		{"SIGNEXTEND/happy/extend_byte0_0xff", binSmall(0, 0xff, SIGNEXTEND), G},
		{"SIGNEXTEND/happy/extend_byte0_0x7f", binSmall(0, 0x7f, SIGNEXTEND), G},
		{"SIGNEXTEND/happy/noop_byte31", binSmall(31, 42, SIGNEXTEND), G},
		{"SIGNEXTEND/err/underflow", cc(op1(SIGNEXTEND), op1(STOP)), G},

		// ============================================================
		//  LT  (a < b → 1, else 0)
		// ============================================================
		{"LT/happy/true_3<5", binSmall(3, 5, LT), G},
		{"LT/happy/false_5<3", binSmall(5, 3, LT), G},
		{"LT/happy/equal", binSmall(5, 5, LT), G},
		{"LT/err/underflow", cc(op1(LT), op1(STOP)), G},

		// ============================================================
		//  GT  (a > b → 1, else 0)
		// ============================================================
		{"GT/happy/true_5>3", binSmall(5, 3, GT), G},
		{"GT/happy/false_3>5", binSmall(3, 5, GT), G},
		{"GT/happy/equal", binSmall(5, 5, GT), G},
		{"GT/err/underflow", cc(op1(GT), op1(STOP)), G},

		// ============================================================
		//  SLT  (signed a < b)
		// ============================================================
		{"SLT/happy/true_neg<pos", binBig(neg10, uint256.NewInt(5), SLT), G},
		{"SLT/happy/false_pos<neg", binBig(uint256.NewInt(5), neg10, SLT), G},
		{"SLT/happy/equal", binSmall(5, 5, SLT), G},
		{"SLT/err/underflow", cc(op1(SLT), op1(STOP)), G},

		// ============================================================
		//  SGT  (signed a > b)
		// ============================================================
		{"SGT/happy/true_pos>neg", binBig(uint256.NewInt(5), neg10, SGT), G},
		{"SGT/happy/false_neg>pos", binBig(neg10, uint256.NewInt(5), SGT), G},
		{"SGT/happy/equal", binSmall(5, 5, SGT), G},
		{"SGT/err/underflow", cc(op1(SGT), op1(STOP)), G},

		// ============================================================
		//  EQ
		// ============================================================
		{"EQ/happy/equal", binSmall(42, 42, EQ), G},
		{"EQ/happy/not_equal", binSmall(42, 99, EQ), G},
		{"EQ/happy/big_equal", binBig(maxU256, maxU256, EQ), G},
		{"EQ/err/underflow", cc(op1(EQ), op1(STOP)), G},

		// ============================================================
		//  ISZERO
		// ============================================================
		{"ISZERO/happy/zero", unSmall(0, ISZERO), G},
		{"ISZERO/happy/nonzero", unSmall(42, ISZERO), G},
		{"ISZERO/happy/max", unBig(maxU256, ISZERO), G},
		{"ISZERO/err/underflow", cc(op1(ISZERO), op1(STOP)), G},

		// ============================================================
		//  AND / OR / XOR
		// ============================================================
		{"AND/happy/0xff&0x0f", binSmall(0xff, 0x0f, AND), G},
		{"AND/happy/zero", binSmall(0xff, 0, AND), G},
		{"AND/err/underflow", cc(op1(AND), op1(STOP)), G},

		{"OR/happy/0xf0|0x0f", binSmall(0xf0, 0x0f, OR), G},
		{"OR/happy/same", binSmall(0xAB, 0xAB, OR), G},
		{"OR/err/underflow", cc(op1(OR), op1(STOP)), G},

		{"XOR/happy/0xff^0x0f", binSmall(0xff, 0x0f, XOR), G},
		{"XOR/happy/cancel", binSmall(42, 42, XOR), G},
		{"XOR/err/underflow", cc(op1(XOR), op1(STOP)), G},

		// ============================================================
		//  NOT
		// ============================================================
		{"NOT/happy/zero", unSmall(0, NOT), G},
		{"NOT/happy/max", unBig(maxU256, NOT), G},
		{"NOT/happy/one", unSmall(1, NOT), G},
		{"NOT/err/underflow", cc(op1(NOT), op1(STOP)), G},

		// ============================================================
		//  BYTE  (byte(n, x) — nth byte from MSB)
		// ============================================================
		{"BYTE/happy/byte31", binSmall(31, 0xAB, BYTE), G},
		{"BYTE/happy/byte30", binSmall(30, 0xAB, BYTE), G},
		{"BYTE/happy/out_of_range", binSmall(32, 0xAB, BYTE), G},
		{"BYTE/err/underflow", cc(op1(BYTE), op1(STOP)), G},

		// ============================================================
		//  SHL / SHR / SAR
		// ============================================================
		{"SHL/happy/shift1", binSmall(1, 1, SHL), G},
		{"SHL/happy/shift0", binSmall(0xff, 0, SHL), G},
		{"SHL/happy/shift256", binBig(uint256.NewInt(0xff), uint256.NewInt(256), SHL), G},
		{"SHL/err/underflow", cc(op1(SHL), op1(STOP)), G},

		{"SHR/happy/shift1", binSmall(0x80, 1, SHR), G},
		{"SHR/happy/shift0", binSmall(0xff, 0, SHR), G},
		{"SHR/happy/shift256", binBig(uint256.NewInt(0xff), uint256.NewInt(256), SHR), G},
		{"SHR/err/underflow", cc(op1(SHR), op1(STOP)), G},

		{"SAR/happy/positive", binSmall(0x80, 1, SAR), G},
		{"SAR/happy/negative", binBig(negOne, uint256.NewInt(1), SAR), G},
		{"SAR/happy/large_shift_pos", binBig(uint256.NewInt(42), uint256.NewInt(257), SAR), G},
		{"SAR/happy/large_shift_neg", binBig(negOne, uint256.NewInt(257), SAR), G},
		{"SAR/err/underflow", cc(op1(SAR), op1(STOP)), G},

		// ============================================================
		//  CLZ  (count leading zeros) — EIP-7939, non-inlined
		// ============================================================
		{"CLZ/happy/zero", unSmall(0, CLZ), G},
		{"CLZ/happy/one", unSmall(1, CLZ), G},
		{"CLZ/happy/0x80", unSmall(0x80, CLZ), G},
		{"CLZ/happy/0xff", unSmall(0xff, CLZ), G},
		{"CLZ/happy/max", unBig(maxU256, CLZ), G},
		{"CLZ/err/underflow", cc(op1(CLZ), op1(STOP)), G},

		// ============================================================
		//  POP
		// ============================================================
		{"POP/happy", cc(p1(42), p1(99), op1(POP), retSeq), G},
		{"POP/err/underflow", cc(op1(POP), op1(STOP)), G},

		// ============================================================
		//  JUMP
		// ============================================================
		{"JUMP/happy", []byte{
			byte(PUSH1), 4,
			byte(JUMP),
			byte(INVALID),
			byte(JUMPDEST),
			byte(PUSH1), 42,
			byte(PUSH1), 0, byte(MSTORE),
			byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN),
		}, G},
		{"JUMP/err/invalid_dest", []byte{
			byte(PUSH1), 3,
			byte(JUMP),
			byte(PUSH1), 0,
		}, G},
		{"JUMP/err/underflow", cc(op1(JUMP), op1(STOP)), G},

		// ============================================================
		//  JUMPI
		// ============================================================
		{"JUMPI/happy/taken", []byte{
			byte(PUSH1), 1,
			byte(PUSH1), 6,
			byte(JUMPI),
			byte(INVALID),
			byte(JUMPDEST),
			byte(PUSH1), 42,
			byte(PUSH1), 0, byte(MSTORE),
			byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN),
		}, G},
		{"JUMPI/happy/not_taken", []byte{
			byte(PUSH1), 0,
			byte(PUSH1), 99,
			byte(JUMPI),
			byte(PUSH1), 42,
			byte(PUSH1), 0, byte(MSTORE),
			byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN),
		}, G},
		{"JUMPI/err/invalid_dest", []byte{
			byte(PUSH1), 1,
			byte(PUSH1), 3,
			byte(JUMPI),
			byte(PUSH1), 0,
		}, G},
		{"JUMPI/err/underflow_empty", cc(op1(JUMPI), op1(STOP)), G},
		{"JUMPI/err/underflow_one", cc(p1(1), op1(JUMPI), op1(STOP)), G},

		// ============================================================
		//  PC
		// ============================================================
		{"PC/happy/at_offset_0", withRet(op1(PC)), G},
		{"PC/happy/at_offset_3", withRet(cc(p1(99), op1(POP), op1(PC))), G},

		// ============================================================
		//  MSIZE
		// ============================================================
		{"MSIZE/happy/no_memory", withRet(op1(MSIZE)), G},

		// ============================================================
		//  JUMPDEST
		// ============================================================
		{"JUMPDEST/happy", cc(op1(JUMPDEST), p1(42), retSeq), G},

		// ============================================================
		//  INVALID
		// ============================================================
		{"INVALID/error", []byte{byte(INVALID)}, G},

		// ============================================================
		//  PUSH0
		// ============================================================
		{"PUSH0/happy", withRet(op1(PUSH0)), G},

		// ============================================================
		//  PUSH1 special cases
		// ============================================================
		{"PUSH1/happy/42", withRet(p1(42)), G},
		{"PUSH1/happy/0", withRet(p1(0)), G},
		{"PUSH1/happy/255", withRet(p1(255)), G},
		{"PUSH1/edge/truncated", []byte{byte(PUSH1)}, G},

		// ============================================================
		//  PUSH2
		// ============================================================
		{"PUSH2/happy", withRet([]byte{byte(PUSH2), 0xAB, 0xCD}), G},
		{"PUSH2/edge/partial", []byte{byte(PUSH2), 0xAB}, G},
		{"PUSH2/edge/empty", []byte{byte(PUSH2)}, G},

		// ============================================================
		//  PUSH3
		// ============================================================
		{"PUSH3/happy", withRet([]byte{byte(PUSH3), 0x01, 0x02, 0x03}), G},

		// ============================================================
		//  PUSH4
		// ============================================================
		{"PUSH4/happy", withRet([]byte{byte(PUSH4), 0x01, 0x02, 0x03, 0x04}), G},

		// ============================================================
		//  Undefined opcode
		// ============================================================
		{"undefined/0x0C", []byte{0x0C}, G},
		{"undefined/0x0D", []byte{0x0D}, G},

		// ============================================================
		//  Out-of-gas edge cases
		// ============================================================
		{"out_of_gas/mul_tight", binSmall(3, 5, MUL), 5},
		{"out_of_gas/jump", []byte{
			byte(PUSH1), 4, byte(JUMP), byte(INVALID), byte(JUMPDEST), byte(STOP),
		}, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runDiff(t, tc.code, tc.gas)
		})
	}

	// =================================================================
	//  PUSH5 – PUSH32: programmatic happy-path + truncated
	// =================================================================
	for n := 5; n <= 32; n++ {
		opByte := byte(PUSH1) + byte(n-1) // PUSH1=0x60 .. PUSH32=0x7F

		t.Run(fmt.Sprintf("PUSH%d/happy", n), func(t *testing.T) {
			t.Parallel()
			code := make([]byte, 1+n)
			code[0] = opByte
			for i := 0; i < n; i++ {
				code[1+i] = byte(0xA0 + i%16)
			}
			runDiff(t, withRet(code), G)
		})

		t.Run(fmt.Sprintf("PUSH%d/edge/truncated", n), func(t *testing.T) {
			t.Parallel()
			code := make([]byte, 1+n/2) // only half the data bytes
			code[0] = opByte
			for i := 1; i < len(code); i++ {
				code[i] = 0xBB
			}
			runDiff(t, code, G)
		})
	}

	// =================================================================
	//  DUP1 – DUP16: happy path + underflow
	// =================================================================
	for n := 1; n <= 16; n++ {
		dupByte := byte(DUP1) + byte(n-1)

		t.Run(fmt.Sprintf("DUP%d/happy", n), func(t *testing.T) {
			t.Parallel()
			var code []byte
			for i := 0; i < n; i++ {
				code = append(code, byte(PUSH1), byte(i+1))
			}
			code = append(code, dupByte)
			runDiff(t, withRet(code), G)
		})

		t.Run(fmt.Sprintf("DUP%d/err/underflow", n), func(t *testing.T) {
			t.Parallel()
			var code []byte
			for i := 0; i < n-1; i++ {
				code = append(code, byte(PUSH1), byte(i+1))
			}
			code = append(code, dupByte, byte(STOP))
			runDiff(t, code, G)
		})
	}

	// =================================================================
	//  SWAP1 – SWAP16: happy path + underflow
	// =================================================================
	for n := 1; n <= 16; n++ {
		swapByte := byte(SWAP1) + byte(n-1)

		t.Run(fmt.Sprintf("SWAP%d/happy", n), func(t *testing.T) {
			t.Parallel()
			var code []byte
			for i := 0; i <= n; i++ {
				code = append(code, byte(PUSH1), byte(i+1))
			}
			code = append(code, swapByte)
			runDiff(t, withRet(code), G)
		})

		t.Run(fmt.Sprintf("SWAP%d/err/underflow", n), func(t *testing.T) {
			t.Parallel()
			var code []byte
			for i := 0; i < n; i++ {
				code = append(code, byte(PUSH1), byte(i+1))
			}
			code = append(code, swapByte, byte(STOP))
			runDiff(t, code, G)
		})
	}

	// =================================================================
	//  Stack overflow: push 1024 items then try one more
	// =================================================================
	t.Run("stack_overflow/push", func(t *testing.T) {
		t.Parallel()
		code := make([]byte, 0, 1024*2+3)
		for i := 0; i < 1024; i++ {
			code = append(code, byte(PUSH1), 0)
		}
		code = append(code, byte(PUSH1), 0, byte(STOP))
		runDiff(t, code, 1_000_000)
	})

	t.Run("stack_overflow/push0", func(t *testing.T) {
		t.Parallel()
		code := make([]byte, 0, 1025)
		for i := 0; i < 1024; i++ {
			code = append(code, byte(PUSH0))
		}
		code = append(code, byte(PUSH0))
		runDiff(t, code, 1_000_000)
	})

	t.Run("stack_overflow/dup", func(t *testing.T) {
		t.Parallel()
		code := make([]byte, 0, 1024*2+2)
		for i := 0; i < 1024; i++ {
			code = append(code, byte(PUSH1), 0)
		}
		code = append(code, byte(DUP1), byte(STOP))
		runDiff(t, code, 1_000_000)
	})

	t.Run("stack_overflow/pc", func(t *testing.T) {
		t.Parallel()
		code := make([]byte, 0, 1024*2+2)
		for i := 0; i < 1024; i++ {
			code = append(code, byte(PUSH1), 0)
		}
		code = append(code, byte(PC), byte(STOP))
		runDiff(t, code, 1_000_000)
	})

	t.Run("stack_overflow/msize", func(t *testing.T) {
		t.Parallel()
		code := make([]byte, 0, 1024*2+2)
		for i := 0; i < 1024; i++ {
			code = append(code, byte(PUSH1), 0)
		}
		code = append(code, byte(MSIZE), byte(STOP))
		runDiff(t, code, 1_000_000)
	})

	// =================================================================
	//  Compound programs: exercise multiple inlined opcodes together
	// =================================================================
	t.Run("compound/arithmetic_chain", func(t *testing.T) {
		t.Parallel()
		// (((5 + 3) * 2) - 1) / 3  →  15/3 = 5
		code := withRet(cc(
			p1(3), p1(2), p1(1), p1(3), p1(5),
			op1(ADD), // 5+3=8
			op1(MUL), // 8*2=16
			op1(SUB), // 16-1=15
			op1(DIV), // 15/3=5
		))
		runDiff(t, code, G)
	})

	t.Run("compound/logic_chain", func(t *testing.T) {
		t.Parallel()
		// (0xff AND 0x0f) OR 0xf0 → 0xff, XOR 0xff → 0, ISZERO → 1
		code := withRet(cc(
			p1(0xff), p1(0xf0), p1(0x0f), p1(0xff),
			op1(AND),    // 0xff & 0x0f = 0x0f
			op1(OR),     // 0x0f | 0xf0 = 0xff
			op1(XOR),    // 0xff ^ 0xff = 0
			op1(ISZERO), // iszero(0) = 1
		))
		runDiff(t, code, G)
	})

	t.Run("compound/dup_swap_pop", func(t *testing.T) {
		t.Parallel()
		// push 10, push 20, DUP2 → [10,20,10], SWAP1 → [10,10,20], POP → [10,10], ADD → 20
		code := withRet(cc(
			p1(20), p1(10),
			op1(DUP2),
			op1(SWAP1),
			op1(POP),
			op1(ADD),
		))
		runDiff(t, code, G)
	})

	t.Run("compound/jump_loop_3_iters", func(t *testing.T) {
		t.Parallel()
		// Counts down from 3 to 0 using JUMP/JUMPI loop, returns final counter
		code := []byte{
			byte(PUSH1), 3, // 0-1
			byte(JUMPDEST),  // 2
			byte(DUP1),      // 3
			byte(ISZERO),    // 4
			byte(PUSH1), 15, // 5-6 (exit at 15)
			byte(JUMPI),    // 7
			byte(PUSH1), 1, // 8-9
			byte(SWAP1),    // 10
			byte(SUB),      // 11
			byte(PUSH1), 2, // 12-13 (loop at 2)
			byte(JUMP),     // 14
			byte(JUMPDEST), // 15 (exit)
			byte(PUSH1), 0, byte(MSTORE),
			byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN),
		}
		runDiff(t, code, G)
	})

	t.Run("compound/shifts_combined", func(t *testing.T) {
		t.Parallel()
		// SHL 4 on 0x0F = 0xF0, then SHR 2 = 0x3C
		code := withRet(cc(
			p1(2), p1(4), p1(0x0F),
			op1(SHL), // 0x0F << 4 = 0xF0
			op1(SHR), // 0xF0 >> 2 = 0x3C
		))
		runDiff(t, code, G)
	})

	t.Run("compound/signextend_then_sar", func(t *testing.T) {
		t.Parallel()
		// SIGNEXTEND byte 0 of 0x80 → 0xFFFF...FF80 (sign-extended)
		// Then SAR 4 → arithmetic right shift
		code := withRet(cc(
			p1(4), p1(0), p1(0x80),
			op1(SIGNEXTEND),
			op1(SAR),
		))
		runDiff(t, code, G)
	})

	// =================================================================
	//  Non-inlined opcodes through default path (verifies fallthrough)
	// =================================================================

	// --- Memory ---

	t.Run("default_path/MLOAD", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(0), op1(MLOAD), retSeq)
		runDiff(t, code, G)
	})

	t.Run("default_path/MSTORE8", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(0xAB), p1(31), op1(MSTORE8), p1(32), p1(0), op1(RETURN))
		runDiff(t, code, G)
	})

	t.Run("default_path/MCOPY", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(42), p1(0), op1(MSTORE),
			p1(32), p1(0), p1(32), op1(MCOPY),
			p1(32), p1(32), op1(RETURN),
		)
		runDiff(t, code, G)
	})

	// --- Closure state (0x30 range) ---

	t.Run("default_path/ADDRESS", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(ADDRESS)), G)
	})

	t.Run("default_path/BALANCE", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(op1(ADDRESS), op1(BALANCE)))
		runDiff(t, code, G)
	})

	t.Run("default_path/ORIGIN", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(ORIGIN)), G)
	})

	t.Run("default_path/CALLER", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(CALLER)), G)
	})

	t.Run("default_path/CALLVALUE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(CALLVALUE)), G)
	})

	t.Run("default_path/CALLDATALOAD", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(p1(0), op1(CALLDATALOAD)))
		runDiff(t, code, G)
	})

	t.Run("default_path/CALLDATASIZE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(CALLDATASIZE)), G)
	})

	t.Run("default_path/CALLDATACOPY", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(32), p1(0), p1(0), op1(CALLDATACOPY),
			p1(32), p1(0), op1(RETURN),
		)
		runDiff(t, code, G)
	})

	t.Run("default_path/CODESIZE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(CODESIZE)), G)
	})

	t.Run("default_path/CODECOPY", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(4), p1(0), p1(0), op1(CODECOPY),
			p1(32), p1(0), op1(RETURN),
		)
		runDiff(t, code, G)
	})

	t.Run("default_path/GASPRICE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(GASPRICE)), G)
	})

	t.Run("default_path/EXTCODESIZE", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(op1(ADDRESS), op1(EXTCODESIZE)))
		runDiff(t, code, G)
	})

	t.Run("default_path/EXTCODECOPY", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(4), p1(0), p1(0), op1(ADDRESS),
			op1(EXTCODECOPY),
			p1(32), p1(0), op1(RETURN),
		)
		runDiff(t, code, G)
	})

	t.Run("default_path/RETURNDATASIZE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(RETURNDATASIZE)), G)
	})

	t.Run("default_path/EXTCODEHASH", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(op1(ADDRESS), op1(EXTCODEHASH)))
		runDiff(t, code, G)
	})

	// --- Block operations (0x40 range) ---

	t.Run("default_path/BLOCKHASH", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(p1(0), op1(BLOCKHASH)))
		runDiff(t, code, G)
	})

	t.Run("default_path/COINBASE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(COINBASE)), G)
	})

	t.Run("default_path/TIMESTAMP", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(TIMESTAMP)), G)
	})

	t.Run("default_path/NUMBER", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(NUMBER)), G)
	})

	t.Run("default_path/PREVRANDAO", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(PREVRANDAO)), G)
	})

	t.Run("default_path/GASLIMIT", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(GASLIMIT)), G)
	})

	t.Run("default_path/CHAINID", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(CHAINID)), G)
	})

	t.Run("default_path/SELFBALANCE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(SELFBALANCE)), G)
	})

	t.Run("default_path/BASEFEE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(BASEFEE)), G)
	})

	t.Run("default_path/BLOBHASH", func(t *testing.T) {
		t.Parallel()
		code := withRet(cc(p1(0), op1(BLOBHASH)))
		runDiff(t, code, G)
	})

	t.Run("default_path/BLOBBASEFEE", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(BLOBBASEFEE)), G)
	})

	// --- Storage (0x54–0x55) ---

	t.Run("default_path/SSTORE_SLOAD", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(42), p1(0), op1(SSTORE),
			p1(0), op1(SLOAD),
			retSeq,
		)
		runDiff(t, code, G)
	})

	// --- Transient storage (0x5c–0x5d, EIP-1153) ---

	t.Run("default_path/TSTORE_TLOAD", func(t *testing.T) {
		t.Parallel()
		code := cc(
			p1(42), p1(0), op1(TSTORE),
			p1(0), op1(TLOAD),
			retSeq,
		)
		runDiff(t, code, G)
	})

	// --- GAS ---

	t.Run("default_path/GAS/simple", func(t *testing.T) {
		t.Parallel()
		runDiff(t, withRet(op1(GAS)), G)
	})

	t.Run("default_path/GAS/after_accumulated_gas", func(t *testing.T) {
		t.Parallel()
		// ADD+ADD accumulate gas in runSwitch without flushing to contract.Gas.
		// GAS (default path) must flush before reading contract.Gas, otherwise
		// it would report a higher value than the old Run() path.
		code := withRet(cc(
			p1(1), p1(2), p1(3), p1(4),
			op1(ADD), // gasAccum += 3
			op1(ADD), // gasAccum += 3
			op1(POP), // gasAccum += 2
			op1(GAS), // default: flush gasAccum, charge GAS cost, push contract.Gas
		))
		runDiff(t, code, G)
	})

	// --- Crypto ---

	t.Run("default_path/KECCAK256", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(32), p1(0), op1(KECCAK256), retSeq)
		runDiff(t, code, G)
	})

	// --- Logging (0xa0–0xa4) ---

	t.Run("default_path/LOG0", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(32), p1(0), op1(LOG0), op1(STOP))
		runDiff(t, code, G)
	})

	t.Run("default_path/LOG1", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(0xAA), p1(32), p1(0), op1(LOG1), op1(STOP))
		runDiff(t, code, G)
	})

	t.Run("default_path/LOG2", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(0xBB), p1(0xAA), p1(32), p1(0), op1(LOG2), op1(STOP))
		runDiff(t, code, G)
	})

	t.Run("default_path/LOG3", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(0xCC), p1(0xBB), p1(0xAA), p1(32), p1(0), op1(LOG3), op1(STOP))
		runDiff(t, code, G)
	})

	t.Run("default_path/LOG4", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(42), p1(0), op1(MSTORE), p1(0xDD), p1(0xCC), p1(0xBB), p1(0xAA), p1(32), p1(0), op1(LOG4), op1(STOP))
		runDiff(t, code, G)
	})

	// --- REVERT ---

	t.Run("default_path/REVERT/empty", func(t *testing.T) {
		t.Parallel()
		code := cc(p1(0), p1(0), op1(REVERT))
		runDiff(t, code, G)
	})

	t.Run("default_path/REVERT/with_data", func(t *testing.T) {
		t.Parallel()
		// Store 0xDEAD at memory offset 0, then REVERT with offset=0 size=32.
		// The revert data should be returned to the caller.
		code := cc(p1(0xDE), p1(0), op1(MSTORE8), p1(0xAD), p1(1), op1(MSTORE8),
			p1(32), p1(0), op1(REVERT))
		runDiff(t, code, G)
	})
}

// TestPreShanghaiForkGate verifies that PUSH0 is correctly rejected on a
// pre-Shanghai chain config. Without the IsShanghai gate on runSwitch, the
// fast path inlines PUSH0 unconditionally and incorrectly executes it.
func TestPreShanghaiForkGate(t *testing.T) {
	// Chain config with all forks through Merge, but Shanghai NOT activated.
	preShanghaiConfig := &params.ChainConfig{
		ChainID:             big.NewInt(1),
		HomesteadBlock:      new(big.Int),
		DAOForkBlock:        new(big.Int),
		EIP150Block:         new(big.Int),
		EIP155Block:         new(big.Int),
		EIP158Block:         new(big.Int),
		ByzantiumBlock:      new(big.Int),
		ConstantinopleBlock: new(big.Int),
		PetersburgBlock:     new(big.Int),
		IstanbulBlock:       new(big.Int),
		MuirGlacierBlock:    new(big.Int),
		BerlinBlock:         new(big.Int),
		LondonBlock:         new(big.Int),
		ArrowGlacierBlock:   new(big.Int),
		GrayGlacierBlock:    new(big.Int),
		MergeNetsplitBlock:  new(big.Int),
		// ShanghaiBlock intentionally nil — PUSH0 should be invalid.
	}

	execWithConfig := func(code []byte, gas uint64, switchDispatch bool, chainCfg *params.ChainConfig) ([]byte, uint64, error) {
		addr := common.BytesToAddress([]byte("contract"))
		caller := common.BytesToAddress([]byte("caller"))

		db, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		db.CreateAccount(addr)
		db.SetCode(addr, code, tracing.CodeChangeUnspecified)
		db.CreateAccount(caller)
		db.Finalise(true)

		bctx := BlockContext{
			CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
			Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int) {},
			GetHash:     func(uint64) common.Hash { return common.Hash{} },
			BlockNumber: big.NewInt(1),
			Time:        1,
			Difficulty:  big.NewInt(1),
			GasLimit:    gas,
			BaseFee:     big.NewInt(1),
			BlobBaseFee: big.NewInt(1),
			Random:      &common.Hash{},
		}

		rules := chainCfg.Rules(bctx.BlockNumber, bctx.Random != nil, bctx.Time)
		db.Prepare(rules, caller, common.Address{}, &addr, ActivePrecompiles(rules), nil)

		evmCfg := Config{EnableSwitchDispatch: switchDispatch}

		evm := NewEVM(bctx, db, chainCfg, evmCfg)
		evm.SetTxContext(TxContext{
			Origin:   caller,
			GasPrice: big.NewInt(1),
		})
		return evm.Call(caller, addr, nil, gas, new(uint256.Int))
	}

	// PUSH0, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
	// If PUSH0 is allowed this returns 32 bytes of zeros.
	// If PUSH0 is rejected this should return an invalid_opcode error.
	code := cc(op1(PUSH0), retSeq)
	gas := uint64(100_000)

	// Slow path (switch dispatch disabled) — should reject PUSH0 as invalid opcode.
	_, _, errSlow := execWithConfig(code, gas, false, preShanghaiConfig)
	slowClass := classifyErr(errSlow)
	if slowClass != "invalid_opcode" {
		t.Fatalf("slow path: expected invalid_opcode, got %q (%v)", slowClass, errSlow)
	}

	// Fast path (switch dispatch enabled) — without the IsShanghai gate, this incorrectly succeeds.
	_, _, errFast := execWithConfig(code, gas, true, preShanghaiConfig)
	fastClass := classifyErr(errFast)

	if fastClass != slowClass {
		t.Fatalf("fork gate bug: fast path returned %q but slow path returned %q\n"+
			"  fast err: %v\n  slow err: %v\n"+
			"  runSwitch inlines PUSH0 unconditionally — needs IsShanghai gate",
			fastClass, slowClass, errFast, errSlow)
	}
	t.Logf("both paths correctly reject PUSH0 pre-Shanghai: %q", fastClass)
}
