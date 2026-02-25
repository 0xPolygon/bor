// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package ethash

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// testStateBuilder creates and initializes a state database for testing
type testStateBuilder struct {
	t      *testing.T
	db     *triedb.Database
	state  *state.StateDB
	addr   common.Address
	amount *big.Int
}

func newTestStateBuilder(t *testing.T) *testStateBuilder {
	privateKey, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)

	memDB := rawdb.NewMemoryDatabase()
	trieDB := triedb.NewDatabase(memDB, triedb.HashDefaults)
	stateDB, _ := state.New(types.EmptyRootHash, state.NewDatabase(trieDB, nil))

	initialBalance := new(big.Int).Mul(big.NewInt(1000), big.NewInt(params.Ether))
	stateDB.SetBalance(addr, uint256.MustFromBig(initialBalance), tracing.BalanceChangeUnspecified)
	stateDB.SetNonce(addr, 0, tracing.NonceChangeUnspecified)

	return &testStateBuilder{
		t:      t,
		db:     trieDB,
		state:  stateDB,
		addr:   addr,
		amount: initialBalance,
	}
}

func (tsb *testStateBuilder) getState() *state.StateDB {
	return tsb.state
}

// assertBlockValid validates the finalized block matches expectations
func assertBlockValid(t *testing.T, result *blockAssemblyResult, expectedHeader *types.Header) {
	t.Helper()

	if result.err != nil {
		t.Fatalf("block assembly failed with error: %v", result.err)
	}

	if result.block == nil || result.receipts == nil {
		t.Fatal("block assembly returned nil block or receipts")
	}

	if result.commitDuration < 0 {
		t.Fatalf("negative commit duration: %v", result.commitDuration)
	}

	// Validate block fields
	if result.block.Number().Cmp(expectedHeader.Number) != 0 {
		t.Errorf("block number mismatch: expected %v, got %v", expectedHeader.Number, result.block.Number())
	}

	if result.block.GasLimit() != expectedHeader.GasLimit {
		t.Errorf("gas limit mismatch: expected %v, got %v", expectedHeader.GasLimit, result.block.GasLimit())
	}

	// Check root hashes are populated
	if result.block.Root() == (common.Hash{}) || expectedHeader.Root == (common.Hash{}) {
		t.Error("root hash should not be empty")
	}
}

type blockAssemblyResult struct {
	block          *types.Block
	receipts       []*types.Receipt
	commitDuration time.Duration
	err            error
}

// fakeChainConfig wraps chain configuration for testing
type fakeChainConfig struct {
	cfg *params.ChainConfig
}

func createFakeChain(cfg *params.ChainConfig) consensus.ChainHeaderReader {
	return &fakeChainConfig{cfg: cfg}
}

func (f *fakeChainConfig) Config() *params.ChainConfig {
	return f.cfg
}

func (f *fakeChainConfig) CurrentHeader() *types.Header {
	return &types.Header{}
}

func (f *fakeChainConfig) GetHeader(_ common.Hash, _ uint64) *types.Header {
	return nil
}

func (f *fakeChainConfig) GetHeaderByNumber(_ uint64) *types.Header {
	return nil
}

func (f *fakeChainConfig) GetHeaderByHash(_ common.Hash) *types.Header {
	return nil
}

func (f *fakeChainConfig) GetTd(_ common.Hash, _ uint64) *big.Int {
	return big.NewInt(0)
}

type diffTest struct {
	ParentTimestamp    uint64
	ParentDifficulty   *big.Int
	CurrentTimestamp   uint64
	CurrentBlocknumber *big.Int
	CurrentDifficulty  *big.Int
}

func (d *diffTest) UnmarshalJSON(b []byte) (err error) {
	var ext struct {
		ParentTimestamp    string
		ParentDifficulty   string
		CurrentTimestamp   string
		CurrentBlocknumber string
		CurrentDifficulty  string
	}

	if err := json.Unmarshal(b, &ext); err != nil {
		return err
	}

	d.ParentTimestamp = math.MustParseUint64(ext.ParentTimestamp)
	d.ParentDifficulty = math.MustParseBig256(ext.ParentDifficulty)
	d.CurrentTimestamp = math.MustParseUint64(ext.CurrentTimestamp)
	d.CurrentBlocknumber = math.MustParseBig256(ext.CurrentBlocknumber)
	d.CurrentDifficulty = math.MustParseBig256(ext.CurrentDifficulty)

	return nil
}

func TestCalcDifficulty(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "tests", "testdata", "BasicTests", "difficulty.json"))
	if err != nil {
		t.Skip(err)
	}
	defer file.Close()

	tests := make(map[string]diffTest)

	err = json.NewDecoder(file).Decode(&tests)
	if err != nil {
		t.Fatal(err)
	}

	config := &params.ChainConfig{HomesteadBlock: big.NewInt(1150000)}

	for name, test := range tests {
		number := new(big.Int).Sub(test.CurrentBlocknumber, big.NewInt(1))

		diff := CalcDifficulty(config, test.CurrentTimestamp, &types.Header{
			Number:     number,
			Time:       test.ParentTimestamp,
			Difficulty: test.ParentDifficulty,
		})
		if diff.Cmp(test.CurrentDifficulty) != 0 {
			t.Error(name, "failed. Expected", test.CurrentDifficulty, "and calculated", diff)
		}
	}
}

func randSlice(min, max uint32) []byte {
	var b = make([]byte, 4)
	_, _ = crand.Read(b)
	a := binary.LittleEndian.Uint32(b)
	size := min + a%(max-min)
	out := make([]byte, size)
	_, _ = crand.Read(out)

	return out
}

func TestDifficultyCalculators(t *testing.T) {
	for i := 0; i < 5000; i++ {
		// 1 to 300 seconds diff
		var timeDelta = uint64(1 + rand.Uint32()%3000)

		diffBig := new(big.Int).SetBytes(randSlice(2, 10))
		if diffBig.Cmp(params.MinimumDifficulty) < 0 {
			diffBig.Set(params.MinimumDifficulty)
		}
		//rand.Read(difficulty)
		header := &types.Header{
			Difficulty: diffBig,
			Number:     new(big.Int).SetUint64(rand.Uint64() % 50_000_000),
			Time:       rand.Uint64() - timeDelta,
		}
		if rand.Uint32()&1 == 0 {
			header.UncleHash = types.EmptyUncleHash
		}

		bombDelay := new(big.Int).SetUint64(rand.Uint64() % 50_000_000)
		for i, pair := range []struct {
			bigFn  func(time uint64, parent *types.Header) *big.Int
			u256Fn func(time uint64, parent *types.Header) *big.Int
		}{
			{FrontierDifficultyCalculator, CalcDifficultyFrontierU256},
			{HomesteadDifficultyCalculator, CalcDifficultyHomesteadU256},
			{DynamicDifficultyCalculator(bombDelay), MakeDifficultyCalculatorU256(bombDelay)},
		} {
			time := header.Time + timeDelta
			want := pair.bigFn(time, header)
			have := pair.u256Fn(time, header)

			if want.BitLen() > 256 {
				continue
			}

			if want.Cmp(have) != 0 {
				t.Fatalf("pair %d: want %x have %x\nparent.Number: %x\np.Time: %x\nc.Time: %x\nBombdelay: %v\n", i, want, have,
					header.Number, header.Time, time, bombDelay)
			}
		}
	}
}

func BenchmarkDifficultyCalculator(b *testing.B) {
	x1 := makeDifficultyCalculator(big.NewInt(1000000))
	x2 := MakeDifficultyCalculatorU256(big.NewInt(1000000))
	h := &types.Header{
		ParentHash: common.Hash{},
		UncleHash:  types.EmptyUncleHash,
		Difficulty: big.NewInt(0xffffff),
		Number:     big.NewInt(500000),
		Time:       1000000,
	}

	b.Run("big-frontier", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			calcDifficultyFrontier(1000014, h)
		}
	})
	b.Run("u256-frontier", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			CalcDifficultyFrontierU256(1000014, h)
		}
	})
	b.Run("big-homestead", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			calcDifficultyHomestead(1000014, h)
		}
	})
	b.Run("u256-homestead", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			CalcDifficultyHomesteadU256(1000014, h)
		}
	})
	b.Run("big-generic", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			x1(1000014, h)
		}
	})
	b.Run("u256-generic", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			x2(1000014, h)
		}
	})
}

func TestFinalizeAndAssembleReturnsCommitTime(t *testing.T) {
	t.Parallel()

	// Setup first test scenario
	stateBuilder1 := newTestStateBuilder(t)
	testConfig := params.TestChainConfig
	fakeChain := createFakeChain(testConfig)
	ethashEngine := NewFaker()

	firstHeader := &types.Header{
		Number:     big.NewInt(1),
		GasLimit:   5000000,
		GasUsed:    0,
		Time:       1000000,
		Difficulty: big.NewInt(1),
		Coinbase:   common.Address{},
		ParentHash: common.Hash{},
	}

	firstBody := &types.Body{
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
		Withdrawals:  []*types.Withdrawal{},
	}

	b1, r1, ct1, e1 := ethashEngine.FinalizeAndAssemble(fakeChain, firstHeader, stateBuilder1.getState(), firstBody, []*types.Receipt{})
	result1 := &blockAssemblyResult{block: b1, receipts: r1, commitDuration: ct1, err: e1}
	assertBlockValid(t, result1, firstHeader)

	// Setup second test scenario with larger state
	stateBuilder2 := newTestStateBuilder(t)
	largeStateAddr := common.Address{0xaa}
	stateBuilder2.state.SetBalance(largeStateAddr, uint256.NewInt(100), tracing.BalanceChangeUnspecified)

	// Populate state with storage entries
	for idx := 0; idx < 100; idx++ {
		storageKey := common.BigToHash(big.NewInt(int64(idx)))
		storageVal := common.BigToHash(big.NewInt(int64(idx * 2)))
		stateBuilder2.state.SetState(largeStateAddr, storageKey, storageVal)
	}

	secondHeader := &types.Header{
		Number:     big.NewInt(2),
		GasLimit:   5000000,
		GasUsed:    0,
		Time:       1000001,
		Difficulty: big.NewInt(1),
		Coinbase:   common.Address{},
		ParentHash: b1.Hash(),
	}

	secondBody := &types.Body{
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
		Withdrawals:  []*types.Withdrawal{},
	}

	b2, r2, ct2, e2 := ethashEngine.FinalizeAndAssemble(fakeChain, secondHeader, stateBuilder2.getState(), secondBody, []*types.Receipt{})
	result2 := &blockAssemblyResult{block: b2, receipts: r2, commitDuration: ct2, err: e2}
	assertBlockValid(t, result2, secondHeader)

	t.Logf("Commit time for empty state: %v", ct1)
	t.Logf("Commit time for state with 100 storage entries: %v", ct2)
}
