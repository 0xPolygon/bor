package core

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// TestV2BalanceValidation verifies that speculative balance reads are caught
// by validation when a prior tx modifies the same sender's balance.
//
// Scenario:
//   - Sender has 1 ETH
//   - Tx 0 (nonce 0): sends 0.9 ETH to recipient (drains most of sender's balance)
//   - Tx 1 (nonce 1): sends 0.5 ETH to recipient (should fail — insufficient balance)
//
// In speculative parallel execution, tx 1 may read the sender's original 1 ETH
// balance (before tx 0's SubBalance delta is visible). This stale read causes
// CanTransfer to return true, and the transfer succeeds speculatively.
//
// Validation must catch this: the recorded balance delta (add=0, sub=0) no longer
// matches the real delta (add=0, sub=0.9ETH+gas) after tx 0 finishes. The
// validation failure triggers re-execution of tx 1 with the correct balance,
// where CanTransfer correctly returns false.
func TestV2BalanceValidation(t *testing.T) {
	t.Run("StaleReadCaught", testStaleBalanceReadCaught)
	t.Run("Executor", testExecutorBalanceValidation)
}

// testStaleBalanceReadCaught verifies that a speculative balance read by
// tx 1 is properly recorded, and that ValidateDetailed catches the
// staleness once tx 0 commits a delta on the same address.
//
// Production scenario this models: tx 1 reads contract X's balance for an
// EVM-level BALANCE opcode, then tx 0 commits a delta to X. Tx 1 must be
// re-executed because its balance read is no longer consistent.
//
// The previous version of this test skipped the FlushToMVStore call for
// tx 0 — without it, tx 0's writes never reached MVBalanceStore, so the
// validation re-read returned the same (0, 0) and the test concluded
// (incorrectly) that validation was broken.
func testStaleBalanceReadCaught(t *testing.T) {
	contract := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	oneEth := new(uint256.Int).Mul(uint256.NewInt(1), uint256.NewInt(1e18))

	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, _ := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	sdb.AddBalance(contract, oneEth, 0)
	root, _ := sdb.Commit(0, false, false)
	tdb.Commit(root, false)
	base, _ := state.New(root, state.NewDatabase(tdb, nil))

	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()
	sb := state.NewSafeBase(base, 0)

	// Tx 1 executes speculatively before tx 0. Reads contract balance
	// without writing to it first (so the read IS recorded for validation).
	pdb1 := state.NewParallelStateDB(1, sb, store, bals)
	pdb1.EnableReadTracking()
	pdb1.Coinbase = common.HexToAddress("0xCB")
	bal := pdb1.GetBalance(contract)
	if bal.Cmp(oneEth) != 0 {
		t.Fatalf("Speculative read: expected %s, got %s", oneEth.ToBig(), bal.ToBig())
	}

	// Tx 0 executes and flushes a delta on the same address.
	pdb0 := state.NewParallelStateDB(0, sb, store, bals)
	pdb0.EnableReadTracking()
	pdb0.Coinbase = common.HexToAddress("0xCB")
	half := new(uint256.Int).Div(oneEth, uint256.NewInt(2))
	pdb0.SubBalance(contract, half, 0)
	pdb0.FlushToMVStore()

	// Tx 1's recorded read (add=0, sub=0) no longer matches the current
	// state (add=0, sub=0.5 ETH). Validation must fail with FailKey="balance".
	res := pdb1.ValidateDetailed()
	if res.Valid {
		t.Fatal("tx 1 validation should FAIL — contract balance read is now stale")
	}
	if res.FailKey != "balance" {
		t.Fatalf("expected FailKey=balance, got %q", res.FailKey)
	}
	t.Logf("validation correctly caught stale balance read: %s", res.FailKey)
}

// testExecutorBalanceValidation runs the full BlockSTM executor with two txs
// from the same sender where the second tx cannot afford the transfer after
// the first. Verifies end-to-end correctness regardless of execution order.
func testExecutorBalanceValidation(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Sender has 1 ETH
	oneEth := new(uint256.Int).Mul(uint256.NewInt(1), uint256.NewInt(1e18))
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, _ := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	sdb.AddBalance(sender, oneEth, 0)
	sdb.SetNonce(sender, 0, 0)
	root, _ := sdb.Commit(0, false, false)
	tdb.Commit(root, false)
	base, _ := state.New(root, state.NewDatabase(tdb, nil))

	chainConfig := params.TestChainConfig
	baseFee := big.NewInt(875000000) // 0.875 gwei
	signer := types.NewLondonSigner(chainConfig.ChainID)

	// Tx 0: sender sends 0.9 ETH (nonce 0)
	tx0, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainConfig.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1e9),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(9e17), // 0.9 ETH
	}), signer, key)

	// Tx 1: sender sends 0.5 ETH (nonce 1) — should fail value transfer
	tx1, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainConfig.ChainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1e9),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(5e17), // 0.5 ETH
	}), signer, key)

	msg0, _ := TransactionToMessage(tx0, signer, baseFee)
	msg1, _ := TransactionToMessage(tx1, signer, baseFee)

	tasks := []V2Task{
		{Index: 0, Tx: tx0, Msg: msg0},
		{Index: 1, Tx: tx1, Msg: msg1},
	}

	coinbase := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(n uint64) common.Hash { return common.Hash{} },
		Coinbase:    coinbase,
		GasLimit:    10000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     baseFee,
	}

	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()

	result := ExecuteV2BlockSTM(context.Background(), tasks, base, store, bals, blockCtx, common.Hash{}, vm.Config{}, chainConfig, 10000000, 2, nil, nil)

	t.Logf("Execution: execs=%d vfails=%d", result.ExecCount, result.VFailCount)

	// Both pdbs should exist
	for i, pdb := range result.Pdbs {
		if pdb == nil {
			t.Fatalf("tx %d: pdb is nil", i)
		}
		t.Logf("tx %d: %d balance ops", i, len(pdb.BalanceOps))
	}

	// Settle and verify final state
	finalDB := base.Copy()
	for _, pdb := range result.Pdbs {
		finalDB.SetTxContext(common.Hash{}, pdb.TxIndex)
		pdb.SettleTo(finalDB)
	}

	recipientFinal := finalDB.GetBalance(recipient)
	senderFinal := finalDB.GetBalance(sender)

	t.Logf("Final balances:")
	t.Logf("  sender:    %s", senderFinal.ToBig().String())
	t.Logf("  recipient: %s", recipientFinal.ToBig().String())

	// Recipient should have EXACTLY 0.9 ETH (only tx 0's transfer succeeds).
	// Tx 1's transfer of 0.5 ETH fails because sender's balance is too low.
	expectedRecipient := new(uint256.Int).Mul(uint256.NewInt(9), uint256.NewInt(1e17))
	if recipientFinal.Cmp(expectedRecipient) != 0 {
		t.Errorf("recipient balance = %s, expected %s (0.9 ETH)",
			recipientFinal.ToBig(), expectedRecipient.ToBig())
	}

	// Sender's final balance = 1 ETH - 0.9 ETH - gas(tx0)
	// Tx 1 fails entirely in buyGas (balance < gasLimit*gasFeeCap + value),
	// so it consumes NO gas and makes no state changes.
	gasPerTx := new(uint256.Int).Mul(uint256.NewInt(21000), uint256.NewInt(875000001))
	expectedSender := new(uint256.Int).Set(oneEth)
	expectedSender.Sub(expectedSender, new(uint256.Int).Mul(uint256.NewInt(9), uint256.NewInt(1e17))) // - 0.9 ETH
	expectedSender.Sub(expectedSender, gasPerTx)                                                      // - gas(tx0) only

	if senderFinal.Cmp(expectedSender) != 0 {
		t.Errorf("sender balance = %s, expected %s",
			senderFinal.ToBig(), expectedSender.ToBig())
	}

	// Verify no uint256 underflow (balance should be reasonable, not huge)
	if senderFinal.Cmp(oneEth) > 0 {
		t.Errorf("sender balance overflow: %s", senderFinal.ToBig())
	}
}

// TestV2GasDeterminism verifies that V2 parallel execution produces
// identical gas across multiple runs. With DeferMVWrites=true, intermediate
// values are never visible, so gas is deterministic regardless of scheduling.
func TestV2GasDeterminism(t *testing.T) {
	blocks, diskdb := loadEmbeddedBlocks(t)
	if len(blocks) == 0 {
		t.Skip("no embedded blocks available")
	}

	config := params.BorMainnetChainConfig
	engine := &benchConsensus{}

	// Pick a block with enough txs
	var bd testBlockData
	for _, b := range blocks {
		if len(b.block.Transactions()) > 50 {
			bd = b
			break
		}
	}
	if bd.block == nil {
		t.Skip("no block with >50 txs")
	}

	author := getAuthor(config, bd.witness.Header())
	var expectedGas uint64
	for run := 0; run < 5; run++ {
		memdb := bd.witness.MakeHashDB(diskdb)
		sdb, err := state.New(bd.witness.Root(), state.NewDatabase(triedb.NewDatabase(memdb, triedb.HashDefaults), nil))
		if err != nil {
			t.Fatal(err)
		}
		hc := &benchHeaderChain{config: config, chainDb: memdb,
			headerCache: lru.NewCache[common.Hash, *types.Header](256), engine: engine}
		bc := &BlockChain{hc: &HeaderChain{config: config, chainDb: memdb,
			headerCache: lru.NewCache[common.Hash, *types.Header](256), engine: engine}}

		res, err := NewV2StateProcessor(hc, bc, 16).Process(bd.block, sdb, vm.Config{}, &author, context.Background())
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if run == 0 {
			expectedGas = res.GasUsed
		} else if res.GasUsed != expectedGas {
			t.Errorf("run %d: gas %d != expected %d (non-deterministic!)", run, res.GasUsed, expectedGas)
		}
	}
}
