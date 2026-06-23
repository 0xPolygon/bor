package core

import (
	"bytes"
	"context"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// This file builds the executor-level differential against serial: the
// fuzzer drives both (a) the full V2 BlockSTM executor and (b) the
// production serial path (ApplyTransactionWithEVM) over the same txs,
// then asserts every consensus output matches: state root, receipts
// root, and per-tx status / cumulative gas / bloom / logs. Comparing
// roots alone is not enough — the settlement log mispairing fixed on
// this branch left state roots identical and corrupted only receipts.
//
// The motivation: TestV2Differential exercises only ParallelStateDB +
// SettleTo, never going through ExecuteV2BlockSTM. v2_executor_diff
// tests run the executor but with a synthetic env (no EVM, no real
// txs). TestV2BlockSTMAllBlocks covers everything but is slow and
// gated behind BOR_BLOCKSTM_TEST=1. This fuzz test fills the gap:
// real EVM, real signed txs, real executor, fast enough for CI.
//
// Inputs are decoded into a sequence of valid signed transactions
// over a fixed set of pre-funded sender keys (see
// v2_serial_parity_gen_test.go for the generator). Each tx is one of:
//   transfer to a sender    — exercises balance delta read/write
//   transfer to a fresh adr — exercises EIP-161 touch / new-account
//   create contract         — exercises CodePath writes + EXTCODEHASH
//   call last contract      — exercises EXTCODEHASH + storage rw +
//                             cross-tx vfail/re-exec under conflict
//   call selfdestruct pair  — exercises EIP-6780 payouts whose balance
//                             ops shape-match recorded transfers
//   7702 authorization      — exercises delegation set/clear, nonce-only
//                             account existence, delegated code reads
//
// Between txs the V2 path settles through SettleTo on a finalDB; the
// serial path runs ApplyTransactionWithEVM + Finalise(true). Mismatch
// in any consensus output → fuzz failure (saved to testdata/fuzz/).

// fuzzChainConfig enables every supported fork (incl. Prague/EIP-7702)
// so the vocabulary above is all live. Bor is nil here, so bor fee
// transfer logs are skipped identically on both paths; the 0x1010
// LogTransfer for value transfers is emitted unconditionally by
// core.Transfer and is part of the compared receipts.
var fuzzChainConfig = params.MergedTestChainConfig

// runSerial applies txs sequentially via ApplyTransactionWithEVM (the
// production serial path — it finalises per tx and emits receipts) on
// a fresh StateDB at root. Returns the post-IntermediateRoot and the
// per-tx receipts.
func runSerial(t testing.TB, tdb *triedb.Database, root common.Hash, txs []*types.Transaction, msgs []*Message, blockCtx vm.BlockContext) (common.Hash, types.Receipts) {
	t.Helper()
	sdb, err := state.New(root, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	gp := new(GasPool).AddGas(blockCtx.GasLimit)
	var usedGas uint64
	receipts := make(types.Receipts, 0, len(txs))
	for i, tx := range txs {
		sdb.SetTxContext(tx.Hash(), i)
		evm := vm.NewEVM(blockCtx, sdb, fuzzChainConfig, vm.Config{})
		// The generator only emits consensus-valid txs (tracked nonces,
		// bounded values against huge balances), so an apply error is a
		// generator bug, not an accepted outcome. EVM-level failures
		// (revert, OOG) still produce a status-failed receipt.
		receipt, err := ApplyTransactionWithEVM(msgs[i], gp, sdb, blockCtx.BlockNumber, common.Hash{}, blockCtx.Time, tx, &usedGas, evm)
		if err != nil {
			t.Fatalf("serial apply tx %d: %v", i, err)
		}
		receipts = append(receipts, receipt)
	}
	return sdb.IntermediateRoot(true), receipts
}

// runV2 runs txs through ExecuteV2BlockSTM with `workers` parallel
// workers, then returns the IntermediateRoot of finalDB and the
// settlement-built receipts. base and finalDB are independent StateDB
// instances at the same root (V2 requires this — workers read from
// base, settlement writes to finalDB).
func runV2(t testing.TB, tdb *triedb.Database, root common.Hash, txs []*types.Transaction, msgs []*Message, blockCtx vm.BlockContext, workers int) (common.Hash, types.Receipts) {
	t.Helper()
	base, err := state.New(root, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	finalDB, err := state.New(root, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}

	tasks := make([]V2Task, len(txs))
	for i := range txs {
		tasks[i] = V2Task{Index: i, Tx: txs[i], Msg: msgs[i]}
	}

	store := blockstm.NewMVStore()
	bals := blockstm.NewMVBalanceStore()
	res := ExecuteV2BlockSTM(context.Background(), tasks, base, store, bals, blockCtx, common.Hash{}, vm.Config{},
		fuzzChainConfig, blockCtx.GasLimit, workers, finalDB, nil)
	if res.PanickedIdx >= 0 {
		t.Fatalf("V2 executor: tx %d panicked", res.PanickedIdx)
	}
	if res.ExecErrIdx >= 0 {
		t.Fatalf("V2 executor: tx %d consensus error: %v", res.ExecErrIdx, res.ExecErr)
	}

	return finalDB.IntermediateRoot(true), res.Receipts
}

// assertReceiptParity compares every consensus field of the two receipt
// sets, then the receipts trie root itself — exactly the header field a
// validator recomputes. Per-field errors first so a divergence
// pinpoints the tx and field instead of just two root hashes.
func assertReceiptParity(t testing.TB, serial, v2 types.Receipts, workers int, decoded []fuzzTx) {
	t.Helper()
	if len(serial) != len(v2) {
		t.Fatalf("receipt count mismatch: serial=%d v2=%d (workers=%d)", len(serial), len(v2), workers)
	}
	for i := range serial {
		s, p := serial[i], v2[i]
		if s.Status != p.Status {
			t.Errorf("tx %d: status serial=%d v2=%d", i, s.Status, p.Status)
		}
		if s.CumulativeGasUsed != p.CumulativeGasUsed {
			t.Errorf("tx %d: cumulative gas serial=%d v2=%d (tx gas serial=%d v2=%d)",
				i, s.CumulativeGasUsed, p.CumulativeGasUsed, s.GasUsed, p.GasUsed)
		}
		if s.Bloom != p.Bloom {
			t.Errorf("tx %d: bloom mismatch", i)
		}
		if len(s.Logs) != len(p.Logs) {
			t.Errorf("tx %d: log count serial=%d v2=%d", i, len(s.Logs), len(p.Logs))
			continue
		}
		for j := range s.Logs {
			sl, pl := s.Logs[j], p.Logs[j]
			if sl.Address != pl.Address || !slices.Equal(sl.Topics, pl.Topics) || !bytes.Equal(sl.Data, pl.Data) {
				t.Errorf("tx %d log %d:\n  serial = addr=%s topics=%v data=%x\n  v2     = addr=%s topics=%v data=%x",
					i, j, sl.Address, sl.Topics, sl.Data, pl.Address, pl.Topics, pl.Data)
			}
		}
	}
	sRoot := types.DeriveSha(serial, trie.NewStackTrie(nil))
	pRoot := types.DeriveSha(v2, trie.NewStackTrie(nil))
	if sRoot != pRoot {
		t.Fatalf(`receipts-root divergence between serial and V2 executor:
  serial = %s
  v2     = %s
  workers= %d
First decoded txs:
  %v`,
			sRoot.Hex(), pRoot.Hex(), workers, decoded[:min(len(decoded), 4)])
	}
}

// runScenarioAndAssertParity is the workhorse. It generates txs from
// the decoded scenario, runs both paths, and fails on any consensus
// output mismatch.
func runScenarioAndAssertParity(t testing.TB, decoded []fuzzTx, workers int) {
	if len(decoded) == 0 {
		return
	}
	tdb, root := buildBaseStateRoot(t)

	signer := types.LatestSigner(fuzzChainConfig)
	baseFee := big.NewInt(1)
	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    fuzzCoinbase,
		GasLimit:    30_000_000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     baseFee,
		Random:      &common.Hash{}, // post-merge rules (PREVRANDAO)
	}

	txs, msgs := signedTxs(t, decoded, signer, baseFee)
	if len(txs) == 0 {
		return
	}

	serialRoot, serialReceipts := runSerial(t, tdb, root, txs, msgs, blockCtx)
	v2Root, v2Receipts := runV2(t, tdb, root, txs, msgs, blockCtx, workers)

	if serialRoot != v2Root {
		t.Fatalf(`state-root divergence between serial and V2 executor:
  serial = %s
  v2     = %s
  workers= %d
  txs    = %d
First decoded txs:
  %v
Re-run a single iteration with: go test -run FuzzV2ExecutorVsSerial/<corpus-id>`,
			serialRoot.Hex(), v2Root.Hex(), workers, len(txs), decoded[:min(len(decoded), 4)])
	}
	assertReceiptParity(t, serialReceipts, v2Receipts, workers, decoded)
}

// FuzzV2ExecutorVsSerial drives the V2 BlockSTM executor and the serial
// state path with random tx sequences and asserts byte-identical
// consensus outputs after each run. Failing inputs are persisted to
// testdata/fuzz/FuzzV2ExecutorVsSerial/ for replay.
//
// Run with `go test -fuzz=FuzzV2ExecutorVsSerial ./core/`.
// Without -fuzz, the seed corpus runs as a regular test in <1s.
func FuzzV2ExecutorVsSerial(f *testing.F) {
	// Seed corpus — covers each tx kind plus a multi-sender mix.
	seeds := [][]byte{
		// One transfer to sender 1 from sender 0.
		{byte(kindTransferToSender), 0, 1, 100, 0, 0, 0, 0, 0, 0},
		// Three same-sender txs (chain).
		{
			byte(kindTransferToSender), 0, 1, 50, 0,
			byte(kindTransferToSender), 0, 2, 50, 0,
			byte(kindTransferToSender), 0, 3, 50, 0,
		},
		// Independent transfers from different senders.
		{
			byte(kindTransferToSender), 0, 1, 10, 0,
			byte(kindTransferToSender), 1, 2, 10, 0,
			byte(kindTransferToSender), 2, 3, 10, 0,
		},
		// Contract create + call (exercises EXTCODEHASH on freshly
		// deployed contract — the regression class Fix #1 closed).
		{
			byte(kindContractCreate), 0, 1, 0, 0,
			byte(kindContractCall), 1, 0, 0, 0,
			byte(kindContractCall), 2, 0, 0, 0,
		},
		// Transfer to fresh address (EIP-161 path).
		{byte(kindTransferToFresh), 0, 0xab, 5, 0},
		// Zero-value call into the predeployed selfdestruct pair: the
		// EIP-6780 payout (X→Y, 10 wei) shape-matches the tx's later
		// recorded transfer (X→Y, 10 wei) — the settlement mispairing
		// shape from PR #2268. State roots match; only receipts diverge.
		{byte(kindCallSelfDestructPair), 0, 0, 0, 0},
		// Repeated delegation-clears for the same authority (Exist via
		// CreatePath + nonce churn on the refund gate).
		{
			byte(kind7702Auth), 0, 0, 0, 0,
			byte(kind7702Auth), 1, 0, 0, 0,
		},
		// Fund → self-drain-to-zero → authorize: the authority is a
		// nonce-only account when its authorization is validated, so the
		// 12500 refund hinges on Exist seeing the nonce — the EIP-7702
		// over-charge shape from the mainnet bad blocks.
		{byte(kindNonceOnlyAccount), 0, 0, 0, 0},
		// Delegate two authorities to the selfdestruct-pair entry (their
		// calls run X's bounce-and-payout code as the authority), then
		// clear one — 7702 churn mixed with EIP-6780 payouts.
		{
			byte(kind7702Auth), 0, 0x80, 0, 0,
			byte(kind7702Auth), 1, 0x81, 0, 0,
			byte(kind7702Auth), 2, 1, 0, 0,
		},
		// Mixed: every kind in one scenario.
		{
			byte(kindContractCreate), 0, 2, 0, 0,
			byte(kindTransferToSender), 1, 2, 7, 0,
			byte(kindContractCall), 2, 0, 0, 0,
			byte(kindCallSelfDestructPair), 3, 0, 0, 0,
			byte(kind7702Auth), 4, 0x80, 0, 0,
			byte(kindTransferToFresh), 3, 0xff, 1, 0,
			byte(kindTransferToSender), 4, 0, 3, 0,
		},
	}
	for _, s := range seeds {
		f.Add(s, uint8(4)) // 4 workers
		f.Add(s, uint8(1)) // serial-mode V2 (single worker)
		f.Add(s, uint8(8)) // 8 workers
	}

	f.Fuzz(func(t *testing.T, raw []byte, workers uint8) {
		w := int(workers%8) + 1 // 1..8 workers
		decoded := decodeScenario(raw)
		if len(decoded) == 0 {
			return
		}
		runScenarioAndAssertParity(t, decoded, w)
	})
}

// TestV2ExecutorVsSerial_SeedCorpus runs every seed in the fuzz corpus
// as a regular unit test so CI catches regressions without needing
// `-fuzz`. Each subtest runs with a small worker count grid.
func TestV2ExecutorVsSerial_SeedCorpus(t *testing.T) {
	cases := map[string][]fuzzTx{
		"OneTransfer": {{kind: kindTransferToSender, senderIdx: 0, recipientIdx: 1, valueGwei: 100}},
		"SenderChain": {
			{kind: kindTransferToSender, senderIdx: 0, recipientIdx: 1, valueGwei: 50},
			{kind: kindTransferToSender, senderIdx: 0, recipientIdx: 2, valueGwei: 50},
			{kind: kindTransferToSender, senderIdx: 0, recipientIdx: 3, valueGwei: 50},
		},
		"IndependentSenders": {
			{kind: kindTransferToSender, senderIdx: 0, recipientIdx: 1, valueGwei: 10},
			{kind: kindTransferToSender, senderIdx: 1, recipientIdx: 2, valueGwei: 10},
			{kind: kindTransferToSender, senderIdx: 2, recipientIdx: 3, valueGwei: 10},
			{kind: kindTransferToSender, senderIdx: 3, recipientIdx: 4, valueGwei: 10},
			{kind: kindTransferToSender, senderIdx: 4, recipientIdx: 0, valueGwei: 10},
		},
		"CreateThenCall": {
			{kind: kindContractCreate, senderIdx: 0, createKind: 1},
			{kind: kindContractCall, senderIdx: 1},
			{kind: kindContractCall, senderIdx: 2},
		},
		"FreshAddrTransfer": {
			{kind: kindTransferToFresh, senderIdx: 0, freshNonce: 0xab, valueGwei: 5},
			{kind: kindTransferToFresh, senderIdx: 1, freshNonce: 0xcd, valueGwei: 7},
		},
		// The PR #2268 settlement mispairing shape — state roots equal,
		// receipts diverge without the BalanceOpsIdx pairing fix.
		"SelfDestructTransferPair": {
			{kind: kindCallSelfDestructPair, senderIdx: 0},
		},
		"SelfDestructPairWithValue": {
			{kind: kindCallSelfDestructPair, senderIdx: 0, valueGwei: 3},
			{kind: kindCallSelfDestructPair, senderIdx: 1},
		},
		"Auth7702RepeatedClears": {
			{kind: kind7702Auth, senderIdx: 0, authSel: 0},
			{kind: kind7702Auth, senderIdx: 1, authSel: 0},
		},
		// The Exist nonce fix's shape: fund the authority, let it drain
		// itself to exactly zero (account = nonce-only), then validate an
		// authorization for it — the 12500 refund hinges on Exist seeing
		// the nonce.
		"NonceOnlyAuthorityRefund": {
			{kind: kindNonceOnlyAccount, senderIdx: 0, authSel: 0},
		},
		"NonceOnlyAuthorityDelegate": {
			{kind: kindNonceOnlyAccount, senderIdx: 1, authSel: 0x81},
		},
		"Auth7702DelegateCallClear": {
			{kind: kind7702Auth, senderIdx: 0, authSel: 0x80},
			{kind: kind7702Auth, senderIdx: 1, authSel: 0x81},
			{kind: kind7702Auth, senderIdx: 2, authSel: 1},
		},
		"MixedKinds": {
			{kind: kindContractCreate, senderIdx: 0, createKind: 2},
			{kind: kindTransferToSender, senderIdx: 1, recipientIdx: 2, valueGwei: 7},
			{kind: kindContractCall, senderIdx: 2},
			{kind: kindCallSelfDestructPair, senderIdx: 3},
			{kind: kind7702Auth, senderIdx: 4, authSel: 0x80},
			{kind: kindTransferToFresh, senderIdx: 3, freshNonce: 0xff, valueGwei: 1},
			{kind: kindTransferToSender, senderIdx: 4, recipientIdx: 0, valueGwei: 3},
		},
	}
	workerGrid := []int{1, 4, 8}
	for name, decoded := range cases {
		for _, w := range workerGrid {
			t.Run(name+"/w"+string(rune('0'+w)), func(t *testing.T) {
				runScenarioAndAssertParity(t, decoded, w)
			})
		}
	}
}
