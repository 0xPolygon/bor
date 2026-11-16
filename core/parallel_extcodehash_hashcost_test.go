package core

import (
	"math/big"
	"runtime"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const repeatedExtcodehashGas = params.MaxTxGas

type extcodehashMeasurement struct {
	duration   time.Duration
	totalAlloc uint64
	usedGas    uint64
}

// repeatedExtcodehashInitcode creates a maximum-size child contract, then
// loops warm EXTCODEHASH against it until out of gas.
func repeatedExtcodehashInitcode() []byte {
	childRuntime := make([]byte, params.MaxCodeSize)
	childInitcode := program.New().ReturnViaCodeCopy(childRuntime).Bytes()

	outer := program.New()
	outer.Push(len(childInitcode))
	outer.Op(vm.PUSH2)
	childOffsetPosition := outer.Size()
	outer.Append([]byte{0, 0})
	outer.Push(0).Op(vm.CODECOPY)
	outer.Push(len(childInitcode)).Push(0).Push(0).Op(vm.CREATE)
	_, loop := outer.Jumpdest()
	outer.Op(vm.DUP1, vm.EXTCODEHASH, vm.POP)
	outer.Push(loop).Op(vm.JUMP)

	childOffset := outer.Size()
	outer.Append(childInitcode)
	outer.Bytes()[childOffsetPosition] = byte(childOffset >> 8)
	outer.Bytes()[childOffsetPosition+1] = byte(childOffset)
	return outer.Bytes()
}

func repeatedExtcodehashBlock(t *testing.T) (*Genesis, *types.Block) {
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

	initcode := repeatedExtcodehashInitcode()
	if len(initcode) > params.MaxInitCodeSize {
		t.Fatalf("initcode has %d bytes; maximum is %d", len(initcode), params.MaxInitCodeSize)
	}
	tx, err := types.SignTx(types.NewContractCreation(
		0,
		new(big.Int),
		repeatedExtcodehashGas,
		big.NewInt(params.BorDefaultMinerGasPrice),
		initcode,
	), types.LatestSigner(genesis.Config), key)
	if err != nil {
		t.Fatal(err)
	}

	_, blocks, receipts := GenerateChainWithGenesis(genesis, ethash.NewFaker(), 1, func(_ int, block *BlockGen) {
		block.AddTx(tx)
	})
	if len(receipts) != 1 || len(receipts[0]) != 1 {
		t.Fatalf("unexpected receipt count: %d batches", len(receipts))
	}
	if receipts[0][0].Status != types.ReceiptStatusFailed {
		t.Fatal("outer creation must run out of gas and fail (that's what keeps repeating the EXTCODEHASH loop)")
	}
	if blocks[0].GasUsed() != repeatedExtcodehashGas {
		t.Fatalf("block used %d gas; want %d", blocks[0].GasUsed(), repeatedExtcodehashGas)
	}
	return genesis, blocks[0]
}

func measureExtcodehashBlock(t *testing.T, genesis *Genesis, block *types.Block, parallel bool) extcodehashMeasurement {
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

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, _, usedGas, _, _, err := chain.ProcessBlock(block, chain.CurrentBlock(), nil, nil, nil)
	duration := time.Since(start)
	if err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	runtime.ReadMemStats(&after)
	return extcodehashMeasurement{
		duration:   duration,
		totalAlloc: after.TotalAlloc - before.TotalAlloc,
		usedGas:    usedGas,
	}
}

// Parallel processing of repeated EXTCODEHASH on the same address should
// stay within a small factor of serial.
func TestParallelExtcodehashRepeatedHashStaysCheap(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previousProcs)

	genesis, block := repeatedExtcodehashBlock(t)
	serial := measureExtcodehashBlock(t, genesis, block, false)
	parallel := measureExtcodehashBlock(t, genesis, block, true)

	t.Logf("canonical gas used=%d child code bytes=%d", block.GasUsed(), params.MaxCodeSize)
	t.Logf("serial ProcessBlock:   allocated %.2f MiB, %s", float64(serial.totalAlloc)/(1<<20), serial.duration)
	t.Logf("parallel ProcessBlock: allocated %.2f MiB, %s", float64(parallel.totalAlloc)/(1<<20), parallel.duration)
	if serial.duration > 0 {
		t.Logf("parallel/serial ratio: %.2fx", float64(parallel.duration)/float64(serial.duration))
	}

	if serial.usedGas != block.GasUsed() || parallel.usedGas != block.GasUsed() {
		t.Fatalf("processors disagreed on gas: block=%d serial=%d parallel=%d",
			block.GasUsed(), serial.usedGas, parallel.usedGas)
	}

	const maxAcceptableSlowdown = 5
	if parallel.duration > serial.duration*maxAcceptableSlowdown {
		t.Fatalf("parallel ProcessBlock took %s vs serial %s (>%dx) — code hash appears to be re-computed instead of cached",
			parallel.duration, serial.duration, maxAcceptableSlowdown)
	}
	const maxAcceptableWallClock = 3 * time.Second
	if parallel.duration > maxAcceptableWallClock {
		t.Fatalf("parallel ProcessBlock took %s, want under %s", parallel.duration, maxAcceptableWallClock)
	}
}
