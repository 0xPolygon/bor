package core

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

var (
	densityLoop     = common.HexToAddress("0x000000000000000000000000000000000000aaaa")
	densityAbsent   = common.HexToAddress("0x000000000000000000000000000000000000dead")
	densityStop     = common.HexToAddress("0x000000000000000000000000000000000000c0de")
	densityDelegate = common.HexToAddress("0x0000000000000000000000000000000000007702")
	densityIdentity = common.BytesToAddress([]byte{4})
)

const (
	densityTxGas    = 3_000_000
	densityUnroll   = 16
	densityGasFloor = 200_000
)

// densityCode runs prefix once, then repeats body until less than
// densityGasFloor gas is left, and stops.
func densityCode(prefix, body []byte) []byte {
	p := program.New().Append(prefix)
	_, loop := p.Jumpdest()
	for i := 0; i < densityUnroll; i++ {
		p.Append(body)
	}
	p.Push(densityGasFloor).Op(vm.GAS, vm.GT).Push(loop).Op(vm.JUMPI, vm.STOP)
	return p.Bytes()
}

func ops(o ...vm.OpCode) []byte { return program.New().Op(o...).Bytes() }

func callBody(op vm.OpCode, to common.Address, args int) []byte {
	p := program.New()
	for i := 0; i < args; i++ {
		p.Push0()
	}
	return p.Push(to).Op(vm.GAS, op, vm.POP).Bytes()
}

func addrBody(op vm.OpCode, to common.Address) []byte {
	return program.New().Push(to).Op(op, vm.POP).Bytes()
}

// refundPrefix sets and clears fresh slots until the refund reaches the
// EIP-3529 cap, so the header's gas used is a fifth below the gas executed.
func refundPrefix() []byte {
	p := program.New()
	for slot := 100; slot < 100+densityTxGas/5/19_900+1; slot++ {
		p.Sstore(slot, 1).Sstore(slot, 0)
	}
	return p.Bytes()
}

type densityCase struct {
	name      string
	prefix    []byte
	body      []byte
	preimages bool
}

func densityCases() []densityCase {
	mem32k := program.New().Push0().Push(0x8000 - 32).Op(vm.MSTORE).Bytes()
	return []densityCase{
		{name: "STATICCALL absent", body: callBody(vm.STATICCALL, densityAbsent, 4)},
		{name: "STATICCALL absent with max refund", prefix: refundPrefix(), body: callBody(vm.STATICCALL, densityAbsent, 4)},
		{name: "STATICCALL code", body: callBody(vm.STATICCALL, densityStop, 4)},
		{name: "STATICCALL delegated", body: callBody(vm.STATICCALL, densityDelegate, 4)},
		{name: "STATICCALL precompile", body: callBody(vm.STATICCALL, densityIdentity, 4)},
		{name: "CALL absent", body: callBody(vm.CALL, densityAbsent, 5)},
		{name: "CALL code", body: callBody(vm.CALL, densityStop, 5)},
		{name: "CALL precompile", body: callBody(vm.CALL, densityIdentity, 5)},
		{name: "CALLCODE absent", body: callBody(vm.CALLCODE, densityAbsent, 5)},
		{name: "DELEGATECALL absent", body: callBody(vm.DELEGATECALL, densityAbsent, 4)},
		{name: "BALANCE", body: addrBody(vm.BALANCE, densityAbsent)},
		{name: "SELFBALANCE", body: ops(vm.SELFBALANCE, vm.POP)},
		{name: "EXTCODESIZE", body: addrBody(vm.EXTCODESIZE, densityStop)},
		{name: "EXTCODEHASH absent", body: addrBody(vm.EXTCODEHASH, densityAbsent)},
		{name: "EXTCODEHASH code", body: addrBody(vm.EXTCODEHASH, densityStop)},
		{name: "EXTCODECOPY", body: program.New().Push0().Push0().Push0().Push(densityStop).Op(vm.EXTCODECOPY).Bytes()},
		{name: "SLOAD", body: ops(vm.PUSH0, vm.SLOAD, vm.POP)},
		{name: "SSTORE unchanged", body: ops(vm.PUSH0, vm.PUSH0, vm.SSTORE)},
		{name: "SSTORE dirty refund toggle", prefix: program.New().Sstore(1, 2).Bytes(),
			body: program.New().Push0().Push(1).Op(vm.SSTORE).Push(2).Push(1).Op(vm.SSTORE).Bytes()},
		{name: "TSTORE toggle", body: program.New().Push(1).Push0().Op(vm.TSTORE, vm.PUSH0, vm.PUSH0, vm.TSTORE).Bytes()},
		{name: "LOG0 empty", body: ops(vm.PUSH0, vm.PUSH0, vm.LOG0)},
		{name: "LOG0 1KiB", body: program.New().Push(1024).Push0().Op(vm.LOG0).Bytes()},
		{name: "KECCAK256 1 word", preimages: true,
			body: program.New().Op(vm.GAS, vm.PUSH0, vm.MSTORE).Push(32).Push0().Op(vm.KECCAK256, vm.POP).Bytes()},
		{name: "KECCAK256 32KiB", preimages: true, prefix: mem32k,
			body: program.New().Op(vm.GAS, vm.PUSH0, vm.MSTORE).Push(0x8000).Push0().Op(vm.KECCAK256, vm.POP).Bytes()},
	}
}

// measureWriteDensity runs one canonical V2 incarnation of a tx calling
// code and returns the budget entries it charged and the gas it used.
func measureWriteDensity(t *testing.T, code []byte, preimages bool) (int64, uint64) {
	t.Helper()
	cfg := *params.MergedTestChainConfig
	key, _ := crypto.GenerateKey()

	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), triedb.HashDefaults)
	gen, _ := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	gen.AddBalance(crypto.PubkeyToAddress(key.PublicKey), uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	gen.SetCode(densityLoop, code, tracing.CodeChangeUnspecified)
	gen.SetState(densityLoop, common.BigToHash(common.Big1), common.BigToHash(common.Big1))
	gen.SetCode(densityStop, []byte{byte(vm.STOP)}, tracing.CodeChangeUnspecified)
	gen.SetCode(densityDelegate, types.AddressToDelegation(densityStop), tracing.CodeChangeUnspecified)
	root, _ := gen.Commit(0, false, false)
	tdb.Commit(root, false)

	signer := types.NewLondonSigner(cfg.ChainID)
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID: cfg.ChainID, GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(7), Gas: densityTxGas, To: &densityLoop,
	}), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    common.HexToAddress("0xc0ffee"),
		GasLimit:    30_000_000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     big.NewInt(7),
		Random:      &common.Hash{},
	}
	msg, err := TransactionToMessage(tx, signer, blockCtx.BaseFee)
	if err != nil {
		t.Fatal(err)
	}

	finalDB, _ := state.New(root, state.NewDatabase(tdb, nil))
	base := finalDB.Copy()
	const limit = 1 << 40
	budget := blockstm.NewWriteBudget(limit, func() {})
	result := ExecuteV2BlockSTM(context.Background(), []V2Task{{Index: 0, Tx: tx, Msg: msg}}, base,
		blockstm.NewMVStore(), blockstm.NewMVBalanceStore(), blockCtx, common.Hash{},
		vm.Config{EnablePreimageRecording: preimages}, &cfg, blockCtx.GasLimit, 1, finalDB, nil, budget)
	if len(result.Receipts) != 1 || result.Receipts[0].Status != types.ReceiptStatusSuccessful {
		t.Fatalf("tx did not succeed: %d receipts", len(result.Receipts))
	}
	return limit - budget.Remaining(), result.GasUsed
}

// Canonical execution of any valid block must charge at most a third of
// v2WriteLimit, so the densest op a tx can repeat sets the limit.
func TestV2WriteDensityStaysUnderLimit(t *testing.T) {
	for _, tc := range densityCases() {
		t.Run(tc.name, func(t *testing.T) {
			entries, gasUsed := measureWriteDensity(t, densityCode(tc.prefix, tc.body), tc.preimages)
			if gasUsed < densityTxGas/2 {
				t.Fatalf("loop used only %d gas", gasUsed)
			}
			t.Logf("%d entries / %d gas = 1 per %.1f gas", entries, gasUsed, float64(gasUsed)/float64(entries))
			if bound := v2WriteLimit(gasUsed) / 3; entries > bound {
				t.Fatalf("canonical execution charged %d entries, over a third of the limit (%d)", entries, bound)
			}
		})
	}
}
