package core

import (
	"bytes"
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

type blockhashWitnessFixture struct {
	genesis *Genesis
	engine  *ethash.Ethash
	prefix  types.Blocks

	honestBlock   *types.Block
	honestWitness *stateless.Witness

	// zeroHashBlock was sealed with BLOCKHASH(0) == 0, and partialWitness
	// carries only its parent header, so stateless execution can reproduce
	// that root only by treating the missing ancestor as a zero hash.
	zeroHashBlock  *types.Block
	partialWitness *stateless.Witness
}

// newBlockhashWitnessFixture builds genesis plus two blocks, then block 3
// calling a contract that stores 1 under BLOCKHASH(0).
func newBlockhashWitnessFixture(t *testing.T) *blockhashWitnessFixture {
	t.Helper()

	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	contract := common.HexToAddress("0xb1a5cab1ec0de")
	code := []byte{byte(vm.PUSH1), 1, byte(vm.PUSH1), 0, byte(vm.BLOCKHASH), byte(vm.SSTORE), byte(vm.STOP)}
	config := params.AllEthashProtocolChanges
	genesis := &Genesis{
		Config:  config,
		BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			from:     {Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(params.Ether))},
			contract: {Code: code},
		},
	}
	engine := ethash.NewFaker()
	signer := types.LatestSigner(config)

	db, prefix, _ := GenerateChainWithGenesis(genesis, engine, 2, func(int, *BlockGen) {})
	producer, err := NewBlockChain(db, genesis, engine, DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Stop)
	if _, err := producer.InsertChain(prefix, false); err != nil {
		t.Fatalf("insert prefix: %v", err)
	}
	callContract := func(_ int, gen *BlockGen) {
		tx, err := types.SignTx(types.NewTransaction(gen.TxNonce(from), contract, big.NewInt(0), 100_000, gen.header.BaseFee, nil), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		gen.AddTxWithChain(producer, tx)
	}

	honestBlocks, _ := GenerateChain(config, prefix[1], engine, db, 1, callContract)
	honestBlock := honestBlocks[0]
	if _, err := producer.InsertChain(honestBlocks, true); err != nil {
		t.Fatalf("insert honest block: %v", err)
	}
	honestWitness := decodeWitness(t, producer.GetWitness(honestBlock.Hash()))
	if len(honestWitness.Headers) < 3 {
		t.Fatalf("honest witness has %d headers, want parent plus the BLOCKHASH(0) ancestors", len(honestWitness.Headers))
	}

	// Resolving BLOCKHASH(0) from block 3 reads headers 2 and 1. Hide header 1
	// so the producer's execution sees zero, as stateless execution would
	// with a parent-only witness.
	producer.hc.headerCache.Purge()
	rawdb.DeleteHeader(db, prefix[0].Hash(), prefix[0].NumberU64())
	zeroHashBlocks, _ := GenerateChain(config, prefix[1], engine, db, 1, callContract)
	zeroHashBlock := zeroHashBlocks[0]
	if zeroHashBlock.Root() == honestBlock.Root() {
		t.Fatal("hiding the ancestor did not change the state root")
	}

	// The zero-hash execution writes a storage slot the honest witness has no
	// proof for, so add that proof and keep only the parent header.
	zeroSlotProof, err := stateless.NewWitness(zeroHashBlock.Header(), producer)
	if err != nil {
		t.Fatal(err)
	}
	parentState, err := state.New(prefix[1].Root(), producer.statedb)
	if err != nil {
		t.Fatal(err)
	}
	parentState.SetWitness(zeroSlotProof)
	parentState.GetState(contract, common.Hash{})
	parentState.IntermediateRoot(config.IsEIP158(zeroHashBlock.Number()))

	partial := honestWitness.Copy()
	partial.Headers = partial.Headers[:1]
	partial.SetHeader(zeroHashBlock.Header())
	for node := range zeroSlotProof.State {
		partial.State[node] = struct{}{}
	}
	var buf bytes.Buffer
	if err := partial.EncodeRLP(&buf); err != nil {
		t.Fatal(err)
	}

	return &blockhashWitnessFixture{
		genesis:        genesis,
		engine:         engine,
		prefix:         prefix,
		honestBlock:    honestBlock,
		honestWitness:  honestWitness,
		zeroHashBlock:  zeroHashBlock,
		partialWitness: decodeWitness(t, buf.Bytes()),
	}
}

func decodeWitness(t *testing.T, encoded []byte) *stateless.Witness {
	t.Helper()
	if len(encoded) == 0 {
		t.Fatal("no witness")
	}
	var w stateless.Witness
	if err := rlp.DecodeBytes(encoded, &w); err != nil {
		t.Fatalf("decode witness: %v", err)
	}
	return &w
}

func (f *blockhashWitnessFixture) newStatelessChain(t *testing.T, parallel bool) *BlockChain {
	t.Helper()
	db, _, _ := GenerateChainWithGenesis(f.genesis, f.engine, 2, func(int, *BlockGen) {})
	cfg := DefaultConfig().WithStateScheme(rawdb.HashScheme)
	cfg.Stateless = true
	chain, err := NewBlockChain(db, f.genesis, f.engine, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	if _, err := chain.InsertChain(f.prefix, false); err != nil {
		t.Fatalf("insert prefix: %v", err)
	}
	chain.parallelStatelessImportEnabled.Store(parallel)
	if parallel {
		chain.parallelStatelessImportWorkers = 4
	}
	return chain
}

func TestStatelessImportBlockhashAncestors(t *testing.T) {
	f := newBlockhashWitnessFixture(t)

	for _, parallel := range []bool{false, true} {
		name := "serial"
		if parallel {
			name = "parallel"
		}
		t.Run(name+"/complete witness imports", func(t *testing.T) {
			chain := f.newStatelessChain(t, parallel)
			n, err := chain.InsertChainStateless(types.Blocks{f.honestBlock}, []*stateless.Witness{f.honestWitness.Copy()})
			if err != nil || n != 1 {
				t.Fatalf("processed=%d err=%v, want 1 and nil", n, err)
			}
			if head := chain.CurrentBlock().Hash(); head != f.honestBlock.Hash() {
				t.Fatalf("head=%x, want %x", head, f.honestBlock.Hash())
			}
		})
		t.Run(name+"/missing ancestor is rejected", func(t *testing.T) {
			chain := f.newStatelessChain(t, parallel)
			head := chain.CurrentBlock().Hash()
			_, err := chain.InsertChainStateless(types.Blocks{f.zeroHashBlock}, []*stateless.Witness{f.partialWitness.Copy()})
			if !errors.Is(err, ErrStatelessMissingAncestor) || !errors.Is(err, ErrStatelessIncompleteState) {
				t.Fatalf("err=%v, want ErrStatelessMissingAncestor wrapped in ErrStatelessIncompleteState", err)
			}
			if got := chain.CurrentBlock().Hash(); got != head {
				t.Fatalf("head moved to %x", got)
			}
		})
	}

	t.Run("ProcessBlockWithWitnesses", func(t *testing.T) {
		chain := f.newStatelessChain(t, false)
		if _, _, err := chain.ProcessBlockWithWitnesses(f.zeroHashBlock, f.partialWitness.Copy()); !errors.Is(err, ErrStatelessMissingAncestor) {
			t.Fatalf("err=%v, want ErrStatelessMissingAncestor", err)
		}
	})
}

func TestGetHashFnReportsMissingAncestor(t *testing.T) {
	genesis := &types.Header{Number: big.NewInt(0)}
	one := &types.Header{Number: big.NewInt(1), ParentHash: genesis.Hash()}
	two := &types.Header{Number: big.NewInt(2), ParentHash: one.Hash()}
	ref := &types.Header{Number: big.NewInt(3), ParentHash: two.Hash()}

	newChain := func(missing *[]uint64, headers ...*types.Header) *HeaderChain {
		db := rawdb.NewMemoryDatabase()
		for _, h := range headers {
			rawdb.WriteHeader(db, h)
		}
		hc := &HeaderChain{chainDb: db, headerCache: lru.NewCache[common.Hash, *types.Header](16)}
		if missing != nil {
			hc.onMissingAncestor = func(n uint64) { *missing = append(*missing, n) }
		}
		return hc
	}

	tests := []struct {
		name        string
		headers     []*types.Header
		n           uint64
		want        common.Hash
		wantMissing []uint64
	}{
		{name: "parent from ref", n: 2, want: two.Hash()},
		{name: "walked ancestor", headers: []*types.Header{two, one}, n: 0, want: genesis.Hash()},
		{name: "ref itself", n: 3},
		{name: "ancestor beyond the headers", headers: []*types.Header{two}, n: 0, wantMissing: []uint64{0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var missing []uint64
			if got := GetHashFn(ref, newChain(&missing, tt.headers...))(tt.n); got != tt.want {
				t.Fatalf("hash=%x, want %x", got, tt.want)
			}
			if !slices.Equal(missing, tt.wantMissing) {
				t.Fatalf("reported missing %v, want %v", missing, tt.wantMissing)
			}
		})
	}

	t.Run("no hook keeps the zero hash", func(t *testing.T) {
		if got := GetHashFn(ref, newChain(nil, two))(0); got != (common.Hash{}) {
			t.Fatalf("hash=%x, want zero", got)
		}
	})
}
