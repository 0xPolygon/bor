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
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
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

// metamorphicHarness is the genesis-predeployed contract set that exercises the
// CREATE2 + SELFDESTRUCT + re-create ("metamorphic") lineage the old vocabulary
// could not reach. A target T is deployed at a deterministic CREATE2 address by
// a single deployer, can be destroyed (separate-tx or same-tx), re-created at
// the same address, and read back via EXTCODEHASH/EXTCODESIZE/BALANCE — the
// destruction-resolved reads whose V2 validation depends on the winning
// writer's position, not just its value.
type metamorphicHarness struct {
	deployer          common.Address // D: CREATE2-deploys T with salt = BALANCE(flag)
	deployDestroy     common.Address // DD: deploys + destroys T in one tx (EIP-6780 true deletion)
	reader            common.Address // R: records EXTCODEHASH/SIZE/BALANCE/SLOAD/EXTCODECOPY/SELFBALANCE/GAS into storage
	target            common.Address // T: CREATE2(D, salt=0, targetInit) — i.e. when flag balance is 0
	flag              common.Address // a plain account whose balance steers D's CREATE2 salt
	reverter          common.Address // REV: writes storage + deploys T, then REVERTs (journal rollback)
	transient         common.Address // TR: TLOAD/TSTORE (EIP-1153) — per-tx/per-incarnation isolation
	delegate          common.Address // DC: DELEGATECALL T (code-of-T / storage-of-DC) across the lineage
	clearRefund       common.Address // CLR: SSTORE nonzero→zero (gas refund parity)
	logger            common.Address // LG: LOG1(EXTCODEHASH(T)) — bloom/log parity across the lineage
	deployerCode      []byte
	deployDestroyCode []byte
	readerCode        []byte
	reverterCode      []byte
	transientCode     []byte
	delegateCode      []byte
	clearRefundCode   []byte
	loggerCode        []byte
	targetInit        []byte
}

// metaHarness is built once; all addresses/bytecodes are deterministic so serial
// and V2 runs target identical state.
var metaHarness = buildMetamorphicHarness()

func buildMetamorphicHarness() metamorphicHarness {
	deployer := common.HexToAddress("0x00000000000000000000000000000000000000d0")
	deployDestroy := common.HexToAddress("0x00000000000000000000000000000000000000d1")
	reader := common.HexToAddress("0x00000000000000000000000000000000000000e0")
	flag := common.HexToAddress("0x00000000000000000000000000000000000000f1")
	var salt [32]byte // 0 — the salt used when the flag account has zero balance

	// T runtime: dispatch on the first calldata byte. Selector 1 returns
	// storage slot 0 (so a caller can read T's storage across the
	// create/destroy/re-create lineage — the GetState destruction-resolved
	// victim read); any other selector (including empty calldata) selfdestructs
	// to the caller. Hand-laid out so the JUMPI target lands on the JUMPDEST at
	// offset 13; keep byte sizes fixed if editing.
	//
	//   00 PUSH0; 01 CALLDATALOAD; 02 PUSH1 0xf8; 04 SHR        -> selector
	//   05 PUSH1 1; 07 EQ; 08 PUSH1 13; 10 JUMPI                -> if sel==1 goto 13
	//   11 CALLER; 12 SELFDESTRUCT                              -> else destroy
	//   13 JUMPDEST; 14 PUSH0 SLOAD; 16 PUSH0 MSTORE; 18 PUSH1 32 PUSH0 RETURN
	targetRuntime := program.New().
		Push0().Op(vm.CALLDATALOAD).Push(0xf8).Op(vm.SHR).
		Push(1).Op(vm.EQ).
		Push(13).Op(vm.JUMPI).
		Op(vm.CALLER, vm.SELFDESTRUCT).
		Op(vm.JUMPDEST).
		Push0().Op(vm.SLOAD).Push0().Op(vm.MSTORE).Push(32).Push0().Op(vm.RETURN).
		Bytes()
	// T initcode: set slot0=1 so a live T carries storage state (its presence in
	// the trie when alive and absence when destroyed is what the state-root
	// comparison checks), then return the runtime.
	targetInit := program.New().Sstore(0, 1).ReturnViaCodeCopy(targetRuntime).Bytes()
	// Deterministic CREATE2 address when salt == 0 (flag balance 0).
	target := crypto.CreateAddress2(deployer, salt, crypto.Keccak256(targetInit))

	// D deploys T via CREATE2 with salt = BALANCE(flag). When the flag account
	// has zero balance the salt is 0 and T lands at `target`; once the flag is
	// funded the salt changes and the deploy lands elsewhere — so whether T
	// exists at `target` depends on a value another tx writes. This is the
	// reorderable input the executor must invalidate on: a tx that speculatively
	// reads flag balance 0, deploys T, then re-executes after the flag is funded
	// must withdraw its CodePath/CreatePath writes at `target`.
	//
	// Hand-built (jump-free) because the salt comes off the stack (BALANCE), not
	// a constant, so program.Create2's constant-salt helper can't be used.
	// CREATE2 pops value, offset, size, salt (value on top).
	deployerCode := program.New().
		Mstore(targetInit, 0).     // initcode → mem[0:len]
		Push(flag).Op(vm.BALANCE). // [salt = balance(flag)]
		Push(len(targetInit)).     // [salt, size]
		Push(0).                   // [salt, size, offset]
		Push(0).                   // [salt, size, offset, value]
		Op(vm.CREATE2).            // [addr]
		Op(vm.POP, vm.STOP).
		Bytes()

	// DD: call D (creates T this tx iff flag balance 0), then call T (T
	// selfdestructs this tx). Created and destroyed in one tx → EIP-6780
	// deletes the account.
	deployDestroyCode := program.New().
		Call(nil, deployer, 0, 0, 0, 0, 0).Op(vm.POP).
		Call(nil, target, 0, 0, 0, 0, 0).Op(vm.POP).
		Op(vm.STOP).Bytes()

	// R: record destruction-resolved reads of T into slots 0/1/2 via opcodes
	// (CodePath + balance), then slot 3 via a STATICCALL that makes T return its
	// storage slot 0 (StatePath victim read across the lineage).
	readerCode := program.New().
		Push(target).Op(vm.EXTCODEHASH).Push(0).Op(vm.SSTORE).
		Push(target).Op(vm.EXTCODESIZE).Push(1).Op(vm.SSTORE).
		Push(target).Op(vm.BALANCE).Push(2).Op(vm.SSTORE).
		Push(1).Push(0).Op(vm.MSTORE8). // mem[0] = selector 1
		StaticCall(nil, target, 0, 1, 0, 32).Op(vm.POP).
		Push(0).Op(vm.MLOAD).Push(3).Op(vm.SSTORE).
		// EXTCODECOPY T's first word → slot4 (CodePath read via a different opcode).
		ExtcodeCopy(target, 0, 0, 32).Push(0).Op(vm.MLOAD).Push(4).Op(vm.SSTORE).
		// SELFBALANCE → slot5; GAS canary → slot6 (gas determinism amplifier).
		Op(vm.SELFBALANCE).Push(5).Op(vm.SSTORE).
		Op(vm.GAS).Push(6).Op(vm.SSTORE).
		Op(vm.STOP).Bytes()

	reverter := common.HexToAddress("0x00000000000000000000000000000000000000e1")
	// REV: deploy T (via D) and write storage, then REVERT. Both effects — the
	// CreatePath/CodePath writes from the nested CREATE2 and the SSTORE — must be
	// rolled back and never published to other txs. A V2 that leaks a reverted
	// frame's writes diverges from serial.
	reverterCode := program.New().
		Call(nil, deployer, 0, 0, 0, 0, 0).Op(vm.POP).
		Sstore(0, 0xdead).
		Push0().Push0().Op(vm.REVERT).
		Bytes()

	// TR: TLOAD/TSTORE (EIP-1153). slot1 records the transient value BEFORE any
	// TSTORE — it must read 0 on every tx and every re-execution (transient is
	// per-tx and reset on each incarnation); a V2 that leaks transient across
	// txs/incarnations makes slot1 nonzero and diverges. slot2 confirms TSTORE
	// then TLOAD round-trips within the tx.
	transient := common.HexToAddress("0x00000000000000000000000000000000000000e2")
	transientCode := program.New().
		Push0().Op(vm.TLOAD).Push(1).Op(vm.SSTORE).
		Push(0xbeef).Push0().Op(vm.TSTORE).
		Push0().Op(vm.TLOAD).Push(2).Op(vm.SSTORE).
		Op(vm.STOP).Bytes()

	// DC: DELEGATECALL T's read path. Resolving the delegate target reads T's
	// CodePath (a different opcode path than EXTCODEHASH), executed against DC's
	// own storage — exercised across T's destroy/re-create lineage.
	delegate := common.HexToAddress("0x00000000000000000000000000000000000000e3")
	delegateCode := program.New().
		Push(1).Push(0).Op(vm.MSTORE8). // mem[0] = selector 1 (T's read path)
		DelegateCall(nil, target, 0, 1, 0, 32).Op(vm.POP).
		Push(0).Op(vm.MLOAD).Push(0).Op(vm.SSTORE).
		Op(vm.STOP).Bytes()

	// CLR: set then clear two slots (nonzero→zero) to trigger SSTORE clear
	// refunds; gas used (hence the receipt) must match between serial and V2.
	clearRefund := common.HexToAddress("0x00000000000000000000000000000000000000e4")
	clearRefundCode := program.New().
		Sstore(0, 1).Sstore(0, 0).
		Sstore(1, 1).Sstore(1, 0).
		Op(vm.STOP).Bytes()

	// LG: emit LOG1 whose data is EXTCODEHASH(T) and whose topic is constant.
	// The log payload tracks T's lineage, so bloom + logs (compared per receipt)
	// must agree across a destroy/re-create reorder.
	logger := common.HexToAddress("0x00000000000000000000000000000000000000e5")
	loggerCode := program.New().
		Push(target).Op(vm.EXTCODEHASH).Push0().Op(vm.MSTORE).
		Push(0xabcd).Push(32).Push0().Op(vm.LOG1).
		Op(vm.STOP).Bytes()

	return metamorphicHarness{
		deployer:          deployer,
		deployDestroy:     deployDestroy,
		reader:            reader,
		target:            target,
		flag:              flag,
		reverter:          reverter,
		transient:         transient,
		delegate:          delegate,
		clearRefund:       clearRefund,
		logger:            logger,
		deployerCode:      deployerCode,
		deployDestroyCode: deployDestroyCode,
		readerCode:        readerCode,
		reverterCode:      reverterCode,
		transientCode:     transientCode,
		delegateCode:      delegateCode,
		clearRefundCode:   clearRefundCode,
		loggerCode:        loggerCode,
		targetInit:        targetInit,
	}
}

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
	// Metamorphic CREATE2 lineage (see metamorphicHarness). Appended after the
	// original kinds so existing seed-corpus byte values keep their meaning.
	kindCreate2Deploy     // call D: CREATE2-deploy T (salt = flag balance; re-creates if destroyed)
	kindCreate2AndDestroy // call DD: deploy + destroy T in one tx (EIP-6780 deletion)
	kindDestroyTarget     // call T directly: T selfdestructs (no-op if T absent)
	kindReadTarget        // call R: EXTCODEHASH/SIZE/BALANCE(T) → storage
	kindFundTarget        // send value to T: funds a dead T, or bounces off a live one
	kindSetFlag           // fund the flag account: flips D's CREATE2 salt (reorderable input)
	kindCallRevert        // call REV: writes storage + deploys T, then REVERTs (journal rollback)
	kindTransient         // call TR: TLOAD/TSTORE isolation (EIP-1153)
	kindDelegateRead      // call DC: DELEGATECALL T's read path
	kindClearRefund       // call CLR: SSTORE nonzero→zero refund
	kindEmitLog           // call LG: LOG1(EXTCODEHASH(T))
	kindCount             // sentinel: number of tx kinds
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
	txType       uint8  // 0=dynamicfee(1559), 1=legacy, 2=accesslist(2930); ignored for 7702/nonce-only
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
		kind := fuzzTxKind(data[i] % byte(kindCount))
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
			// Tx envelope type from the value byte's high bits, which %4096
			// discards — so it doesn't perturb valueGwei.
			txType: (data[i+4] >> 4) % 3,
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
		case kindCreate2Deploy:
			a := metaHarness.deployer
			to = &a
			gas = fuzzGasLimit
		case kindCreate2AndDestroy:
			a := metaHarness.deployDestroy
			to = &a
			gas = fuzzGasLimit
		case kindDestroyTarget:
			a := metaHarness.target
			to = &a
			gas = fuzzGasLimit
		case kindReadTarget:
			a := metaHarness.reader
			to = &a
			gas = fuzzGasLimit
		case kindFundTarget:
			a := metaHarness.target
			to = &a
			gas = fuzzGasLimit
		case kindSetFlag:
			a := metaHarness.flag
			to = &a
		case kindCallRevert:
			a := metaHarness.reverter
			to = &a
			gas = fuzzGasLimit
		case kindTransient:
			a := metaHarness.transient
			to = &a
			gas = fuzzGasLimit
		case kindDelegateRead:
			a := metaHarness.delegate
			to = &a
			gas = fuzzGasLimit
		case kindClearRefund:
			a := metaHarness.clearRefund
			to = &a
			gas = fuzzGasLimit
		case kindEmitLog:
			a := metaHarness.logger
			to = &a
			gas = fuzzGasLimit
		case kind7702Auth:
			authIdx := int(d.authSel) % numFuzzAuthorities
			target := common.Address{} // zero address clears the delegation
			if d.authSel&0x80 != 0 {
				target = sdPairEntry
			} else if d.authSel&0x40 != 0 {
				// Delegate to the metamorphic target: resolving the authority's
				// delegated code reads T's CodePath, which a concurrent
				// destroy/re-create moves across the destruction boundary —
				// the 7702 × metamorphic interaction.
				target = metaHarness.target
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
		// kindSetFlag must move the flag account's balance off zero so D's salt
		// actually flips; force a minimum of 1 wei when the decoded value is 0.
		if d.kind == kindSetFlag && value.Sign() == 0 {
			value = big.NewInt(1)
		}
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
			// Vary the tx envelope so V2 must handle every type identically.
			switch d.txType {
			case 1: // legacy
				inner = &types.LegacyTx{
					Nonce:    nonce,
					GasPrice: big.NewInt(1e9),
					Gas:      gas,
					To:       to,
					Value:    value,
					Data:     data,
				}
			case 2: // EIP-2930 access list — pre-warms the target's slot 0
				alGas := max(gas, 30000) // cover the access-list intrinsic gas
				inner = &types.AccessListTx{
					ChainID:    signer.ChainID(),
					Nonce:      nonce,
					GasPrice:   big.NewInt(1e9),
					Gas:        alGas,
					To:         to,
					Value:      value,
					Data:       data,
					AccessList: types.AccessList{{Address: metaHarness.target, StorageKeys: []common.Hash{{}}}},
				}
			default: // EIP-1559 dynamic fee
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
	sdb.SetCode(metaHarness.deployer, metaHarness.deployerCode, 0)
	sdb.SetCode(metaHarness.deployDestroy, metaHarness.deployDestroyCode, 0)
	sdb.SetCode(metaHarness.reader, metaHarness.readerCode, 0)
	sdb.SetCode(metaHarness.reverter, metaHarness.reverterCode, 0)
	sdb.SetCode(metaHarness.transient, metaHarness.transientCode, 0)
	sdb.SetCode(metaHarness.delegate, metaHarness.delegateCode, 0)
	sdb.SetCode(metaHarness.clearRefund, metaHarness.clearRefundCode, 0)
	sdb.SetCode(metaHarness.logger, metaHarness.loggerCode, 0)
	root, err := sdb.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatal(err)
	}
	return tdb, root
}
