package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
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

// This file builds the executor-level differential against serial: the
// fuzzer drives both (a) the full V2 BlockSTM executor and (b) a
// straight-line serial application of the same txs, then asserts the
// resulting state roots are byte-identical.
//
// The motivation: TestV2Differential exercises only ParallelStateDB +
// SettleTo, never going through ExecuteV2BlockSTM. v2_executor_diff
// tests run the executor but with a synthetic env (no EVM, no real
// txs). TestV2BlockSTMAllBlocks covers everything but is slow and
// gated behind BOR_BLOCKSTM_TEST=1. This fuzz test fills the gap:
// real EVM, real signed txs, real executor, fast enough for CI.
//
// Inputs are decoded into a sequence of valid signed transactions
// over a fixed set of pre-funded sender keys. Each tx is one of:
//   transfer to a sender    — exercises balance delta read/write
//   transfer to a fresh adr — exercises EIP-161 touch / new-account
//   create contract         — exercises CodePath writes + EXTCODEHASH
//   call last contract      — exercises EXTCODEHASH + storage rw +
//                             cross-tx vfail/re-exec under conflict
//
// Between txs the V2 path runs through SettleTo on a finalDB; the
// serial path runs ApplyMessage + Finalise(true). Both finish with
// IntermediateRoot. Mismatch → fuzz failure (saved to testdata/fuzz/).

// numFuzzSenders is fixed so the encoder can stably interpret "sender
// index" as one byte. Five senders is enough to exercise per-sender
// nonce chaining and cross-sender independence without exploding the
// per-iteration cost of fuzzing.
const numFuzzSenders = 5

// fuzzGasLimit caps every tx so a malformed contract can't burn the
// fuzzer's wall-clock. 200k is enough for any contract scenario the
// generator produces; transfers consume 21k.
const fuzzGasLimit = 200_000

// fuzzMaxTxs caps txs per scenario so a long fuzz input doesn't blow
// out the per-iteration runtime — keeps each fuzz step fast.
const fuzzMaxTxs = 25

// fuzzKeys are deterministic per-run keys derived from a constant seed
// so failing fuzz inputs reproduce identically across runs.
var fuzzKeys = mustGenFuzzKeys(numFuzzSenders)

func mustGenFuzzKeys(n int) []*ecdsa.PrivateKey {
	out := make([]*ecdsa.PrivateKey, n)
	for i := 0; i < n; i++ {
		// Stable, deterministic key derivation from index — never use
		// these in production; they're test-only for byte-identical
		// repro across machines.
		seed := sha256.Sum256([]byte{0x42, byte(i)})
		k, err := crypto.ToECDSA(seed[:])
		if err != nil {
			panic(err)
		}
		out[i] = k
	}
	return out
}

// fuzzCoinbase is the block coinbase. It's deliberately separate from
// any sender so the fee-burn / fee-tip path involves balance changes
// to a non-sender address, exercising V2's settle-time fee plumbing.
var fuzzCoinbase = common.HexToAddress("0xCBCBCBCBCBCBCBCBCBCBCBCBCBCBCBCBCBCBCBCB")

// initialBalance per sender — generous enough that a 25-tx scenario
// can't bankrupt anyone via gas + value.
var initialBalance = new(uint256.Int).Mul(uint256.NewInt(100), uint256.NewInt(1e18))

// fuzzTxKind enumerates the tx shapes the generator produces.
type fuzzTxKind uint8

const (
	kindTransferToSender fuzzTxKind = iota
	kindTransferToFresh
	kindContractCreate
	kindContractCall
)

// fuzzTx is one tx in a decoded scenario.
type fuzzTx struct {
	kind         fuzzTxKind
	senderIdx    int    // index into fuzzKeys
	recipientIdx int    // for kindTransferToSender
	freshNonce   byte   // for kindTransferToFresh — derived from input bytes for determinism
	valueGwei    uint16 // small value so balances don't run out
	createKind   uint8  // for kindContractCreate — selects which canned bytecode
}

// canned contract bytecodes used by kindContractCreate. Each one is
// short and self-contained so the fuzzer can deploy any of them cheaply.
var fuzzCreateSnippets = [][]byte{
	// 0: empty constructor → returns nothing → contract has no code.
	{0x60, 0x00, 0x60, 0x00, 0xf3}, // PUSH1 0 PUSH1 0 RETURN
	// 1: SSTORE slot 0 = 1, then return empty code.
	{0x60, 0x01, 0x60, 0x00, 0x55, 0x60, 0x00, 0x60, 0x00, 0xf3},
	// 2: returns runtime code that does SSTORE slot 1 += 1 and STOP.
	// Constructor: copy the runtime code to memory, return it.
	// Runtime code: 60 01 60 01 54 01 60 01 55 00
	//               PUSH1 1 PUSH1 1 SLOAD ADD PUSH1 1 SSTORE STOP  (10 bytes)
	{
		0x60, 0x0a, // PUSH1 10 (length)
		0x60, 0x0c, // PUSH1 12 (offset = past constructor)
		0x60, 0x00, // PUSH1 0  (dest in memory)
		0x39,       // CODECOPY
		0x60, 0x0a, // PUSH1 10 (length)
		0x60, 0x00, // PUSH1 0
		0xf3, // RETURN
		// runtime code starts here:
		0x60, 0x01, 0x60, 0x01, 0x54, 0x01, 0x60, 0x01, 0x55, 0x00,
	},
}

// decodeScenario consumes the fuzzer's bytes and produces a sequence
// of valid txs. Validity (nonce ordering, balance) is the decoder's
// responsibility — failing txs are fine (both paths must reject the
// same way) but txs that exhaust a sender's balance bias the test.
func decodeScenario(data []byte) []fuzzTx {
	if len(data) < 5 {
		return nil
	}
	var out []fuzzTx
	i := 0
	for i+4 < len(data) && len(out) < fuzzMaxTxs {
		// Layout: [kind(1) | sender(1) | aux(1) | valueLo(1) | valueHi(1)]
		kind := fuzzTxKind(data[i] % 4)
		senderIdx := int(data[i+1]) % numFuzzSenders
		aux := data[i+2]
		valueGwei := uint16(data[i+3]) | (uint16(data[i+4]) << 8)
		// Cap value in gwei so cumulative spend per sender stays well
		// under initialBalance.
		valueGwei %= 4096

		tx := fuzzTx{
			kind:      kind,
			senderIdx: senderIdx,
			valueGwei: valueGwei,
		}
		switch kind {
		case kindTransferToSender:
			tx.recipientIdx = int(aux) % numFuzzSenders
		case kindTransferToFresh:
			tx.freshNonce = aux
		case kindContractCreate:
			tx.createKind = aux % uint8(len(fuzzCreateSnippets))
		case kindContractCall:
			// nothing extra; we'll resolve the target at apply time
		}
		out = append(out, tx)
		i += 5
	}
	return out
}

// scenarioState tracks per-sender nonces and the most-recently-created
// contract address so kindContractCall has a target.
type scenarioState struct {
	nonces      [numFuzzSenders]uint64
	lastCreated common.Address
	hasContract bool
}

// freshAddr derives a deterministic address from a single byte —
// reused across both serial and V2 runs so they target the same
// recipient.
func freshAddr(seed byte) common.Address {
	var a common.Address
	a[19] = seed
	a[18] = 0xa1
	return a
}

// signedTxs builds the signed transactions from the decoded scenario,
// using the per-sender nonce tracker. Returns parallel slices: the
// signed txs and their pre-recovered Messages.
func signedTxs(t testing.TB, decoded []fuzzTx, signer types.Signer, baseFee *big.Int) ([]*types.Transaction, []*Message) {
	t.Helper()
	state := scenarioState{}
	txs := make([]*types.Transaction, 0, len(decoded))
	msgs := make([]*Message, 0, len(decoded))

	for _, d := range decoded {
		nonce := state.nonces[d.senderIdx]
		state.nonces[d.senderIdx]++

		var to *common.Address
		var data []byte
		gas := uint64(21000)
		switch d.kind {
		case kindTransferToSender:
			a := crypto.PubkeyToAddress(fuzzKeys[d.recipientIdx].PublicKey)
			to = &a
		case kindTransferToFresh:
			a := freshAddr(d.freshNonce)
			to = &a
		case kindContractCreate:
			data = fuzzCreateSnippets[d.createKind]
			gas = fuzzGasLimit
		case kindContractCall:
			if !state.hasContract {
				// No contract deployed yet — fall back to a fresh transfer.
				a := freshAddr(d.freshNonce)
				to = &a
			} else {
				a := state.lastCreated
				to = &a
				gas = fuzzGasLimit
			}
		}

		// Compute value in wei from gwei; 0 is a legal value too.
		value := new(big.Int).Mul(big.NewInt(int64(d.valueGwei)), big.NewInt(1e9))
		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID:   params.TestChainConfig.ChainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(1),
			GasFeeCap: big.NewInt(1e9),
			Gas:       gas,
			To:        to,
			Value:     value,
			Data:      data,
		}), signer, fuzzKeys[d.senderIdx])
		if err != nil {
			t.Fatal(err)
		}
		// Track the contract address that this tx WOULD create — both
		// paths derive it the same way (CREATE = keccak(rlp(sender, nonce))).
		if d.kind == kindContractCreate {
			state.lastCreated = crypto.CreateAddress(crypto.PubkeyToAddress(fuzzKeys[d.senderIdx].PublicKey), nonce)
			state.hasContract = true
		}
		msg, err := TransactionToMessage(tx, signer, baseFee)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
		msgs = append(msgs, msg)
	}
	return txs, msgs
}

// buildBaseStateRoot creates an in-memory pre-state with every sender
// pre-funded, commits, and returns (db, root) so both serial and V2
// paths can re-open at the same root.
func buildBaseStateRoot(t testing.TB) (*triedb.Database, common.Hash) {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range fuzzKeys {
		addr := crypto.PubkeyToAddress(k.PublicKey)
		sdb.AddBalance(addr, initialBalance, 0)
	}
	root, err := sdb.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatal(err)
	}
	return tdb, root
}

// runSerial applies txs sequentially via ApplyMessage on a fresh
// StateDB at root. Between txs Finalise(true) so EIP-158 empty-account
// deletion lines up with V2's settle path. Returns the post-IntermediateRoot.
func runSerial(t testing.TB, tdb *triedb.Database, root common.Hash, txs []*types.Transaction, msgs []*Message, blockCtx vm.BlockContext) common.Hash {
	t.Helper()
	sdb, err := state.New(root, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	for i, tx := range txs {
		sdb.SetTxContext(tx.Hash(), i)
		evm := vm.NewEVM(blockCtx, sdb, params.TestChainConfig, vm.Config{})
		evm.SetTxContext(NewEVMTxContext(msgs[i]))
		// Failures are expected and accepted (e.g., out-of-gas) — V2
		// must reject them the same way. Either way the state changes
		// the message produced before failure must be reflected in
		// the journal+Finalise so the trie matches V2's settle path.
		_, _ = ApplyMessage(evm, msgs[i], new(GasPool).AddGas(blockCtx.GasLimit))
		sdb.Finalise(true)
	}
	return sdb.IntermediateRoot(true)
}

// runV2 runs txs through ExecuteV2BlockSTM with `workers` parallel
// workers, then returns the IntermediateRoot of finalDB. base and
// finalDB are independent StateDB instances at the same root (V2
// requires this — workers read from base, settlement writes to finalDB).
func runV2(t testing.TB, tdb *triedb.Database, root common.Hash, txs []*types.Transaction, msgs []*Message, blockCtx vm.BlockContext, workers int) common.Hash {
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
	_ = ExecuteV2BlockSTM(context.Background(), tasks, base, store, bals, blockCtx, common.Hash{}, vm.Config{},
		params.TestChainConfig, blockCtx.GasLimit, workers, finalDB, nil)

	return finalDB.IntermediateRoot(true)
}

// runScenarioAndAssertParity is the workhorse. It generates txs from
// the decoded scenario, runs both paths, and t.Fatals on root mismatch.
func runScenarioAndAssertParity(t testing.TB, decoded []fuzzTx, workers int) {
	if len(decoded) == 0 {
		return
	}
	tdb, root := buildBaseStateRoot(t)

	signer := types.NewLondonSigner(params.TestChainConfig.ChainID)
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
	}

	txs, msgs := signedTxs(t, decoded, signer, baseFee)
	if len(txs) == 0 {
		return
	}

	serialRoot := runSerial(t, tdb, root, txs, msgs, blockCtx)
	v2Root := runV2(t, tdb, root, txs, msgs, blockCtx, workers)

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
}

// FuzzV2ExecutorVsSerial drives the V2 BlockSTM executor and the serial
// state path with random tx sequences and asserts byte-identical
// state roots after each run. Failing inputs are persisted to
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
		// Mixed: every kind in one scenario.
		{
			byte(kindContractCreate), 0, 2, 0, 0,
			byte(kindTransferToSender), 1, 2, 7, 0,
			byte(kindContractCall), 2, 0, 0, 0,
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
		"OneTransfer": {{kindTransferToSender, 0, 1, 0, 100, 0}},
		"SenderChain": {
			{kindTransferToSender, 0, 1, 0, 50, 0},
			{kindTransferToSender, 0, 2, 0, 50, 0},
			{kindTransferToSender, 0, 3, 0, 50, 0},
		},
		"IndependentSenders": {
			{kindTransferToSender, 0, 1, 0, 10, 0},
			{kindTransferToSender, 1, 2, 0, 10, 0},
			{kindTransferToSender, 2, 3, 0, 10, 0},
			{kindTransferToSender, 3, 4, 0, 10, 0},
			{kindTransferToSender, 4, 0, 0, 10, 0},
		},
		"CreateThenCall": {
			{kindContractCreate, 0, 0, 0, 0, 1},
			{kindContractCall, 1, 0, 0, 0, 0},
			{kindContractCall, 2, 0, 0, 0, 0},
		},
		"FreshAddrTransfer": {
			{kindTransferToFresh, 0, 0, 0xab, 5, 0},
			{kindTransferToFresh, 1, 0, 0xcd, 7, 0},
		},
		"MixedKinds": {
			{kindContractCreate, 0, 0, 0, 0, 2},
			{kindTransferToSender, 1, 2, 0, 7, 0},
			{kindContractCall, 2, 0, 0, 0, 0},
			{kindTransferToFresh, 3, 0, 0xff, 1, 0},
			{kindTransferToSender, 4, 0, 0, 3, 0},
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
