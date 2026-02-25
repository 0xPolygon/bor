package vm

import (
	_ "embed"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

//go:embed testdata/snailtracer.hex
var snailtracerHex string

// BenchmarkSnailtracer runs the snailtracer Benchmark() function through
// the EVM with no tracer attached. The interpreter takes whichever path
// is active in interpreter.go — runSwitch when enabled, the traditional
// Run() loop when commented out.
//
// Usage:
//
//	# With runSwitch enabled:
//	go test -run=^$ -bench=BenchmarkSnailtracer -benchmem -count=10 ./core/vm/ | tee new.txt
//
//	# With runSwitch disabled:
//	go test -run=^$ -bench=BenchmarkSnailtracer -benchmem -count=10 ./core/vm/ | tee old.txt
//
//	# Compare:
//	benchstat old.txt new.txt
func BenchmarkSnailtracer(b *testing.B) {
	code := hexDecode(strings.TrimSpace(snailtracerHex))
	addr := common.BytesToAddress([]byte("snailtracer"))
	caller := common.BytesToAddress([]byte("caller"))

	// Benchmark() selector = 0x30627b7c
	calldata := hexDecode("30627b7c")

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		db, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		db.CreateAccount(addr)
		db.SetCode(addr, code, tracing.CodeChangeUnspecified)
		db.CreateAccount(caller)
		db.AddBalance(caller, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
		db.Finalise(true)

		bctx := BlockContext{
			CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
			Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int) {},
			GetHash:     func(uint64) common.Hash { return common.Hash{} },
			BlockNumber: big.NewInt(1),
			Time:        1,
			Difficulty:  big.NewInt(1),
			GasLimit:    1_000_000_000,
			BaseFee:     big.NewInt(1),
			BlobBaseFee: big.NewInt(1),
			Random:      &common.Hash{},
		}

		rules := benchChainConfig.Rules(bctx.BlockNumber, bctx.Random != nil, bctx.Time)
		db.Prepare(rules, caller, common.Address{}, &addr, ActivePrecompiles(rules), nil)

		evm := NewEVM(bctx, db, benchChainConfig, Config{})
		evm.SetTxContext(TxContext{
			Origin:   caller,
			GasPrice: big.NewInt(1),
		})
		evm.Call(caller, addr, calldata, 1_000_000_000, new(uint256.Int), nil)
	}
}

func hexDecode(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Chain config that enables all forks through Cancun (no Verkle / EIP-4762).
// ---------------------------------------------------------------------------

var benchChainConfig = &params.ChainConfig{
	ChainID:             big.NewInt(1),
	HomesteadBlock:      new(big.Int),
	DAOForkBlock:        new(big.Int),
	EIP150Block:         new(big.Int),
	EIP155Block:         new(big.Int),
	EIP158Block:         new(big.Int),
	ByzantiumBlock:      new(big.Int),
	ConstantinopleBlock: new(big.Int),
	PetersburgBlock:     new(big.Int),
	IstanbulBlock:       new(big.Int),
	MuirGlacierBlock:    new(big.Int),
	BerlinBlock:         new(big.Int),
	LondonBlock:         new(big.Int),
	ArrowGlacierBlock:   new(big.Int),
	GrayGlacierBlock:    new(big.Int),
	MergeNetsplitBlock:  new(big.Int),
	ShanghaiBlock:       new(big.Int),
	CancunBlock:         new(big.Int),
	PragueBlock:         new(big.Int),
	OsakaBlock:          new(big.Int),
	// VerkleBlock intentionally nil — enabling it would activate EIP-4762.
}
