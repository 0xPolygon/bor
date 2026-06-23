// Copyright 2024 The go-ethereum Authors
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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

var reservedBurntAddr = common.HexToAddress("0x00000000000000000000000000000000000000dd")

// reservedTestConfig clones BorUnittestChainConfig (London active at 0) and
// layers the reserved-blockspace fork + an optional reserved client on top,
// without mutating the shared global config.
func reservedTestConfig(forkBlock *big.Int, reservedSenders ...common.Address) *params.ChainConfig {
	cc := *params.BorUnittestChainConfig
	bor := *cc.Bor
	bor.BurntContract = map[string]string{"0": reservedBurntAddr.Hex()}
	bor.ReservedBlockspaceBlock = forkBlock
	if len(reservedSenders) > 0 {
		bor.ReservedClients = []params.ReservedClient{
			{Addresses: reservedSenders, QuotaGas: 30_000_000},
		}
	}
	cc.Bor = &bor
	return &cc
}

func reservedBlockCtx(coinbase common.Address, blockNumber *big.Int, baseFee *big.Int) vm.BlockContext {
	return vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(n uint64) common.Hash { return common.Hash{} },
		Coinbase:    coinbase,
		GasLimit:    30_000_000,
		BlockNumber: blockNumber,
		Time:        1,
		BaseFee:     baseFee,
	}
}

// fundedState returns an in-memory StateDB with `sender` funded and nonce 0.
func fundedState(t *testing.T, sender common.Address, balance *uint256.Int) *state.StateDB {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	sdb.AddBalance(sender, balance, 0)
	sdb.SetNonce(sender, 0, 0)
	return sdb
}

// TestReservedTxSkipsFees pins the core ticket-B behaviour: a reserved-sender
// tx executes fee-free past the fork — no gas debit, no base-fee floor, no
// producer tip, no burn. Only msg.Value moves.
func TestReservedTxSkipsFees(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	coinbase := common.HexToAddress("0x000000000000000000000000000000000000c0b0")
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")

	cc := reservedTestConfig(big.NewInt(0), sender)
	baseFee := big.NewInt(1_000_000_000)
	blockCtx := reservedBlockCtx(coinbase, big.NewInt(1), baseFee)

	initial := uint256.NewInt(1e18)
	sdb := fundedState(t, sender, initial)

	value := big.NewInt(5)
	signer := types.NewLondonSigner(cc.ChainID)
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   cc.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0), // below baseFee — would be ErrFeeCapTooLow if not reserved
		Gas:       21000,
		To:        &recipient,
		Value:     value,
	}), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := TransactionToMessage(tx, signer, baseFee)
	if err != nil {
		t.Fatal(err)
	}

	evm := vm.NewEVM(blockCtx, sdb, cc, vm.Config{})
	evm.SetTxContext(NewEVMTxContext(msg))
	result, err := ApplyMessage(evm, msg, new(GasPool).AddGas(blockCtx.GasLimit))
	if err != nil {
		t.Fatalf("reserved tx failed: %v", err)
	}
	if result.Failed() {
		t.Fatalf("reserved tx reverted: %v", result.Err)
	}

	// No fee was applied in-protocol.
	if result.FeeBurnt.Sign() != 0 {
		t.Errorf("FeeBurnt=%s, want 0", result.FeeBurnt)
	}
	if result.FeeTipped.Sign() != 0 {
		t.Errorf("FeeTipped=%s, want 0", result.FeeTipped)
	}
	if got := sdb.GetBalance(coinbase); !got.IsZero() {
		t.Errorf("coinbase tip=%s, want 0", got)
	}
	if got := sdb.GetBalance(reservedBurntAddr); !got.IsZero() {
		t.Errorf("burnt-contract balance=%s, want 0", got)
	}

	// Sender paid only value — no gas was debited.
	wantSender := new(uint256.Int).Sub(initial, uint256.MustFromBig(value))
	if got := sdb.GetBalance(sender); got.Cmp(wantSender) != 0 {
		t.Errorf("sender balance=%s, want %s (value only, no gas)", got, wantSender)
	}
	if got := sdb.GetBalance(recipient); got.Cmp(uint256.MustFromBig(value)) != 0 {
		t.Errorf("recipient balance=%s, want %s", got, value)
	}
	if result.UsedGas != 21000 {
		t.Errorf("UsedGas=%d, want 21000", result.UsedGas)
	}
}

// TestReservedTxGatedByFork verifies the fork gate: before the activation
// block a reserved sender gets no exemption, so a zero-fee tx is rejected
// with ErrFeeCapTooLow exactly as any other underpriced tx.
func TestReservedTxGatedByFork(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")

	cc := reservedTestConfig(big.NewInt(100), sender) // fork at 100
	baseFee := big.NewInt(1_000_000_000)
	blockCtx := reservedBlockCtx(common.HexToAddress("0xc0b0"), big.NewInt(1), baseFee) // pre-fork

	sdb := fundedState(t, sender, uint256.NewInt(1e18))
	signer := types.NewLondonSigner(cc.ChainID)
	tx, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   cc.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(1),
	}), signer, key)
	msg, _ := TransactionToMessage(tx, signer, baseFee)

	evm := vm.NewEVM(blockCtx, sdb, cc, vm.Config{})
	evm.SetTxContext(NewEVMTxContext(msg))
	_, err := ApplyMessage(evm, msg, new(GasPool).AddGas(blockCtx.GasLimit))
	if err == nil {
		t.Fatal("pre-fork zero-fee tx should be rejected, got nil error")
	}
}

// TestNonReservedSenderPaysNormally verifies the negative case: with the fork
// active but the sender NOT a reserved client, fees apply as usual (tip to
// coinbase, base fee burnt).
func TestNonReservedSenderPaysNormally(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	coinbase := common.HexToAddress("0x000000000000000000000000000000000000c0b0")
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Fork active, but the reserved client is a DIFFERENT address.
	reserved := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	cc := reservedTestConfig(big.NewInt(0), reserved)
	baseFee := big.NewInt(1_000_000_000)
	blockCtx := reservedBlockCtx(coinbase, big.NewInt(1), baseFee)

	sdb := fundedState(t, sender, uint256.NewInt(1e18))
	signer := types.NewLondonSigner(cc.ChainID)
	tx, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   cc.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(2_000_000_000),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(1),
	}), signer, key)
	msg, _ := TransactionToMessage(tx, signer, baseFee)

	evm := vm.NewEVM(blockCtx, sdb, cc, vm.Config{})
	evm.SetTxContext(NewEVMTxContext(msg))
	result, err := ApplyMessage(evm, msg, new(GasPool).AddGas(blockCtx.GasLimit))
	if err != nil {
		t.Fatalf("non-reserved tx failed: %v", err)
	}
	if result.FeeBurnt == nil || result.FeeBurnt.Sign() == 0 {
		t.Errorf("expected non-zero burn for non-reserved sender, got %v", result.FeeBurnt)
	}
	if got := sdb.GetBalance(coinbase); got.IsZero() {
		t.Error("expected non-zero coinbase tip for non-reserved sender")
	}
	if got := sdb.GetBalance(reservedBurntAddr); got.IsZero() {
		t.Error("expected non-zero burnt-contract balance for non-reserved sender")
	}
}

// TestReservedTxSerialParallelParity is the consensus-critical check: the
// reserved fee path must produce a byte-identical post-state whether the tx
// runs through the serial executor (ApplyMessage) or BlockSTM
// (ExecuteV2BlockSTM). Any divergence is a chain split.
func TestReservedTxSerialParallelParity(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	coinbase := common.HexToAddress("0x000000000000000000000000000000000000c0b0")
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")

	cc := reservedTestConfig(big.NewInt(0), sender)
	baseFee := big.NewInt(1_000_000_000)
	blockCtx := reservedBlockCtx(coinbase, big.NewInt(1), baseFee)

	// Committed base state shared by both paths.
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))
	sdb.AddBalance(sender, uint256.NewInt(1e18), 0)
	sdb.SetNonce(sender, 0, 0)
	root, _ := sdb.Commit(0, false, false)
	tdb.Commit(root, false)

	signer := types.NewLondonSigner(cc.ChainID)
	tx, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   cc.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(7),
	}), signer, key)
	msg, _ := TransactionToMessage(tx, signer, baseFee)

	// Serial path.
	serialDB, _ := state.New(root, state.NewDatabase(tdb, nil))
	evm := vm.NewEVM(blockCtx, serialDB, cc, vm.Config{})
	evm.SetTxContext(NewEVMTxContext(msg))
	if _, err := ApplyMessage(evm, msg, new(GasPool).AddGas(blockCtx.GasLimit)); err != nil {
		t.Fatalf("serial reserved tx failed: %v", err)
	}
	serialRoot := serialDB.IntermediateRoot(true)

	// Parallel (BlockSTM) path.
	base, _ := state.New(root, state.NewDatabase(tdb, nil))
	finalDB := base.Copy()
	finalDB.StartPrefetcher("test", nil, nil)
	defer finalDB.StopPrefetcher()
	tasks := []V2Task{{Index: 0, Tx: tx, Msg: msg}}
	result := ExecuteV2BlockSTM(context.Background(), tasks, base,
		blockstm.NewMVStore(), blockstm.NewMVBalanceStore(),
		blockCtx, common.Hash{}, vm.Config{}, cc, blockCtx.GasLimit, 1, finalDB, nil)
	if result.ExecErrIdx >= 0 {
		t.Fatalf("parallel reserved tx errored at %d: %v", result.ExecErrIdx, result.ExecErr)
	}
	if result.PanickedIdx >= 0 {
		t.Fatalf("parallel reserved tx panicked at %d", result.PanickedIdx)
	}
	parallelRoot := finalDB.IntermediateRoot(true)

	if serialRoot != parallelRoot {
		t.Fatalf("state root divergence: serial=%s parallel=%s", serialRoot.Hex(), parallelRoot.Hex())
	}

	// And the balances landed where expected on both.
	for _, db := range []*state.StateDB{serialDB, finalDB} {
		if got := db.GetBalance(coinbase); !got.IsZero() {
			t.Errorf("coinbase tip=%s, want 0", got)
		}
		if got := db.GetBalance(reservedBurntAddr); !got.IsZero() {
			t.Errorf("burnt balance=%s, want 0", got)
		}
		if got := db.GetBalance(recipient); got.Uint64() != 7 {
			t.Errorf("recipient=%s, want 7", got)
		}
	}
}
