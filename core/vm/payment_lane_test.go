package vm

import (
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

func laneWord(b []byte) []byte {
	return common.LeftPadBytes(b, 32)
}

func laneCalldata(sel [4]byte, words ...[]byte) []byte {
	out := append([]byte{}, sel[:]...)
	for _, w := range words {
		out = append(out, laneWord(w)...)
	}

	return out
}

// signedCalldata encodes an EIP-3009 call. With sig nil it is the v,r,s
// variant (nine static words), otherwise the bytes-signature variant.
func signedCalldata(sel [4]byte, from, to common.Address, amount []byte, sig []byte) []byte {
	words := [][]byte{from[:], to[:], amount, {}, {0xff}, {0x01}}
	if sig == nil {
		return laneCalldata(sel, append(words, []byte{27}, []byte{0x11}, []byte{0x22})...)
	}

	out := laneCalldata(sel, append(words, big.NewInt(7*32).Bytes(), big.NewInt(int64(len(sig))).Bytes())...)
	padded := make([]byte, (len(sig)+31)/32*32)
	copy(padded, sig)

	return append(out, padded...)
}

func TestIsPaymentCall(t *testing.T) {
	t.Parallel()

	var (
		payer = common.HexToAddress("0xaa")
		payee = common.HexToAddress("0xbb")
		relay = common.HexToAddress("0xcc")
		one   = []byte{1}
		zero  = []byte{0}
		sig65 = make([]byte, 65)
	)

	type tc struct {
		name   string
		caller common.Address
		input  []byte
		want   int  // canonical length, 0 = not a payment
		pulls  bool // a payment when allowance pulls are counted, if not otherwise
	}

	unknownSel := [4]byte{0x09, 0x5e, 0xa7, 0xb3}
	transfer := laneCalldata(selTransfer, payee[:], one)
	transferFrom := laneCalldata(selTransferFrom, payer[:], payee[:], one)
	tests := []tc{
		{"empty input", payer, nil, 0, false},
		{"short selector", payer, []byte{0xa9, 0x05, 0x9c}, 0, false},
		{"unknown selector", payer, laneCalldata(unknownSel, payee[:], one), 0, false},
		{"unknown selector, three words", payer, laneCalldata(unknownSel, payer[:], payee[:], one), 0, false},
		{"transfer", payer, transfer, 68, false},
		{"transfer with a few trailing bytes", payer, append(append([]byte{}, transfer...), make([]byte, maxPaymentTrailingBytes)...), 68, false},
		{"transfer with too many trailing bytes", payer, append(append([]byte{}, transfer...), make([]byte, maxPaymentTrailingBytes+1)...), 0, false},
		{"transfer truncated", payer, transfer[:67], 0, false},
		{"transfer zero amount", payer, laneCalldata(selTransfer, payee[:], zero), 0, false},
		{"transfer to self", payer, laneCalldata(selTransfer, payer[:], one), 0, false},
		{"transfer high amount byte", payer, laneCalldata(selTransfer, payee[:], append([]byte{1}, make([]byte, 31)...)), 68, false},
		{"transferFrom by owner", payer, transferFrom, 100, false},
		{"transferFrom by owner with trailing data", payer, append(append([]byte{}, transferFrom...), 0xff), 100, false},
		{"transferFrom allowance pull", payee, transferFrom, 0, true},
		{"transferFrom by third party", relay, transferFrom, 0, true},
		{"transferFrom by owner truncated", payer, transferFrom[:99], 0, false},
		{"transferFrom by owner zero", payer, laneCalldata(selTransferFrom, payer[:], payee[:], zero), 0, false},
		{"transferFrom by owner to self", payer, laneCalldata(selTransferFrom, payer[:], payer[:], one), 0, false},
	}

	for _, sel := range [][4]byte{selTransferWithAuth, selReceiveWithAuth} {
		full := signedCalldata(sel, payer, payee, one, nil)
		tests = append(tests,
			tc{"v,r,s " + common.Bytes2Hex(sel[:]), payee, full, 292, false},
			tc{"v,r,s relayed " + common.Bytes2Hex(sel[:]), relay, full, 292, false},
			tc{"v,r,s with trailing data " + common.Bytes2Hex(sel[:]), relay, append(append([]byte{}, full...), 1, 2, 3), 292, false},
			tc{"v,r,s truncated " + common.Bytes2Hex(sel[:]), relay, full[:291], 0, false},
			tc{"v,r,s zero amount " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payee, zero, nil), 0, false},
			tc{"v,r,s self " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payer, one, nil), 0, false},
		)
	}

	for _, sel := range [][4]byte{selTransferWithAuthSig, selReceiveWithAuthSig} {
		full := signedCalldata(sel, payer, payee, one, sig65)
		badOffset := append([]byte{}, full...)
		badOffset[4+7*32-1] = 0xc0
		tests = append(tests,
			tc{"bytes sig " + common.Bytes2Hex(sel[:]), relay, full, 4 + 8*32 + 96, false},
			tc{"bytes sig with trailing data " + common.Bytes2Hex(sel[:]), relay, append(append([]byte{}, full...), 0xff), 4 + 8*32 + 96, false},
			tc{"bytes sig at max length " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payee, one, make([]byte, maxPaymentSignatureLen)), 4 + 8*32 + maxPaymentSignatureLen, false},
			tc{"bytes sig above max length " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payee, one, make([]byte, maxPaymentSignatureLen+1)), 0, false},
			tc{"bytes sig empty " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payee, one, []byte{}), 0, false},
			tc{"bytes sig truncated " + common.Bytes2Hex(sel[:]), relay, full[:len(full)-1], 0, false},
			tc{"bytes sig without length word " + common.Bytes2Hex(sel[:]), relay, full[:4+7*32], 0, false},
			tc{"bytes sig unexpected offset " + common.Bytes2Hex(sel[:]), relay, badOffset, 0, false},
			tc{"bytes sig zero amount " + common.Bytes2Hex(sel[:]), relay, signedCalldata(sel, payer, payee, zero, sig65), 0, false},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A truncated input must not be readable past its length.
			tt.input = slices.Clip(tt.input)
			asset := common.HexToAddress("0x70ce")
			require.Equal(t, tt.want, paymentCall(tt.caller, asset, tt.input, false).n)
			require.Equal(t, tt.want > 0 || tt.pulls, paymentCall(tt.caller, asset, tt.input, true).n > 0)
		})
	}
}

func laneTestEVM(t *testing.T, assets ...common.Address) (*EVM, *state.StateDB) {
	t.Helper()

	db, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(0)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{"0": {Assets: assets, BaseFeeDiscountBps: 5000, ActiveGasPercentage: 70}}
	cfg.Bor = &bor

	ctx := BlockContext{
		CanTransfer:         func(StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:            func(StateDB, common.Address, common.Address, *uint256.Int) {},
		BlockNumber:         big.NewInt(1),
		BaseFee:             big.NewInt(1),
		Difficulty:          big.NewInt(1),
		PaymentLaneScaleBps: params.PaymentLaneMaxBps,
	}

	return NewEVM(ctx, db, &cfg, Config{}), db
}

// payToken stands in for a listed token: it logs Transfer(caller, to,
// amount) for transfer and Transfer(from, to, amount) for the other payment
// selectors, writes slot 0 and stops.
var payToken = common.FromHex("60003560e01c63a9059cbb146047576044356000526024356004357fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a36001600055005b602435600052600435337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005500")

// transferLogged prefixes code with the Transfer(caller, to, amount) log of a
// transfer call, so a token whose own code does more still logs its payment.
func transferLogged(code []byte) []byte {
	return append(common.FromHex("602435600052600435337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3"), code...)
}

// callOp appends op to target with the frame's full calldata, already
// copied to memory at 0, and pops the success flag.
func callOp(code []byte, op OpCode, target common.Address) []byte {
	code = append(code, byte(PUSH1), 0, byte(PUSH1), 0, byte(CALLDATASIZE), byte(PUSH1), 0)
	if op == CALL || op == CALLCODE {
		code = append(code, byte(PUSH1), 0)
	}

	code = append(code, byte(PUSH20))
	code = append(code, target[:]...)

	return append(code, byte(GAS), byte(op), byte(POP))
}

var copyCalldata = []byte{byte(CALLDATASIZE), byte(PUSH1), 0, byte(PUSH1), 0, byte(CALLDATACOPY)}

// forwardTo CALLs target with the frame's full calldata.
func forwardTo(target common.Address) []byte {
	return append(callOp(append([]byte{}, copyCalldata...), CALL, target), byte(STOP))
}

// forwardThenRevert CALLs target, then reverts its own frame.
func forwardThenRevert(target common.Address) []byte {
	return append(callOp(append([]byte{}, copyCalldata...), CALL, target), byte(PUSH1), 0, byte(PUSH1), 0, byte(REVERT))
}

// forwardTwice CALLs target twice in sequence.
func forwardTwice(target common.Address) []byte {
	code := callOp(append([]byte{}, copyCalldata...), CALL, target)
	return append(callOp(code, CALL, target), byte(STOP))
}

// delegateTo DELEGATECALLs target and succeeds whatever the outcome.
func delegateTo(target common.Address) []byte {
	return append(callOp(append([]byte{}, copyCalldata...), DELEGATECALL, target), byte(STOP))
}

// staticCallTo STATICCALLs target with the frame's full calldata, then
// succeeds whatever the outcome.
func staticCallTo(target common.Address) []byte {
	return append(callOp(append([]byte{}, copyCalldata...), STATICCALL, target), byte(STOP))
}

// burnGas loops until roughly n gas is spent, then stops.
func burnGas(n uint64) []byte {
	// counter = n/26 (26 gas per iteration); loop: counter -= 1; jumpi while counter != 0
	iters := n / 26
	code := []byte{byte(PUSH4), byte(iters >> 24), byte(iters >> 16), byte(iters >> 8), byte(iters)}
	loop := byte(len(code))
	code = append(code, byte(JUMPDEST), byte(PUSH1), 1, byte(SWAP1), byte(SUB), byte(DUP1), byte(PUSH1), loop, byte(JUMPI), byte(STOP))

	return code
}

// staticCallTwice STATICCALLs target twice, then stops.
func staticCallTwice(target common.Address) []byte {
	code := callOp(append([]byte{}, copyCalldata...), STATICCALL, target)
	return append(callOp(code, STATICCALL, target), byte(STOP))
}

// createAndStop CREATEs a contract with init code init, then stops.
func createAndStop(init []byte) []byte {
	code := writeMemory(init)
	return append(code, byte(PUSH2), byte(len(init)>>8), byte(len(init)), byte(PUSH1), 0, byte(PUSH1), 0, byte(CREATE), byte(POP), byte(STOP))
}

// writeMemory stores data at memory offset 0, word by word.
func writeMemory(data []byte) []byte {
	var code []byte
	for off := 0; off < len(data); off += 32 {
		word := make([]byte, 32)
		copy(word, data[off:])
		code = append(code, byte(PUSH32))
		code = append(code, word...)
		code = append(code, byte(PUSH2), byte(off>>8), byte(off), byte(MSTORE))
	}

	return code
}

// createRunning CREATEs a contract whose init code CALLs target with input,
// then reverts the creation.
func createRunning(target common.Address, input []byte) []byte {
	init := writeMemory(input)
	init = append(init, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), byte(len(input)), byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH20))
	init = append(init, target[:]...)
	init = append(init, byte(GAS), byte(CALL), byte(POP), byte(PUSH1), 0, byte(PUSH1), 0, byte(REVERT))

	code := writeMemory(init)

	return append(code, byte(PUSH2), byte(len(init)>>8), byte(len(init)), byte(PUSH1), 0, byte(PUSH1), 0, byte(CREATE), byte(POP), byte(STOP))
}

func TestPaymentLaneOutermostFrameOnly(t *testing.T) {
	t.Parallel()

	var (
		sender  = common.HexToAddress("0x5e4de7")
		payee   = common.HexToAddress("0xbee")
		inner   = common.HexToAddress("0x70ce0001")
		outer   = common.HexToAddress("0x70ce0002")
		wrapper = common.HexToAddress("0x3a11e701")
		input   = laneCalldata(selTransfer, payee[:], []byte{1})
	)

	evm, db := laneTestEVM(t, inner, outer)
	db.SetCode(inner, payToken, tracing.CodeChangeUnspecified)
	db.SetCode(outer, transferLogged(forwardTo(inner)), tracing.CodeChangeUnspecified)
	db.SetCode(wrapper, forwardTo(outer), tracing.CodeChangeUnspecified)

	evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

	// The wrapper's own frame is not a payment; only outer is credited, and
	// inner, although a listed asset, runs outside outer's own code.
	const gas = 1_000_000
	_, left, err := evm.Call(sender, wrapper, input, gas, new(uint256.Int))
	require.NoError(t, err)

	nested, txCallLen := evm.PaymentLaneGas()
	require.Zero(t, txCallLen)
	require.Positive(t, nested)
	require.Less(t, nested, gas-left)

	// Calling outer directly makes it the tx-level frame: the calldata is
	// reported for intrinsic credit. inner is warm by now, so outer's own
	// CALL to it costs less than the first time.
	evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})
	_, left, err = evm.Call(sender, outer, input, gas, new(uint256.Int))
	require.NoError(t, err)

	direct, txCallLen := evm.PaymentLaneGas()
	require.Equal(t, 68, txCallLen)
	require.Positive(t, direct)
	require.LessOrEqual(t, direct, nested)
	require.Less(t, direct, gas-left, "inner's work is not outer's own")
}

func TestPaymentLaneNotMeteredBeforeFork(t *testing.T) {
	t.Parallel()

	token := common.HexToAddress("0x70ce0001")
	evm, db := laneTestEVM(t, token)
	db.SetCode(token, payToken, tracing.CodeChangeUnspecified)
	require.NotNil(t, evm.PaymentLane())

	cfg := *evm.ChainConfig()
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(2)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{"2": evm.PaymentLane()}
	cfg.Bor = &bor

	before := NewEVM(evm.Context, db, &cfg, Config{})
	require.Nil(t, before.PaymentLane())

	payee := common.HexToAddress("0xbee")
	_, _, err := before.Call(common.HexToAddress("0x5e4de7"), token, laneCalldata(selTransfer, payee[:], []byte{1}), 100_000, new(uint256.Int))
	require.NoError(t, err)

	laneGas, txCallLen := before.PaymentLaneGas()
	require.Zero(t, laneGas)
	require.Zero(t, txCallLen)
}

// Each block runs with the parameter set in force at its number.
func TestPaymentLaneUsesVersionAtBlock(t *testing.T) {
	t.Parallel()

	first := &params.PaymentLaneConfig{Assets: []common.Address{common.HexToAddress("0x01")}, ActiveGasPercentage: 70}
	retune := &params.PaymentLaneConfig{Assets: []common.Address{common.HexToAddress("0x02")}, ActiveGasPercentage: 70}

	cfg := *params.BorUnittestChainConfig
	bor := *cfg.Bor
	bor.PaymentLaneBlock = big.NewInt(10)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{"10": first, "20": retune}
	cfg.Bor = &bor

	for number, want := range map[int64]*params.PaymentLaneConfig{9: nil, 10: first, 19: first, 20: retune, 21: retune} {
		evm := NewEVM(BlockContext{BlockNumber: big.NewInt(number), Difficulty: big.NewInt(1)}, nil, &cfg, Config{})
		require.Same(t, want, evm.PaymentLane(), "block %d", number)
	}
}

// Metering does not depend on the block's discount scale: payment gas is
// still measured on a congested block, only its price is not reduced.
func TestPaymentLaneMeteredAtZeroScale(t *testing.T) {
	t.Parallel()

	token := common.HexToAddress("0x70ce0001")
	evm, db := laneTestEVM(t, token)
	db.SetCode(token, payToken, tracing.CodeChangeUnspecified)

	ctx := evm.Context
	ctx.PaymentLaneScaleBps = 0
	congested := NewEVM(ctx, db, evm.ChainConfig(), Config{})

	payee := common.HexToAddress("0xbee")
	_, _, err := congested.Call(common.HexToAddress("0x5e4de7"), token, laneCalldata(selTransfer, payee[:], []byte{1}), 100_000, new(uint256.Int))
	require.NoError(t, err)

	laneGas, txCallLen := congested.PaymentLaneGas()
	require.Positive(t, laneGas)
	require.Equal(t, 68, txCallLen)
}

func TestPaymentLaneCountsAllowancePullsWhenConfigured(t *testing.T) {
	t.Parallel()

	var (
		token  = common.HexToAddress("0x70ce0001")
		puller = common.HexToAddress("0x3a11e701")
		owner  = common.HexToAddress("0xaa")
		payee  = common.HexToAddress("0xbee")
		input  = laneCalldata(selTransferFrom, owner[:], payee[:], []byte{1})
	)

	for _, pulls := range []bool{false, true} {
		evm, db := laneTestEVM(t, token)
		evm.lane.cfg.CountAllowancePulls = pulls
		db.SetCode(token, payToken, tracing.CodeChangeUnspecified)
		db.SetCode(puller, forwardTo(token), tracing.CodeChangeUnspecified)
		evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

		_, _, err := evm.Call(common.HexToAddress("0x5e4de7"), puller, input, 1_000_000, new(uint256.Int))
		require.NoError(t, err)

		laneGas, _ := evm.PaymentLaneGas()
		require.Equal(t, pulls, laneGas > 0)
	}
}

// The payment lane is a pricing fork: it must not add, drop or swap any
// precompile relative to the fork it activates on top of.
func TestPaymentLanePrecompilesUnchanged(t *testing.T) {
	t.Parallel()

	base := params.BorUnittestChainConfig.Rules(big.NewInt(1), false, 0)
	require.False(t, base.IsPaymentLane)

	lane := base
	lane.IsPaymentLane = true

	before := activePrecompiledContracts(base)
	after := activePrecompiledContracts(lane)
	require.Equal(t, len(before), len(after))

	for addr, p := range before {
		require.Contains(t, after, addr)
		require.Equal(t, p, after[addr], addr.Hex())
	}
}

func runLaneFrames(t *testing.T, entry common.Address, codes map[common.Address][]byte, assets ...common.Address) (laneGas uint64, txCallLen int, used uint64) {
	t.Helper()

	evm, db := laneTestEVM(t, assets...)
	for addr, code := range codes {
		db.SetCode(addr, code, tracing.CodeChangeUnspecified)
	}

	evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

	payee := common.HexToAddress("0xbee")
	_, left, err := evm.Call(common.HexToAddress("0x5e4de7"), entry, laneCalldata(selTransfer, payee[:], []byte{1}), 1_000_000, new(uint256.Int))
	require.NoError(t, err)

	laneGas, txCallLen = evm.PaymentLaneGas()

	return laneGas, txCallLen, 1_000_000 - left
}

func TestPaymentLaneFrameAccounting(t *testing.T) {
	t.Parallel()

	var (
		token  = common.HexToAddress("0x70ce0001")
		impl   = common.HexToAddress("0x70ce1111")
		wallet = common.HexToAddress("0x3a11e701")
		rev    = common.HexToAddress("0x3a11e702")
		entry  = common.HexToAddress("0x3a11e703")
		hook   = common.HexToAddress("0x3a11e704")
		pure   = common.HexToAddress("0x70ce0002")
	)

	single, _, _ := runLaneFrames(t, wallet, map[common.Address][]byte{token: payToken, wallet: forwardTo(token)}, token)
	require.Positive(t, single)

	t.Run("sequential payments are both credited", func(t *testing.T) {
		t.Parallel()

		twice, txCallLen, used := runLaneFrames(t, wallet, map[common.Address][]byte{token: payToken, wallet: forwardTwice(token)}, token)
		require.Zero(t, txCallLen)
		require.Greater(t, twice, single)
		require.Less(t, twice, used)
	})

	t.Run("proxy delegatecall to its implementation is the asset's own work", func(t *testing.T) {
		t.Parallel()

		direct, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: payToken}, token)
		proxied, txCallLen, used := runLaneFrames(t, token, map[common.Address][]byte{token: delegateTo(impl), impl: payToken}, token)
		require.Equal(t, 68, txCallLen)
		require.Greater(t, proxied, direct)
		require.Equal(t, used, proxied, "the whole frame is credited")
	})

	t.Run("callback to another contract is not credited", func(t *testing.T) {
		t.Parallel()

		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: {byte(STOP)}}, token)
		costly, _, used := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: burnGas(200_000)}, token)
		require.Greater(t, used, uint64(200_000))
		require.Equal(t, cheap, costly, "gas burned in the callback must not be credited")
	})

	t.Run("every callback in a payment is excluded, not only the first", func(t *testing.T) {
		t.Parallel()

		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTwice(hook)), hook: {byte(STOP)}}, token)
		costly, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTwice(hook)), hook: burnGas(100_000)}, token)
		require.Equal(t, cheap, costly)
	})

	t.Run("work nested inside a callback is excluded once", func(t *testing.T) {
		t.Parallel()

		deep := common.HexToAddress("0x3a11e705")
		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: staticCallTo(deep), deep: {byte(STOP)}}, token)
		costly, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: staticCallTo(deep), deep: burnGas(100_000)}, token)
		require.Positive(t, cheap)
		require.Equal(t, cheap, costly)
	})

	t.Run("contract created by the asset is not credited", func(t *testing.T) {
		t.Parallel()

		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(createAndStop([]byte{byte(STOP)}))}, token)
		costly, _, used := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(createAndStop(burnGas(100_000)))}, token)
		require.Greater(t, used, uint64(100_000))
		require.Positive(t, cheap, "the asset's own work is still credited")
		require.Equal(t, cheap, costly)
	})

	t.Run("static call into a listed asset is not a payment", func(t *testing.T) {
		t.Parallel()

		gas, _, _ := runLaneFrames(t, wallet, map[common.Address][]byte{pure: {byte(PUSH1), 0, byte(POP), byte(STOP)}, wallet: staticCallTo(pure)}, pure)
		require.Zero(t, gas)
	})

	t.Run("call from the asset into another contract is not credited", func(t *testing.T) {
		t.Parallel()

		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(forwardTo(hook)), hook: {byte(STOP)}}, token)
		costly, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(forwardTo(hook)), hook: burnGas(200_000)}, token)
		require.Equal(t, cheap, costly)
	})

	t.Run("failing callback is still not credited", func(t *testing.T) {
		t.Parallel()

		cheap, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: {byte(STOP)}}, token)
		failing, _, _ := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(staticCallTo(hook)), hook: append(burnGas(200_000)[:len(burnGas(200_000))-1], byte(INVALID))}, token)
		require.Equal(t, cheap, failing)
	})

	t.Run("swallowed reverting call drops its credit", func(t *testing.T) {
		t.Parallel()

		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, rev: forwardThenRevert(token), entry: forwardTo(rev)}, token)
		require.Zero(t, gas)
	})

	t.Run("swallowed failing delegatecall drops its credit", func(t *testing.T) {
		t.Parallel()

		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, rev: forwardThenRevert(token), entry: delegateTo(rev)}, token)
		require.Zero(t, gas)
	})

	t.Run("swallowed failing callcode drops its credit", func(t *testing.T) {
		t.Parallel()

		// entry swallows the failure of its own CALLCODE frame, which paid.
		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, rev: forwardThenRevert(token), entry: append(callOp(append([]byte{}, copyCalldata...), CALLCODE, rev), byte(STOP))}, token)
		require.Zero(t, gas)

		kept, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, wallet: forwardTo(token), entry: append(callOp(append([]byte{}, copyCalldata...), CALLCODE, wallet), byte(STOP))}, token)
		require.Positive(t, kept)
	})

	t.Run("a payment inside a static call is never credited", func(t *testing.T) {
		t.Parallel()

		// The asset cannot log its transfer in a static frame, so it fails.
		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, wallet: forwardTo(token), entry: staticCallTo(wallet)}, token)
		require.Zero(t, gas)
	})

	t.Run("reverted creation drops its credit", func(t *testing.T) {
		t.Parallel()

		payee := common.HexToAddress("0xbee")
		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, entry: createRunning(token, laneCalldata(selTransfer, payee[:], []byte{1}))}, token)
		require.Zero(t, gas)
	})

	t.Run("successful delegatecall keeps its credit", func(t *testing.T) {
		t.Parallel()

		gas, _, _ := runLaneFrames(t, entry, map[common.Address][]byte{token: payToken, wallet: forwardTo(token), entry: delegateTo(wallet)}, token)
		require.Positive(t, gas)
	})

	t.Run("listed asset without code is not a payment", func(t *testing.T) {
		t.Parallel()

		gas, txCallLen, _ := runLaneFrames(t, token, map[common.Address][]byte{}, token)
		require.Zero(t, gas)
		require.Zero(t, txCallLen)
	})

	t.Run("unlisted token is not a payment", func(t *testing.T) {
		t.Parallel()

		gas, _, _ := runLaneFrames(t, wallet, map[common.Address][]byte{token: payToken, wallet: forwardTo(token)}, common.HexToAddress("0x70ce0099"))
		require.Zero(t, gas)
	})
}

func TestPaymentLaneMinAmount(t *testing.T) {
	t.Parallel()

	token := common.HexToAddress("0x70ce0001")
	payee := common.HexToAddress("0xbee")

	for _, tc := range []struct {
		amount int64
		want   bool
	}{{99, false}, {100, true}, {101, true}} {
		evm, db := laneTestEVM(t, token)
		evm.lane.cfg.MinAmounts = map[common.Address]*big.Int{token: big.NewInt(100)}
		db.SetCode(token, payToken, tracing.CodeChangeUnspecified)
		evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

		input := laneCalldata(selTransfer, payee[:], big.NewInt(tc.amount).Bytes())
		_, _, err := evm.Call(common.HexToAddress("0x5e4de7"), token, input, 100_000, new(uint256.Int))
		require.NoError(t, err)

		laneGas, _ := evm.PaymentLaneGas()
		require.Equal(t, tc.want, laneGas > 0, "amount %d", tc.amount)
	}
}

// logTransfer logs Transfer(from, to, amount) from the current context.
func logTransfer(from, to common.Address, amount byte) []byte {
	code := []byte{byte(PUSH1), amount, byte(PUSH1), 0, byte(MSTORE), byte(PUSH20)}
	code = append(code, to[:]...)
	code = append(code, byte(PUSH20))
	code = append(code, from[:]...)
	code = append(code, byte(PUSH32))
	code = append(code, transferEventSig[:]...)

	return append(code, byte(PUSH1), 0x20, byte(PUSH1), 0, byte(LOG3))
}

func TestPaymentLaneRequiresTransferLog(t *testing.T) {
	t.Parallel()

	var (
		sender = common.HexToAddress("0x5e4de7")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
		impl   = common.HexToAddress("0x70ce1111")
		hook   = common.HexToAddress("0x3a11e704")
		wallet = common.HexToAddress("0x3a11e701")
	)

	run := func(entry common.Address, codes map[common.Address][]byte) uint64 {
		gas, _, _ := runLaneFrames(t, entry, codes, token)
		return gas
	}

	require.Positive(t, run(token, map[common.Address][]byte{token: payToken}))

	for name, codes := range map[string]map[common.Address][]byte{
		"success without a log":  {token: {byte(PUSH1), 1, byte(PUSH1), 0, byte(SSTORE), byte(STOP)}},
		"wrong amount logged":    {token: append(logTransfer(sender, payee, 2), byte(STOP))},
		"wrong payee logged":     {token: append(logTransfer(sender, sender, 1), byte(STOP))},
		"wrong payer logged":     {token: append(logTransfer(payee, payee, 1), byte(STOP))},
		"log from another frame": {token: transferLogged(nil)[:0], hook: append(logTransfer(sender, payee, 1), byte(STOP))},
		"logged then rolled back by a swallowed failing delegatecall": {
			token: delegateTo(impl),
			impl:  append(logTransfer(sender, payee, 1), byte(PUSH1), 0, byte(PUSH1), 0, byte(REVERT)),
		},
	} {
		if len(codes[token]) == 0 {
			codes[token] = append(staticCallTo(hook)[:0:0], forwardTo(hook)...)
		}

		require.Zero(t, run(token, codes), name)
	}

	// The same holds for a payment made through a wallet.
	require.Zero(t, run(wallet, map[common.Address][]byte{token: {byte(STOP)}, wallet: forwardTo(token)}))
}

func TestPaymentLaneFrameCreditIsBounded(t *testing.T) {
	t.Parallel()

	token := common.HexToAddress("0x70ce0001")
	impl := common.HexToAddress("0x70ce1111")

	// The burn runs in the token's own context (a proxy delegatecall).
	heavy, _, used := runLaneFrames(t, token, map[common.Address][]byte{token: transferLogged(delegateTo(impl)), impl: burnGas(400_000)}, token)
	require.Greater(t, used, uint64(params.PaymentLaneMaxTransferFrameGas))
	require.Equal(t, uint64(params.PaymentLaneMaxTransferFrameGas), heavy, "transfer frames have the tighter bound")

	// EIP-3009 frames verify a signature, so they get the wider bound.
	sender, payee := common.HexToAddress("0x5e4de7"), common.HexToAddress("0xbee")
	evm, db := laneTestEVM(t, token)
	db.SetCode(token, append(pullLog(), delegateTo(impl)...), tracing.CodeChangeUnspecified)
	db.SetCode(impl, burnGas(400_000), tracing.CodeChangeUnspecified)
	evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})
	_, _, err := evm.Call(sender, token, signedCalldata(selTransferWithAuth, sender, payee, []byte{1}, nil), 1_000_000, new(uint256.Int))
	require.NoError(t, err)

	gas, _ := evm.PaymentLaneGas()
	require.Equal(t, uint64(params.PaymentLaneMaxFrameGas), gas)
}

// A proxy token copies its whole calldata into its own memory, so padding
// would inflate its frame: a payment call carries at most a few trailing
// bytes.
func TestPaymentLanePaddedCallThroughProxy(t *testing.T) {
	t.Parallel()

	var (
		sender = common.HexToAddress("0x5e4de7")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
		impl   = common.HexToAddress("0x70ce1111")
		wallet = common.HexToAddress("0x3a11e701")
	)

	run := func(trailing int) uint64 {
		evm, db := laneTestEVM(t, token)
		db.SetCode(token, delegateTo(impl), tracing.CodeChangeUnspecified)
		db.SetCode(impl, payToken, tracing.CodeChangeUnspecified)
		db.SetCode(wallet, forwardTo(token), tracing.CodeChangeUnspecified)
		evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

		input := append(laneCalldata(selTransfer, payee[:], []byte{1}), make([]byte, trailing)...)
		_, _, err := evm.Call(sender, wallet, input, 5_000_000, new(uint256.Int))
		require.NoError(t, err)

		gas, _ := evm.PaymentLaneGas()

		return gas
	}

	plain := run(0)
	require.Positive(t, plain)
	// The allowed trailing bytes only add their tiny copy cost.
	require.InDelta(t, plain, run(maxPaymentTrailingBytes), 10)
	require.Zero(t, run(100_000), "a padded call is not a payment")
}

// pullLog logs Transfer(word0, word1, word2) of the calldata, as a token does
// for transferFrom and EIP-3009 calls.
func pullLog() []byte {
	code := common.FromHex("604435600052602435600435")
	code = append(code, byte(PUSH32))
	code = append(code, transferEventSig[:]...)

	return append(code, byte(PUSH1), 0x20, byte(PUSH1), 0, byte(LOG3))
}

// logEvent logs amount with the given topics (1 to 4) from the current
// context, with data of dataLen bytes (the amount right-aligned in the
// first word).
func logEvent(amount byte, dataLen byte, topics ...common.Hash) []byte {
	code := []byte{byte(PUSH1), amount, byte(PUSH1), 0, byte(MSTORE)}
	for i := len(topics) - 1; i >= 0; i-- {
		code = append(code, byte(PUSH32))
		code = append(code, topics[i][:]...)
	}

	return append(code, byte(PUSH1), dataLen, byte(PUSH1), 0, byte(LOG0)+byte(len(topics)))
}

func TestPaymentLaneTransferLogEdgeCases(t *testing.T) {
	t.Parallel()

	var (
		sender = common.HexToAddress("0x5e4de7")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
		impl   = common.HexToAddress("0x70ce1111")
		hook   = common.HexToAddress("0x3a11e704")
		rev    = common.HexToAddress("0x3a11e702")
	)

	run := func(codes map[common.Address][]byte, input []byte) uint64 {
		evm, db := laneTestEVM(t, token)
		for addr, code := range codes {
			db.SetCode(addr, code, tracing.CodeChangeUnspecified)
		}

		evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})
		_, _, err := evm.Call(sender, token, input, 1_000_000, new(uint256.Int))
		require.NoError(t, err)

		gas, _ := evm.PaymentLaneGas()

		return gas
	}

	transfer := laneCalldata(selTransfer, payee[:], []byte{1})
	from, to := common.BytesToHash(sender[:]), common.BytesToHash(payee[:])
	approval := common.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925")

	t.Run("log from the implementation behind a proxy counts", func(t *testing.T) {
		t.Parallel()
		require.Positive(t, run(map[common.Address][]byte{token: delegateTo(impl), impl: append(logTransfer(sender, payee, 1), byte(STOP))}, transfer))
	})

	t.Run("log kept when a later inner call fails and is swallowed", func(t *testing.T) {
		t.Parallel()

		code := append(logTransfer(sender, payee, 1), delegateTo(rev)...)
		require.Positive(t, run(map[common.Address][]byte{token: code, rev: {byte(PUSH1), 0, byte(PUSH1), 0, byte(REVERT)}}, transfer))
	})

	t.Run("transferFrom by the owner matches on from", func(t *testing.T) {
		t.Parallel()
		require.Positive(t, run(map[common.Address][]byte{token: payToken}, laneCalldata(selTransferFrom, sender[:], payee[:], []byte{1})))
	})

	for name, code := range map[string][]byte{
		"four topics":         logEvent(1, 32, transferEventSig, from, to, common.Hash{}),
		"two topics":          logEvent(1, 32, transferEventSig, from),
		"other event":         logEvent(1, 32, approval, from, to),
		"amount padded":       logEvent(1, 64, transferEventSig, from, to),
		"amount in no data":   logEvent(1, 0, transferEventSig, from, to),
		"correct log matched": logEvent(1, 32, transferEventSig, from, to),
	} {
		gas := run(map[common.Address][]byte{token: append(code, byte(STOP))}, transfer)
		require.Equal(t, name == "correct log matched", gas > 0, name)
	}

	t.Run("re-entered asset logging from inside a callback does not count", func(t *testing.T) {
		t.Parallel()

		// The token forwards to the hook unless the hook is calling it back,
		// in which case it logs the exact transfer: a foreign frame's log.
		input := signedCalldata(selTransferWithAuth, sender, payee, []byte{1}, nil)
		reenter := []byte{byte(CALLER), byte(PUSH20)}
		reenter = append(reenter, hook[:]...)
		reenter = append(reenter, byte(EQ))
		fwd := forwardTo(hook)
		reenter = append(reenter, byte(PUSH1), byte(len(reenter)+3+len(fwd)), byte(JUMPI))
		reenter = append(reenter, fwd...)
		reenter = append(reenter, byte(JUMPDEST))
		reenter = append(reenter, pullLog()...)
		reenter = append(reenter, byte(STOP))

		require.Zero(t, run(map[common.Address][]byte{token: reenter, hook: forwardTo(token)}, input))
		require.Positive(t, run(map[common.Address][]byte{token: append(pullLog(), byte(STOP))}, input), "control: the same log in the asset's own frame")
	})
}

// Bridged tokens run native meta-transactions as a call to themselves with
// the signer appended; the payer is that signer.
func TestPaymentLaneMetaTransactionSelfCall(t *testing.T) {
	t.Parallel()

	var (
		user   = common.HexToAddress("0x05e4")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
		caller = common.HexToAddress("0x5e4de7")
	)

	run := func(from common.Address, input []byte) uint64 {
		evm, db := laneTestEVM(t, token)
		db.SetCode(token, append(logTransfer(user, payee, 1), byte(STOP)), tracing.CodeChangeUnspecified)
		evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

		_, _, err := evm.Call(from, token, input, 200_000, new(uint256.Int))
		require.NoError(t, err)

		gas, _ := evm.PaymentLaneGas()

		return gas
	}

	transfer := laneCalldata(selTransfer, payee[:], []byte{1})
	require.Positive(t, run(token, append(append([]byte{}, transfer...), user[:]...)), "self-call with the signer appended")
	require.Zero(t, run(token, append(append([]byte{}, transfer...), append(user[:], 0)...)), "not exactly an address appended")
	require.Zero(t, run(caller, append(append([]byte{}, transfer...), user[:]...)), "only the asset itself can append the signer")

	pull := laneCalldata(selTransferFrom, user[:], payee[:], []byte{1})
	require.Positive(t, run(token, append(pull, user[:]...)), "transferFrom by the signer")
}

func TestDirectPaymentCall(t *testing.T) {
	t.Parallel()

	var (
		sender = common.HexToAddress("0x5e4de7")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
	)

	lane := &params.PaymentLaneConfig{Assets: []common.Address{token}, ActiveGasPercentage: 70, MinAmounts: map[common.Address]*big.Int{token: big.NewInt(10)}}
	pay := func(amount int64) []byte { return laneCalldata(selTransfer, payee[:], big.NewInt(amount).Bytes()) }

	direct := func(lane *params.PaymentLaneConfig, to common.Address, input []byte) bool {
		callLen, maxGas := DirectPaymentCall(lane, sender, to, input)
		require.Equal(t, callLen == 0, maxGas == 0)

		return callLen > 0
	}

	callLen, maxGas := DirectPaymentCall(lane, sender, token, append(pay(10), 0xff))
	require.Equal(t, 4+2*32, callLen, "trailing bytes are not part of the payment")
	require.Equal(t, uint64(params.PaymentLaneMaxTransferFrameGas), maxGas)

	require.True(t, direct(lane, token, pay(10)))
	require.False(t, direct(lane, token, pay(9)), "below the minimum")
	require.False(t, direct(lane, token, laneCalldata(selTransfer, sender[:], []byte{10})), "not a payment shape")
	require.False(t, direct(lane, payee, pay(10)), "unlisted asset")
	require.False(t, direct(nil, token, pay(10)), "before the fork")
}

// laneFrameGas runs a payment to entry on top of committed token storage and
// returns the lane gas, the gas used by every frame per callee and the final
// refund counter.
func laneFrameGas(t *testing.T, entry, token common.Address, codes map[common.Address][]byte, slots map[common.Hash]common.Hash) (uint64, map[common.Address][]uint64, uint64) {
	t.Helper()

	evm, db := laneTestEVM(t, token)
	for addr, code := range codes {
		db.SetCode(addr, code, tracing.CodeChangeUnspecified)
	}

	for k, v := range slots {
		db.SetState(token, k, v)
	}

	root, err := db.Commit(0, true, false)
	require.NoError(t, err)

	db, err = state.New(root, db.Database())
	require.NoError(t, err)

	var (
		stack  []common.Address
		frames = map[common.Address][]uint64{}
	)

	hooks := &tracing.Hooks{
		OnEnter: func(_ int, _ byte, _, to common.Address, _ []byte, _ uint64, _ *big.Int) { stack = append(stack, to) },
		OnExit: func(_ int, _ []byte, gasUsed uint64, _ error, _ bool) {
			to := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			frames[to] = append(frames[to], gasUsed)
		},
	}

	evm = NewEVM(evm.Context, db, evm.ChainConfig(), Config{Tracer: hooks})
	evm.SetTxContext(TxContext{GasPrice: big.NewInt(1)})

	payee := common.HexToAddress("0xbee")
	_, _, err = evm.Call(common.HexToAddress("0x5e4de7"), entry, laneCalldata(selTransfer, payee[:], []byte{1}), 1_000_000, new(uint256.Int))
	require.NoError(t, err)

	laneGas, _ := evm.PaymentLaneGas()

	return laneGas, frames, db.GetRefund()
}

// Refunds earned by the asset's own code are paid back at settlement, so
// they are not part of the payment gas.
func TestPaymentLaneCreditExcludesRefunds(t *testing.T) {
	t.Parallel()

	var (
		token  = common.HexToAddress("0x70ce0001")
		impl   = common.HexToAddress("0x70ce1111")
		hook   = common.HexToAddress("0x3a11e704")
		wallet = common.HexToAddress("0x3a11e701")
		slot   = common.Hash{31: 7}
		funded = map[common.Hash]common.Hash{slot: {31: 1}}
	)

	setThenRestore := []byte{byte(PUSH1), 1, byte(PUSH1), 9, byte(SSTORE), byte(PUSH1), 0, byte(PUSH1), 9, byte(SSTORE)}
	toggle := []byte{byte(PUSH1), 7, byte(SLOAD), byte(ISZERO), byte(PUSH1), 7, byte(SSTORE), byte(STOP)}

	for name, tc := range map[string]struct {
		entry  common.Address
		codes  map[common.Address][]byte
		slots  map[common.Hash]common.Hash
		refund uint64
		want   func(f map[common.Address][]uint64) uint64
	}{
		"slot written and restored": {
			entry:  token,
			codes:  map[common.Address][]byte{token: transferLogged(setThenRestore)},
			refund: 19_900,
			want:   func(f map[common.Address][]uint64) uint64 { return f[token][0] - 19_900 },
		},
		"balance slot cleared, next to non-payment work": {
			entry:  wallet,
			codes:  map[common.Address][]byte{token: transferLogged(toggle), wallet: forwardTo(token)},
			slots:  funded,
			refund: 4_800,
			want:   func(f map[common.Address][]uint64) uint64 { return f[token][0] - 4_800 },
		},
		"refund given back by a later payment": {
			entry:  wallet,
			codes:  map[common.Address][]byte{token: transferLogged(toggle), wallet: forwardTwice(token)},
			slots:  funded,
			refund: 2_800,
			want:   func(f map[common.Address][]uint64) uint64 { return f[token][0] - 4_800 + f[token][1] },
		},
		"refund earned in a callback is excluded once": {
			entry:  token,
			codes:  map[common.Address][]byte{token: transferLogged(forwardTo(hook)), hook: append(setThenRestore, byte(STOP))},
			refund: 19_900,
			want:   func(f map[common.Address][]uint64) uint64 { return f[token][0] - f[hook][0] },
		},
		"refund rolled back with its frame": {
			entry:  token,
			codes:  map[common.Address][]byte{token: transferLogged(delegateTo(impl)), impl: append(setThenRestore, byte(PUSH1), 0, byte(PUSH1), 0, byte(REVERT))},
			refund: 0,
			want:   func(f map[common.Address][]uint64) uint64 { return f[token][0] },
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			laneGas, frames, refund := laneFrameGas(t, tc.entry, token, tc.codes, tc.slots)
			require.Equal(t, tc.refund, refund)
			require.Equal(t, tc.want(frames), laneGas)
		})
	}
}

// Only the refund a foreign frame earned is excluded from the payment credit,
// not the whole refund counter. A refund the asset earned before the foreign
// frame opened must not be folded into the foreign delta, or the asset's own
// refund would be credited as well as paid back at settlement.
func TestPaymentLaneForeignRefundExcludesOnlyForeignDelta(t *testing.T) {
	t.Parallel()

	var (
		sender = common.HexToAddress("0x5e4de7")
		payee  = common.HexToAddress("0xbee")
		token  = common.HexToAddress("0x70ce0001")
		hook   = common.HexToAddress("0x3a11e704")
	)

	// Writes slot 9 then restores it: 0 -> 1 costs, 1 -> 0 refunds 19_900.
	setThenRestore := []byte{byte(PUSH1), 1, byte(PUSH1), 9, byte(SSTORE), byte(PUSH1), 0, byte(PUSH1), 9, byte(SSTORE)}

	// The asset logs its transfer, earns a refund, then calls a foreign hook
	// that earns the same refund again.
	assetCode := append(logTransfer(sender, payee, 1), setThenRestore...)
	assetCode = append(assetCode, callOp(nil, CALL, hook)...)
	assetCode = append(assetCode, byte(STOP))

	laneGas, frames, refund := laneFrameGas(t, token, token, map[common.Address][]byte{
		token: assetCode,
		hook:  append(append([]byte{}, setThenRestore...), byte(STOP)),
	}, nil)

	require.Equal(t, uint64(19_900*2), refund)
	// The asset's own refund (19_900) and the hook's whole frame are excluded.
	require.Equal(t, frames[token][0]-frames[hook][0]-19_900, laneGas)
}
