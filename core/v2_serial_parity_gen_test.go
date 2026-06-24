package core

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb"
)

// This file holds the scenario generator for the serial-vs-V2 parity
// fuzzer (see v2_serial_parity_fuzz_test.go): tx vocabulary, key
// derivation, scenario decoding, signing, and the pre-state builder.
//
// The vocabulary must track the fork schedule: when a fork adds a tx
// type or changes account-mutating behavior, add a kind here in the
// same PR. The three V2 divergences fixed on this branch (settlement
// log mispairing, Exist missing nonce-only accounts, code-read
// snapshot inconsistency) all lived in surface the old vocabulary —
// transfers, creates, storage calls — could not reach: selfdestruct
// payouts and EIP-7702 authorizations.

// numFuzzSenders is fixed so the encoder can stably interpret "sender
// index" as one byte. Five senders is enough to exercise per-sender
// nonce chaining and cross-sender independence without exploding the
// per-iteration cost of fuzzing.
const numFuzzSenders = 5

// numFuzzAuthorities is the pool of EIP-7702 authority keys. They are
// disjoint from the sender keys and never send transactions, so their
// account nonce moves only via applied authorizations — which keeps
// scenarioState's authority-nonce tracking exact and, crucially, lets
// a delegation-clear produce a nonce-only account (nonce>0, zero
// balance, no code): the account class ParallelStateDB.Exist used to
// misreport.
const numFuzzAuthorities = 2

// fuzzGasLimit caps every tx so a malformed contract can't burn the
// fuzzer's wall-clock. 200k is enough for any contract scenario the
// generator produces; transfers consume 21k.
const fuzzGasLimit = 200_000

// fuzzMaxTxs caps txs per scenario so a long fuzz input doesn't blow
// out the per-iteration runtime — keeps each fuzz step fast.
const fuzzMaxTxs = 25

// fuzzKeys / fuzzAuthKeys are deterministic per-run keys derived from
// constant seeds so failing fuzz inputs reproduce identically across runs.
var (
	fuzzKeys     = mustGenFuzzKeys(numFuzzSenders, 0x42)
	fuzzAuthKeys = mustGenFuzzKeys(numFuzzAuthorities, 0x43)
)

func mustGenFuzzKeys(n int, tag byte) []*ecdsa.PrivateKey {
	out := make([]*ecdsa.PrivateKey, n)
	for i := 0; i < n; i++ {
		// Stable, deterministic key derivation from index — never use
		// these in production; they're test-only for byte-identical
		// repro across machines.
		seed := sha256.Sum256([]byte{tag, byte(i)})
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

// The EIP-6780 selfdestruct pair from PR #2268, predeployed at fixed
// addresses (genesis-style) so generator txs can target them. Entry X
// holds 10 wei; called by anyone but Z it bounces through helper Z,
// which re-enters X (msg.sender == Z → selfdestruct to payout EOA Y)
// and then selfdestructs back to X; X's outer frame finally transfers
// 10 wei to Y. With a zero-value entry call, the selfdestruct payout
// ops (X→Y, 10) shape-match the later recorded transfer (X→Y, 10) —
// the settlement mispairing class that produced V2 receipts diverging
// from serial while state roots stayed identical.
var (
	sdPairEntry  = common.HexToAddress("0x000000000000000000000000000000000000aaaa") // X
	sdPairHelper = common.HexToAddress("0x000000000000000000000000000000000000cccc") // Z

	// Payout EOA Y (low bytes bbbb) lives only inside sdPairEntryCode below; no Go binding.
	// if (msg.sender == Z) { selfdestruct(payable(Y)); }
	// else { Z.call{value: 0}(""); payable(Y).transfer(10); }
	sdPairEntryCode = common.FromHex("3373000000000000000000000000000000000000cccc146056575f5f5f5f5f73000000000000000000000000000000000000cccc5af1505f5f5f5f600a73000000000000000000000000000000000000bbbb5af150005b73000000000000000000000000000000000000bbbbff")
	// X.call{value: 0}(""); selfdestruct(payable(X));
	sdPairHelperCode = common.FromHex("5f5f5f5f5f73000000000000000000000000000000000000aaaa5af15073000000000000000000000000000000000000aaaaff")
)

// sweeper selfdestructs to the address in calldata[0:20], forwarding its
// whole balance (including the call value). An EIP-6780 payout credits
// the beneficiary directly in the opcode handler — unlike a plain value
// CALL, which explicitly creates a non-existent recipient (evm.Call →
// CreateAccount → CreatePath). Funding through the sweeper is therefore
// the way to give a fresh account balance without making it visible to
// existence checks that only consult CreatePath/balance/base.
var (
	sweeperAddr = common.HexToAddress("0x000000000000000000000000000000000000dddd")
	// PUSH0 CALLDATALOAD PUSH1 96 SHR SELFDESTRUCT
	sweeperCode = common.FromHex("5f3560601cff")
)

// fuzzTxKind enumerates the tx shapes the generator produces.
type fuzzTxKind uint8

const (
	kindTransferToSender fuzzTxKind = iota
	kindTransferToFresh
	kindContractCreate
	kindContractCall
	kindCallSelfDestructPair
	kind7702Auth
	kindNonceOnlyAccount
)

// fuzzTx is one tx in a decoded scenario.
type fuzzTx struct {
	kind         fuzzTxKind
	senderIdx    int    // index into fuzzKeys
	recipientIdx int    // for kindTransferToSender
	freshNonce   byte   // for kindTransferToFresh — derived from input bytes for determinism
	valueGwei    uint16 // small value so balances don't run out
	createKind   uint8  // for kindContractCreate — selects which canned bytecode
	authSel      uint8  // for kind7702Auth/kindNonceOnlyAccount — low bits pick the authority, 0x80 delegates to X (else clears)
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
		kind := fuzzTxKind(data[i] % 7)
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
		case kindCallSelfDestructPair:
			// value (0 is legal and is the mispairing shape) goes to X
		case kind7702Auth, kindNonceOnlyAccount:
			tx.authSel = aux
		}
		out = append(out, tx)
		i += 5
	}
	return out
}

// scenarioState tracks per-sender and per-authority nonces plus the
// most-recently-created contract address so kindContractCall has a target.
type scenarioState struct {
	nonces      [numFuzzSenders]uint64
	authNonces  [numFuzzAuthorities]uint64
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
	st := scenarioState{}
	txs := make([]*types.Transaction, 0, len(decoded))
	msgs := make([]*Message, 0, len(decoded))

	for _, d := range decoded {
		if d.kind == kindNonceOnlyAccount {
			txs, msgs = appendNonceOnlyAccountTxs(t, txs, msgs, &st, d, signer, baseFee)
			continue
		}
		nonce := st.nonces[d.senderIdx]
		st.nonces[d.senderIdx]++

		var to *common.Address
		var data []byte
		var authList []types.SetCodeAuthorization
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
			if !st.hasContract {
				// No contract deployed yet — fall back to a fresh transfer.
				a := freshAddr(d.freshNonce)
				to = &a
			} else {
				a := st.lastCreated
				to = &a
				gas = fuzzGasLimit
			}
		case kindCallSelfDestructPair:
			a := sdPairEntry
			to = &a
			gas = fuzzGasLimit
		case kind7702Auth:
			authIdx := int(d.authSel) % numFuzzAuthorities
			target := common.Address{} // zero address clears the delegation
			if d.authSel&0x80 != 0 {
				target = sdPairEntry
			}
			auth, err := types.SignSetCode(fuzzAuthKeys[authIdx], types.SetCodeAuthorization{
				ChainID: *uint256.MustFromBig(signer.ChainID()),
				Address: target,
				Nonce:   st.authNonces[authIdx],
			})
			if err != nil {
				t.Fatal(err)
			}
			st.authNonces[authIdx]++
			authList = []types.SetCodeAuthorization{auth}
			a := crypto.PubkeyToAddress(fuzzAuthKeys[authIdx].PublicKey)
			to = &a
			gas = fuzzGasLimit
		}

		// Compute value in wei from gwei; 0 is a legal value too.
		value := new(big.Int).Mul(big.NewInt(int64(d.valueGwei)), big.NewInt(1e9))
		var inner types.TxData
		if d.kind == kind7702Auth {
			// To is the authority itself, so post-application calls run its
			// (possibly delegated) code. Value stays zero so a cleared
			// authority remains a nonce-only account — the class
			// ParallelStateDB.Exist must report identically to serial.
			inner = &types.SetCodeTx{
				ChainID:   uint256.MustFromBig(signer.ChainID()),
				Nonce:     nonce,
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(1e9),
				Gas:       gas,
				To:        *to,
				Value:     uint256.NewInt(0),
				AuthList:  authList,
			}
		} else {
			inner = &types.DynamicFeeTx{
				ChainID:   signer.ChainID(),
				Nonce:     nonce,
				GasTipCap: big.NewInt(1),
				GasFeeCap: big.NewInt(1e9),
				Gas:       gas,
				To:        to,
				Value:     value,
				Data:      data,
			}
		}
		tx, err := types.SignTx(types.NewTx(inner), signer, fuzzKeys[d.senderIdx])
		if err != nil {
			t.Fatal(err)
		}
		// Track the contract address that this tx WOULD create — both
		// paths derive it the same way (CREATE = keccak(rlp(sender, nonce))).
		if d.kind == kindContractCreate {
			st.lastCreated = crypto.CreateAddress(crypto.PubkeyToAddress(fuzzKeys[d.senderIdx].PublicKey), nonce)
			st.hasContract = true
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

// appendNonceOnlyAccountTxs emits the three-tx sequence that leaves an
// authority as a nonce-only account (nonce>0, zero balance, no code, no
// in-block create) at the moment its authorization is validated:
//
//  1. a sender funds the authority with exactly drain-gas + 1 wei VIA THE
//     SWEEPER's selfdestruct payout — a plain value CALL would create the
//     recipient (evm.Call → CreateAccount), defeating the shape
//  2. the authority spends its entire balance in a plain transfer — its
//     account now exists only via the sender nonce increment
//  3. a sender carries a SetCodeTx authorization signed by the authority;
//     applyAuthorization gates a 12500-gas refund on Exist(authority)
//
// This is the EIP-7702 over-charge shape from the mainnet bad blocks the
// Exist nonce fix closed: serial sees the account (nonce>0), a V2 that
// derives existence from balance/create/base alone does not. Note that
// neither a delegation clear (SetCode(nil) marks the account created) nor
// a plain transfer (evm.Call creates the recipient) can produce this
// account class — which is why the plain kind7702Auth scenarios can't
// reach it and the selfdestruct-funded drain here is load-bearing.
func appendNonceOnlyAccountTxs(t testing.TB, txs []*types.Transaction, msgs []*Message, st *scenarioState, d fuzzTx, signer types.Signer, baseFee *big.Int) ([]*types.Transaction, []*Message) {
	t.Helper()
	authIdx := int(d.authSel) % numFuzzAuthorities
	authority := fuzzAuthKeys[authIdx]
	authorityAddr := crypto.PubkeyToAddress(authority.PublicKey)
	an := st.authNonces[authIdx]

	// Drain economics: fee cap = base fee + 1 tip, gas exactly 21000,
	// 1 wei of value onward — the upfront gas purchase plus value consumes
	// the funded amount to the last wei and the refund is zero.
	drainPrice := new(big.Int).Add(baseFee, big.NewInt(1))
	fund := new(big.Int).Mul(big.NewInt(21000), drainPrice)
	fund.Add(fund, big.NewInt(1))

	build := func(inner types.TxData, key *ecdsa.PrivateKey) {
		tx, err := types.SignTx(types.NewTx(inner), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := TransactionToMessage(tx, signer, baseFee)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
		msgs = append(msgs, msg)
	}

	sweeper := sweeperAddr
	build(&types.DynamicFeeTx{
		ChainID:   signer.ChainID(),
		Nonce:     st.nonces[d.senderIdx],
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1e9),
		Gas:       fuzzGasLimit,
		To:        &sweeper,
		Value:     fund,
		Data:      authorityAddr.Bytes(),
	}, fuzzKeys[d.senderIdx])
	st.nonces[d.senderIdx]++

	payout := freshAddr(0xee)
	build(&types.DynamicFeeTx{
		ChainID:   signer.ChainID(),
		Nonce:     an,
		GasTipCap: big.NewInt(1),
		GasFeeCap: drainPrice,
		Gas:       21000,
		To:        &payout,
		Value:     big.NewInt(1),
	}, authority)

	target := common.Address{}
	if d.authSel&0x80 != 0 {
		target = sdPairEntry
	}
	auth, err := types.SignSetCode(authority, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(signer.ChainID()),
		Address: target,
		Nonce:   an + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	build(&types.SetCodeTx{
		ChainID:   uint256.MustFromBig(signer.ChainID()),
		Nonce:     st.nonces[d.senderIdx],
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e9),
		Gas:       fuzzGasLimit,
		To:        authorityAddr,
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{auth},
	}, fuzzKeys[d.senderIdx])
	st.nonces[d.senderIdx]++
	st.authNonces[authIdx] = an + 2
	return txs, msgs
}

// buildBaseStateRoot creates an in-memory pre-state with every sender
// pre-funded and the selfdestruct pair predeployed, commits, and
// returns (db, root) so both serial and V2 paths can re-open at the
// same root.
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
	sdb.SetCode(sdPairEntry, sdPairEntryCode, 0)
	sdb.AddBalance(sdPairEntry, uint256.NewInt(10), 0)
	sdb.SetCode(sdPairHelper, sdPairHelperCode, 0)
	sdb.AddBalance(sdPairHelper, uint256.NewInt(10), 0)
	sdb.SetCode(sweeperAddr, sweeperCode, 0)
	root, err := sdb.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatal(err)
	}
	return tdb, root
}
