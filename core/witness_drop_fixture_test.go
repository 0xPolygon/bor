package core

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// This fixture backs the witness/drop determinism suite. It builds one block
// that drives every witness-collection path in the serial processor at once,
// plus a set of "ghost" transactions that a producer may attempt and then
// abandon.
//
// The property under test is that the witness is a function of the block:
// whatever the producer tried and discarded must leave no trace, or a node
// importing the block computes a different witness than the node that
// produced it, and the two disagree on the BP-signed witness hash.

var (
	wdReadOnly = common.HexToAddress("0x00000000000000000000000000000000000d0001")
	wdWrite    = common.HexToAddress("0x00000000000000000000000000000000000d0002")
	wdRevert   = common.HexToAddress("0x00000000000000000000000000000000000d0003")
	wdProbe    = common.HexToAddress("0x00000000000000000000000000000000000d0004")
	wdCreate   = common.HexToAddress("0x00000000000000000000000000000000000d0005")
	wdDeep     = common.HexToAddress("0x00000000000000000000000000000000000d0006")
	wdKill     = common.HexToAddress("0x00000000000000000000000000000000000d0007")
	wdSuicide  = common.HexToAddress("0x00000000000000000000000000000000000d0008")
	wdExists   = common.HexToAddress("0x00000000000000000000000000000000000e0001")
	wdMissing  = common.HexToAddress("0x00000000000000000000000000000000000e0002")

	// Ghost targets: reachable only through a transaction the producer
	// attempts and then drops.
	wdGhostA     = common.HexToAddress("0x00000000000000000000000000000000000d0009")
	wdGhostB     = common.HexToAddress("0x00000000000000000000000000000000000d000a")
	wdGhostProbe = common.HexToAddress("0x00000000000000000000000000000000000e0009")
	// wdShared is read by BOTH a ghost and an included transaction: dropping
	// the ghost must not remove what the included transaction needs.
	wdShared = common.HexToAddress("0x00000000000000000000000000000000000d000b")
)

// wdWriters is how many independent storage-writing contracts the block
// touches. Each becomes one concurrent trie update in IntermediateRoot, where
// witness.AddState is called from every worker goroutine at once.
const wdWriters = 24

func wdWriter(i int) common.Address {
	return common.BigToAddress(new(big.Int).Add(big.NewInt(0xf00000), big.NewInt(int64(i))))
}

// wdSloadRun reads slots 0..n-1 and discards the values.
func wdSloadRun(n int) []byte {
	var asm string
	for i := 0; i < n; i++ {
		asm += fmt.Sprintf("60%02x", i) + "54" + "50" // PUSH1 i; SLOAD; POP
	}
	return common.FromHex(asm + "00")
}

// wdSstoreRun writes i+1 into slots 0..n-1.
func wdSstoreRun(n int) []byte {
	var asm string
	for i := 0; i < n; i++ {
		asm += fmt.Sprintf("60%02x", i+1) + fmt.Sprintf("60%02x", i) + "55"
	}
	return common.FromHex(asm + "00")
}

// wdRevertCode writes a slot and then reverts: the state change rolls back but
// the read stays in the witness, which has no journal.
func wdRevertCode() []byte {
	return common.FromHex("6007" + "5f" + "55" + "5f" + "5f" + "fd")
}

// wdProbeCode exercises absence proofs: an account that exists and one that
// does not, through both BALANCE and the code-hash/size opcodes.
func wdProbeCode() []byte {
	asm := "73" + wdExists.Hex()[2:] + "31" + "50" +
		"73" + wdMissing.Hex()[2:] + "31" + "50" +
		"73" + wdExists.Hex()[2:] + "3f" + "50" +
		"73" + wdMissing.Hex()[2:] + "3b" + "50"
	return common.FromHex(asm + "00")
}

// wdCreateCode deploys a child that writes storage.
func wdCreateCode() []byte {
	// init (7 bytes): PUSH1 0x42; PUSH0; SSTORE; PUSH0; PUSH0; RETURN
	// PUSH7 stores it right-aligned in word 0, so it lives at offset 25.
	asm := "66" + "60425f555f5ff3" +
		"5f" + "52" +
		"6007" + "6019" + "5f" + "f0" + "50"
	return common.FromHex(asm + "00")
}

// wdSuicideCode deploys a child that writes storage and SELFDESTRUCTs in the
// same transaction -- post-Cancun the only way to drive a real destruction.
func wdSuicideCode() []byte {
	// init (5 bytes): PUSH1 0x01; PUSH0; SSTORE; CALLER; SELFDESTRUCT
	asm := "64" + "60015f5533ff" +
		"5f" + "52" +
		"6005" + "601b" + "5f" + "f0" + "50"
	return common.FromHex(asm + "00")
}

// wdGhostCode reads storage and an account balance nothing else in the block
// touches, then writes a slot so the abandoned transaction has state effects
// to roll back.
func wdGhostCode(probe common.Address, base int) []byte {
	var asm string
	for i := 0; i < 8; i++ {
		asm += fmt.Sprintf("60%02x", base+i) + "54" + "50"
	}
	asm += "73" + probe.Hex()[2:] + "31" + "50"
	asm += "6009" + "6009" + "55"
	return common.FromHex(asm + "00")
}

// wdSharedCode branches on calldata so one contract can be entered two ways:
//
//	no calldata  -> read slots 0..3      (loads the account, caches those slots)
//	any calldata -> read slots 16..19    (fresh slots on an already-loaded account)
//
// That second entry is what makes the dropped-transaction cache eviction
// observable. If an included transaction loads the account first, a dropped
// transaction reading NEW slots of it cannot be cleaned up by evicting the
// account -- the account legitimately belongs to the block -- so the slot
// cache itself has to be rolled back, or a later included read of those slots
// is a silent cache hit whose trie path never reaches the witness.
//
//	 0: CALLDATASIZE
//	 1: PUSH1 0x15
//	 3: JUMPI
//	 4: read slots 0..3        (16 bytes)
//	20: STOP
//	21: JUMPDEST
//	22: read slots 16..19      (16 bytes)
//	38: STOP
func wdSharedCode() []byte {
	base := ""
	for i := 0; i < 4; i++ {
		base += fmt.Sprintf("60%02x", i) + "54" + "50"
	}
	ext := ""
	for i := 16; i < 20; i++ {
		ext += fmt.Sprintf("60%02x", i) + "54" + "50"
	}
	return common.FromHex("36" + "6015" + "57" + base + "00" + "5b" + ext + "00")
}

type wdKey struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

// witnessDropFixture is one block plus a pool of ghost transactions the
// producer may attempt and abandon.
type witnessDropFixture struct {
	root     common.Hash
	tdb      *triedb.Database
	txs      []*types.Transaction
	names    []string
	ghosts   []*types.Transaction
	tasks    []V2Task
	keys     []*wdKey
	signer   types.Signer
	blockCtx vm.BlockContext
	config   *params.ChainConfig
	header   *types.Header
}

func newWitnessDropFixture(t *testing.T) *witnessDropFixture {
	t.Helper()

	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	gen, err := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	if err != nil {
		t.Fatalf("genesis state: %v", err)
	}
	cfg := *params.MergedTestChainConfig

	gen.SetCode(wdReadOnly, wdSloadRun(8), tracing.CodeChangeUnspecified)
	gen.SetCode(wdWrite, wdSstoreRun(4), tracing.CodeChangeUnspecified)
	gen.SetCode(wdRevert, wdRevertCode(), tracing.CodeChangeUnspecified)
	gen.SetCode(wdProbe, wdProbeCode(), tracing.CodeChangeUnspecified)
	gen.SetCode(wdCreate, wdCreateCode(), tracing.CodeChangeUnspecified)
	gen.SetCode(wdDeep, wdSstoreRun(16), tracing.CodeChangeUnspecified)
	gen.SetCode(wdSuicide, wdSuicideCode(), tracing.CodeChangeUnspecified)
	gen.SetCode(wdShared, wdSharedCode(), tracing.CodeChangeUnspecified)
	gen.SetCode(wdGhostA, wdGhostCode(wdGhostProbe, 16), tracing.CodeChangeUnspecified)
	gen.SetCode(wdGhostB, wdGhostCode(wdGhostProbe, 32), tracing.CodeChangeUnspecified)

	// Deep, pre-populated storage: shallow tries hide path-dependent node
	// selection.
	for i := 0; i < 96; i++ {
		k := common.BigToHash(big.NewInt(int64(i)))
		v := common.BigToHash(big.NewInt(int64(i + 1000)))
		for _, a := range []common.Address{wdReadOnly, wdWrite, wdDeep, wdKill, wdGhostA, wdGhostB, wdShared} {
			gen.SetState(a, k, v)
		}
	}
	for i := 0; i < wdWriters; i++ {
		addr := wdWriter(i)
		gen.SetCode(addr, wdSstoreRun(8), tracing.CodeChangeUnspecified)
		for j := 0; j < 96; j++ {
			gen.SetState(addr, common.BigToHash(big.NewInt(int64(j))), common.BigToHash(big.NewInt(int64(j+7))))
		}
	}

	gen.AddBalance(wdExists, uint256.NewInt(7), tracing.BalanceChangeUnspecified)
	gen.AddBalance(wdGhostProbe, uint256.NewInt(11), tracing.BalanceChangeUnspecified)

	type wdCall struct {
		to   common.Address
		data []byte
	}
	included := []wdCall{
		{to: wdReadOnly}, {to: wdWrite}, {to: wdRevert}, {to: wdProbe}, {to: wdCreate},
		{to: wdDeep}, {to: wdKill}, {to: wdReadOnly}, {to: wdSuicide},
		// Loads wdShared and caches slots 0..3. Anything a dropped
		// transaction later reads on this account is a NEW slot on an
		// already-loaded object.
		{to: wdShared},
	}
	for i := 0; i < wdWriters; i++ {
		included = append(included, wdCall{to: wdWriter(i)})
	}
	// Reads wdShared slots 16..19 -- the same slots wdGhostShared touches.
	// Placed last so a ghost can sit between the two.
	included = append(included, wdCall{to: wdShared, data: []byte{0x01}})

	ghostCalls := []wdCall{{to: wdGhostA}, {to: wdGhostB}, {to: wdShared, data: []byte{0x01}}}

	// Deterministic sender keys: random keys would change the witness between
	// fixture builds and make cross-run comparison meaningless. Every
	// transaction gets its own sender so nonce ordering never couples them.
	nsenders := len(included) + len(ghostCalls)
	keys := make([]*wdKey, 0, nsenders)
	for i := 1; i <= nsenders; i++ {
		k, err := crypto.ToECDSA(common.LeftPadBytes([]byte{byte(i >> 8), byte(i)}, 32))
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		addr := crypto.PubkeyToAddress(k.PublicKey)
		gen.AddBalance(addr, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
		keys = append(keys, &wdKey{key: k, addr: addr})
	}

	root, err := gen.Commit(0, false, false)
	if err != nil {
		t.Fatalf("commit genesis: %v", err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatalf("commit triedb: %v", err)
	}

	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    common.HexToAddress("0x00000000000000000000000000000000c0ffee00"),
		GasLimit:    30_000_000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     big.NewInt(7),
		Random:      &common.Hash{},
	}

	f := &witnessDropFixture{
		root: root, tdb: tdb, blockCtx: blockCtx, config: &cfg,
		header: &types.Header{
			Number: new(big.Int).Set(blockCtx.BlockNumber), Time: blockCtx.Time,
			GasLimit: blockCtx.GasLimit, BaseFee: new(big.Int).Set(blockCtx.BaseFee), Root: root,
		},
	}

	signer := types.NewLondonSigner(cfg.ChainID)
	f.keys = keys
	f.signer = signer
	mk := func(k *wdKey, c wdCall) *types.Transaction {
		dst := c.to
		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID: cfg.ChainID, Nonce: 0, GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(7),
			Gas: 1_000_000, To: &dst, Value: big.NewInt(0), Data: c.data,
		}), signer, k.key)
		if err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		return tx
	}

	for i, c := range included {
		tx := mk(keys[i], c)
		f.txs = append(f.txs, tx)
		f.names = append(f.names, wdName(i, c.to, len(c.data) > 0))
		msg, err := TransactionToMessage(tx, signer, blockCtx.BaseFee)
		if err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
		f.tasks = append(f.tasks, V2Task{Index: i, Tx: tx, Msg: msg})
	}
	for j, c := range ghostCalls {
		f.ghosts = append(f.ghosts, mk(keys[len(included)+j], c))
	}
	return f
}

func wdName(i int, to common.Address, ext bool) string {
	switch to {
	case wdReadOnly:
		return fmt.Sprintf("readOnly%d", i)
	case wdWrite:
		return "write"
	case wdRevert:
		return "revert"
	case wdProbe:
		return "probe"
	case wdCreate:
		return "create"
	case wdDeep:
		return "deep"
	case wdKill:
		return "kill"
	case wdSuicide:
		return "suicide"
	case wdShared:
		if ext {
			return "sharedExt"
		}
		return "shared"
	}
	return fmt.Sprintf("writer%d", i)
}

// wdTxNonce signs a transaction from the sender at index i with an explicit
// nonce, used to drive the pre-check drop paths (nonce too high / too low).
func (f *witnessDropFixture) wdTxNonce(t *testing.T, i int, to common.Address, nonce uint64) *types.Transaction {
	t.Helper()
	dst := to
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID: f.config.ChainID, Nonce: nonce, GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(7),
		Gas: 1_000_000, To: &dst, Value: big.NewInt(0),
	}), f.signer, f.keys[i].key)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}
