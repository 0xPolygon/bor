package core

import (
	"math/big"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const (
	keccakBranchFollowersPerWave = 7
	keccakBranchDelay            = 350_000
	keccakBranchFollowerGas      = params.MaxTxGas
)

type keccakBranchResult struct {
	heapGrowth uint64
	totalAlloc uint64
	duration   time.Duration
}

func keccakBranchPush2Placeholder(p *program.Program) int {
	p.Op(vm.PUSH2)
	offset := p.Size()
	p.Append([]byte{0, 0})
	return offset
}

func keccakBranchPatchPush2(p *program.Program, offset int, target uint64) {
	if target > 0xffff {
		panic("jump target does not fit PUSH2")
	}
	code := p.Bytes()
	code[offset] = byte(target >> 8)
	code[offset+1] = byte(target)
}

// keccakBranchGateCode: empty calldata reads slot zero; non-empty calldata
// stores its first word. Used so canonical (serial) ordering can flip the
// gate before speculative parallel workers observe the write.
func keccakBranchGateCode() []byte {
	p := program.New()
	p.Op(vm.CALLDATASIZE, vm.ISZERO)
	getterPatch := keccakBranchPush2Placeholder(p)
	p.Op(vm.JUMPI)
	p.Push(0).Op(vm.CALLDATALOAD).Push(0).Op(vm.SSTORE, vm.STOP)
	_, getter := p.Jumpdest()
	p.Push(0).Op(vm.SLOAD)
	p.Push(0).Op(vm.MSTORE)
	p.Push(32).Push(0).Op(vm.RETURN)
	keccakBranchPatchPush2(p, getterPatch, getter)
	return p.Bytes()
}

// keccakBranchLeaderCode delays briefly then flips the gate. The delay lets
// speculative workers read the gate before the leader's write is visible,
// while canonical serial execution always runs the leader first.
func keccakBranchLeaderCode(gate common.Address) []byte {
	p := program.New()
	p.Push(keccakBranchDelay)
	_, loop := p.Jumpdest()
	p.Push(1).Op(vm.SWAP1, vm.SUB, vm.DUP1)
	p.Push(loop).Op(vm.JUMPI)
	p.Op(vm.POP)
	p.Push(1).Push(0).Op(vm.MSTORE)
	p.Call(nil, gate, 0, 0, 32, 0, 0)
	p.Op(vm.POP, vm.STOP)
	return p.Bytes()
}

// keccakBranchFollowerCode: if the gate reads zero (a speculative run ahead
// of the leader), it hashes unique 64-byte inputs until out of gas.
// Canonical execution sees the gate set and stops immediately.
func keccakBranchFollowerCode(gate common.Address, id byte) []byte {
	p := program.New()
	p.StaticCall(nil, gate, 0, 0, 0, 32)
	p.Op(vm.POP)
	p.Push(0).Op(vm.MLOAD)
	stopPatch := keccakBranchPush2Placeholder(p)
	p.Op(vm.JUMPI)

	seedA := crypto.Keccak256Hash([]byte{0xa1, id})
	seedB := crypto.Keccak256Hash([]byte{0xb2, id})
	p.Push(seedA).Push(0).Op(vm.MSTORE)
	p.Push(seedB).Push(32).Op(vm.MSTORE)
	_, loop := p.Jumpdest()
	p.Push(64).Push(0).Op(vm.KECCAK256)
	p.Push(0).Op(vm.MSTORE)
	p.Push(loop).Op(vm.JUMP)

	_, stop := p.Jumpdest()
	p.Op(vm.STOP)
	keccakBranchPatchPush2(p, stopPatch, stop)
	return p.Bytes()
}

func keccakBranchFixture(t *testing.T) (*Genesis, *types.Block, *types.Block) {
	t.Helper()
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	chainConfig := *params.AllEthashProtocolChanges
	borConfig := *chainConfig.Bor
	borConfig.MadhugiriBlock = big.NewInt(0)
	chainConfig.Bor = &borConfig
	genesis := &Genesis{
		Config:   &chainConfig,
		GasLimit: 45_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)},
		},
	}

	signer := types.LatestSigner(genesis.Config)
	gate := crypto.CreateAddress(sender, 0)
	leader := crypto.CreateAddress(sender, 1)
	followers := make([]common.Address, keccakBranchFollowersPerWave)
	runtimes := [][]byte{keccakBranchGateCode(), keccakBranchLeaderCode(gate)}
	for i := range followers {
		followers[i] = crypto.CreateAddress(sender, uint64(i+2))
		runtimes = append(runtimes, keccakBranchFollowerCode(gate, byte(i+1)))
	}

	deployTxs := make([]*types.Transaction, 0, len(runtimes))
	for nonce, runtimeCode := range runtimes {
		initCode := program.New().ReturnViaCodeCopy(runtimeCode).Bytes()
		tx, err := types.SignTx(types.NewContractCreation(
			uint64(nonce), new(big.Int), 1_000_000,
			big.NewInt(2*params.InitialBaseFee), initCode,
		), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		deployTxs = append(deployTxs, tx)
	}

	branchTxs := make([]*types.Transaction, 0, 2+len(followers))
	branchTargets := append([]common.Address{leader}, followers...)
	branchTargets = append(branchTargets, gate)
	for i, to := range branchTargets {
		gas := uint64(10_000_000)
		var data []byte
		if i > 0 {
			gas = keccakBranchFollowerGas
		}
		if i == len(branchTargets)-1 {
			gas = 100_000
			data = []byte{0}
		}
		tx, err := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(len(deployTxs) + i),
			GasPrice: big.NewInt(2 * params.InitialBaseFee),
			Gas:      gas,
			To:       &to,
			Data:     data,
		}), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		branchTxs = append(branchTxs, tx)
	}

	engine := ethash.NewFaker()
	_, blocks, receipts := GenerateChainWithGenesis(genesis, engine, 2, func(i int, b *BlockGen) {
		txs := deployTxs
		if i == 1 {
			txs = branchTxs
		}
		for _, tx := range txs {
			b.AddTx(tx)
		}
	})
	if len(receipts) != 2 || len(receipts[0]) != len(deployTxs) || len(receipts[1]) != len(branchTxs) {
		t.Fatalf("unexpected receipt batches: %d", len(receipts))
	}
	for blockIndex, blockReceipts := range receipts {
		for txIndex, receipt := range blockReceipts {
			if receipt.Status != types.ReceiptStatusSuccessful {
				t.Fatalf("block %d transaction %d failed", blockIndex, txIndex)
			}
		}
	}
	return genesis, blocks[0], blocks[1]
}

func measureKeccakBranchProcessBlock(t *testing.T, genesis *Genesis, setupBlock, branchBlock *types.Block, parallel bool) keccakBranchResult {
	t.Helper()
	engine := ethash.NewFaker()
	var (
		chain *BlockChain
		err   error
	)
	if parallel {
		chain, err = NewParallelBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, DefaultConfig(), 8, false)
	} else {
		chain, err = NewBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, DefaultConfig())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if _, err := chain.InsertChain(types.Blocks{setupBlock}, false); err != nil {
		t.Fatalf("insert setup block: %v", err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	_, _, _, _, _, err = chain.ProcessBlock(branchBlock, setupBlock.Header(), nil, nil, nil)
	duration := time.Since(started)
	if err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	runtime.ReadMemStats(&after)
	var heapGrowth uint64
	if after.HeapAlloc > before.HeapAlloc {
		heapGrowth = after.HeapAlloc - before.HeapAlloc
	}
	return keccakBranchResult{
		heapGrowth: heapGrowth,
		totalAlloc: after.TotalAlloc - before.TotalAlloc,
		duration:   duration,
	}
}

// Discarded speculative executions must not grow the shared keccak cache
// past its cap.
func TestParallelKeccakCacheStaysBounded(t *testing.T) {
	// With GOMAXPROCS=1 the leader can finish before any follower runs, so
	// the speculative path would never execute.
	previousProcs := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previousProcs)

	oldGCPercent := debug.SetGCPercent(100)
	defer func() {
		debug.SetGCPercent(oldGCPercent)
		runtime.GC()
	}()

	genesis, setupBlock, branchBlock := keccakBranchFixture(t)
	serial := measureKeccakBranchProcessBlock(t, genesis, setupBlock, branchBlock, false)
	parallel := measureKeccakBranchProcessBlock(t, genesis, setupBlock, branchBlock, true)

	t.Logf("setup txs=%d setup gas used=%d", len(setupBlock.Transactions()), setupBlock.GasUsed())
	t.Logf("branch txs=%d canonical gas used=%d block gas limit=%d follower gas=%d",
		len(branchBlock.Transactions()), branchBlock.GasUsed(), branchBlock.GasLimit(), keccakBranchFollowerGas)
	t.Logf("serial ProcessBlock:   heap +%.2f MiB, allocated %.2f MiB, %s",
		float64(serial.heapGrowth)/(1<<20), float64(serial.totalAlloc)/(1<<20), serial.duration)
	t.Logf("parallel ProcessBlock: heap +%.2f MiB, allocated %.2f MiB, %s",
		float64(parallel.heapGrowth)/(1<<20), float64(parallel.totalAlloc)/(1<<20), parallel.duration)

	if branchBlock.GasUsed() >= 10_000_000 {
		t.Fatalf("branch block used too much canonical gas: %d", branchBlock.GasUsed())
	}

	// Headroom for the other per-block caches and prefetcher state.
	const maxAcceptableHeapGrowth = 64 << 20
	if parallel.heapGrowth > maxAcceptableHeapGrowth {
		t.Fatalf("parallel heap growth = %.2f MiB, want under %.2f MiB",
			float64(parallel.heapGrowth)/(1<<20), float64(maxAcceptableHeapGrowth)/(1<<20))
	}
}
