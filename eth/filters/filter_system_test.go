// Copyright 2016 The go-ethereum Authors
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

package filters

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/filtermaps"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/triedb"
)

type testBackend struct {
	db              ethdb.Database
	fm              *filtermaps.FilterMaps
	txFeed          event.Feed
	logsFeed        event.Feed
	rmLogsFeed      event.Feed
	pendingLogsFeed event.Feed
	chainFeed       event.Feed
	pendingBlock    *types.Block
	pendingReceipts types.Receipts

	stateSyncFeed  event.Feed
	chainConfig    *params.ChainConfig
	historyCutoff  uint64
	headersByHash  map[common.Hash]*types.Header
	headersByNum   map[uint64]*types.Header
	receiptsByHash map[common.Hash]types.Receipts
	borReceipts    map[common.Hash]*types.Receipt
	headHash       common.Hash
	finalizedHash  common.Hash
}

func (b *testBackend) SubscribeStateSyncEvent(ch chan<- core.StateSyncEvent) event.Subscription {
	return b.stateSyncFeed.Subscribe(ch)
}

func (b *testBackend) ChainConfig() *params.ChainConfig {
	if b.chainConfig != nil {
		return b.chainConfig
	}

	return params.TestChainConfig
}

func (b *testBackend) CurrentHeader() *types.Header {
	hdr, _ := b.HeaderByNumber(context.TODO(), rpc.LatestBlockNumber)
	return hdr
}

func (b *testBackend) CurrentBlock() *types.Header {
	return b.CurrentHeader()
}

func (b *testBackend) ChainDb() ethdb.Database {
	return b.db
}

func (b *testBackend) GetCanonicalHash(number uint64) common.Hash {
	if b.headersByNum != nil {
		if header := b.headersByNum[number]; header != nil {
			return header.Hash()
		}
	}

	return rawdb.ReadCanonicalHash(b.db, number)
}

func (b *testBackend) GetHeader(hash common.Hash, number uint64) *types.Header {
	hdr, _ := b.HeaderByHash(context.Background(), hash)
	return hdr
}

func (b *testBackend) GetReceiptsByHash(hash common.Hash) types.Receipts {
	r, _ := b.GetReceipts(context.Background(), hash)
	return r
}

func (b *testBackend) GetRawReceipts(hash common.Hash, number uint64) types.Receipts {
	return rawdb.ReadRawReceipts(b.db, hash, number)
}

func (b *testBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	if b.headersByNum != nil {
		switch blockNr {
		case rpc.LatestBlockNumber:
			return b.headersByHash[b.headHash], nil
		case rpc.FinalizedBlockNumber:
			if b.finalizedHash == (common.Hash{}) {
				return nil, nil
			}
			return b.headersByHash[b.finalizedHash], nil
		case rpc.SafeBlockNumber:
			return nil, errors.New("safe block not found")
		default:
			if blockNr < 0 {
				return nil, nil
			}
			return b.headersByNum[uint64(blockNr)], nil
		}
	}

	var (
		hash common.Hash
		num  uint64
	)

	// nolint:exhaustive
	switch blockNr {
	case rpc.LatestBlockNumber:
		hash = rawdb.ReadHeadBlockHash(b.db)
		number, ok := rawdb.ReadHeaderNumber(b.db, hash)
		if !ok {
			//nolint:nilnil
			return nil, nil
		}
		num = number
	case rpc.FinalizedBlockNumber:
		hash = rawdb.ReadFinalizedBlockHash(b.db)
		number, ok := rawdb.ReadHeaderNumber(b.db, hash)
		if !ok {
			return nil, nil
		}
		num = number
	case rpc.SafeBlockNumber:
		return nil, errors.New("safe block not found")
	default:
		num = uint64(blockNr)
		hash = rawdb.ReadCanonicalHash(b.db, num)
	}

	return rawdb.ReadHeader(b.db, hash, num), nil
}

func (b *testBackend) HeaderByHash(_ context.Context, hash common.Hash) (*types.Header, error) {
	if b.headersByHash != nil {
		return b.headersByHash[hash], nil
	}

	number, ok := rawdb.ReadHeaderNumber(b.db, hash)
	if !ok {
		//nolint:nilnil
		return nil, nil
	}
	return rawdb.ReadHeader(b.db, hash, number), nil
}

func (b *testBackend) GetBody(ctx context.Context, hash common.Hash, number rpc.BlockNumber) (*types.Body, error) {
	if body := rawdb.ReadBody(b.db, hash, uint64(number)); body != nil {
		return body, nil
	}

	return nil, errors.New("block body not found")
}

func (b *testBackend) GetReceipts(_ context.Context, hash common.Hash) (types.Receipts, error) {
	if b.receiptsByHash != nil {
		return b.receiptsByHash[hash], nil
	}

	if number, ok := rawdb.ReadHeaderNumber(b.db, hash); ok {
		if header := rawdb.ReadHeader(b.db, hash, number); header != nil {
			return rawdb.ReadReceipts(b.db, hash, number, header.Time, params.TestChainConfig), nil
		}
	}

	return nil, nil
}

func (b *testBackend) GetLogs(_ context.Context, hash common.Hash, number uint64) ([][]*types.Log, error) {
	if b.receiptsByHash != nil {
		receipts := b.receiptsByHash[hash]
		logs := make([][]*types.Log, len(receipts))
		for i, receipt := range receipts {
			logs[i] = receipt.Logs
		}
		return logs, nil
	}

	logs := rawdb.ReadLogs(b.db, hash, number)
	return logs, nil
}

func (b *testBackend) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return b.txFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) event.Subscription {
	return b.rmLogsFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.logsFeed.Subscribe(ch)
}

func (b *testBackend) SubscribePendingLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.pendingLogsFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeChainEvent(ch chan<- core.ChainEvent) event.Subscription {
	return b.chainFeed.Subscribe(ch)
}

func (b *testBackend) CurrentView() *filtermaps.ChainView {
	head := b.CurrentBlock()
	return filtermaps.NewChainView(b, head.Number.Uint64(), head.Hash())
}

func (b *testBackend) NewMatcherBackend() filtermaps.MatcherBackend {
	return b.fm.NewMatcherBackend()
}

func (b *testBackend) GetBorBlockReceipt(_ context.Context, blockHash common.Hash) (*types.Receipt, error) {
	if b.borReceipts != nil {
		number, ok := rawdb.ReadHeaderNumber(b.db, blockHash)
		if !ok {
			return &types.Receipt{}, nil
		}
		if cfg := b.ChainConfig(); cfg != nil && cfg.Bor != nil && cfg.Bor.Sprint != nil && !cfg.Bor.IsSprintStart(number) {
			return &types.Receipt{}, nil
		}
		if receipt := b.borReceipts[blockHash]; receipt != nil {
			return receipt, nil
		}
		return &types.Receipt{}, nil
	}

	number, ok := rawdb.ReadHeaderNumber(b.db, blockHash)
	if !ok {
		return &types.Receipt{}, nil
	}

	receipt := rawdb.ReadBorReceipt(b.db, blockHash, number, b.ChainConfig())
	if receipt == nil {
		return &types.Receipt{}, nil
	}

	return receipt, nil
}

func (b *testBackend) GetBorBlockLogs(ctx context.Context, blockHash common.Hash) ([]*types.Log, error) {
	receipt, err := b.GetBorBlockReceipt(ctx, blockHash)
	if err != nil {
		return []*types.Log{}, err
	}

	if receipt == nil {
		return []*types.Log{}, nil
	}

	return receipt.Logs, nil
}

func (b *testBackend) startFilterMaps(history uint64, disabled bool, params filtermaps.Params) {
	head := b.CurrentBlock()
	chainView := filtermaps.NewChainView(b, head.Number.Uint64(), head.Hash())
	config := filtermaps.Config{
		History:        history,
		Disabled:       disabled,
		ExportFileName: "",
	}
	b.fm, _ = filtermaps.NewFilterMaps(b.db, chainView, 0, 0, params, config)
	b.fm.Start()
	b.fm.WaitIdle()
}

func (b *testBackend) stopFilterMaps() {
	b.fm.Stop()
	b.fm = nil
}

func (b *testBackend) setPending(block *types.Block, receipts types.Receipts) {
	b.pendingBlock = block
	b.pendingReceipts = receipts
}

func (b *testBackend) HistoryPruningCutoff() uint64 {
	return b.historyCutoff
}

func newTestFilterSystem(db ethdb.Database, cfg Config) (*testBackend, *FilterSystem) {
	backend := &testBackend{db: db}
	sys := NewFilterSystem(backend, cfg)

	return backend, sys
}

type historicalBorLogsHarness struct {
	api             *FilterAPI
	preBlock        *types.Block
	missingBorBlock *types.Block
	postBlock       *types.Block
	preBorAddr      common.Address
	preBorTopic     common.Hash
	postAddr        common.Address
	postTopic       common.Hash
	postBorAddr     common.Address
}

func makeReceiptWithTopic(addr common.Address, topic common.Hash) *types.Receipt {
	receipt := types.NewReceipt(nil, false, 0)
	receipt.Logs = []*types.Log{{
		Address: addr,
		Topics:  []common.Hash{topic},
	}}
	receipt.Bloom = types.CreateBloom(receipt)

	return receipt
}

func makeBorLogs(addr common.Address, topic common.Hash) []*types.Log {
	return []*types.Log{{
		Address: addr,
		Topics:  []common.Hash{topic},
	}}
}

func cloneBorHistoricalTestConfig(madhugiriBlock uint64) *params.ChainConfig {
	cfgCopy := *params.BorTestChainConfig
	borCopy := *params.BorTestChainConfig.Bor
	sprintCopy := make(map[string]uint64, len(borCopy.Sprint))
	for k, v := range borCopy.Sprint {
		sprintCopy[k] = v
	}
	borCopy.Sprint = sprintCopy
	borCopy.Sprint["0"] = 1
	borCopy.MadhugiriBlock = new(big.Int).SetUint64(madhugiriBlock)
	cfgCopy.Bor = &borCopy

	return &cfgCopy
}

func writeBorReceiptForTest(t *testing.T, db ethdb.Database, _ *params.ChainConfig, block *types.Block, receipts types.Receipts, logs []*types.Log) {
	t.Helper()

	logIndex := uint(0)
	for _, receipt := range receipts {
		logIndex += uint(len(receipt.Logs))
	}

	types.DeriveFieldsForBorLogs(logs, block.Hash(), block.NumberU64(), uint(len(block.Transactions())), logIndex)

	batch := db.NewBatch()
	rawdb.WriteBorReceipt(batch, block.Hash(), block.NumberU64(), &types.ReceiptForStorage{
		Status: types.ReceiptStatusSuccessful,
		Logs:   logs,
	})
	rawdb.WriteBorTxLookupEntry(batch, block.Hash(), block.NumberU64())

	if err := batch.Write(); err != nil {
		t.Fatalf("failed to write bor receipt: %v", err)
	}
}

func newHistoricalBorLogsHarness(t *testing.T, enableBorLogs bool) *historicalBorLogsHarness {
	t.Helper()

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, enableBorLogs)
		cfg          = cloneBorHistoricalTestConfig(4)

		preBorAddr  = common.HexToAddress("0x1000000000000000000000000000000000000001")
		postAddr    = common.HexToAddress("0x2000000000000000000000000000000000000002")
		postBorAddr = common.HexToAddress("0x3000000000000000000000000000000000000003")

		preBorTopic  = common.HexToHash("0x1000000000000000000000000000000000000000000000000000000000000001")
		postTopic    = common.HexToHash("0x2000000000000000000000000000000000000000000000000000000000000002")
		postBorTopic = common.HexToHash("0x3000000000000000000000000000000000000000000000000000000000000003")

		gspec = &core.Genesis{
			Config:  cfg,
			Alloc:   types.GenesisAlloc{},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
	)

	backend.chainConfig = cfg
	api.SetChainConfig(cfg)
	t.Cleanup(func() { _ = db.Close() })

	_, chain, receipts := core.GenerateChainWithGenesis(gspec, ethash.NewFaker(), 5, func(i int, gen *core.BlockGen) {
		if i == 3 {
			receipt := makeReceiptWithTopic(postAddr, postTopic)
			gen.AddUncheckedReceipt(receipt)
			gen.AddUncheckedTx(types.NewTransaction(999, common.HexToAddress("0x999"), big.NewInt(999), 999, gen.BaseFee(), nil))
		}
	})

	gspec.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults))

	for i, block := range chain {
		rawdb.WriteBlock(db, block)
		rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
		rawdb.WriteHeadBlockHash(db, block.Hash())
		rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), receipts[i])
	}

	backend.headersByHash = make(map[common.Hash]*types.Header, len(chain))
	backend.headersByNum = make(map[uint64]*types.Header, len(chain))
	backend.receiptsByHash = make(map[common.Hash]types.Receipts, len(chain))
	backend.borReceipts = make(map[common.Hash]*types.Receipt, 2)
	for i, block := range chain {
		backend.headersByHash[block.Hash()] = block.Header()
		backend.headersByNum[block.NumberU64()] = block.Header()
		backend.receiptsByHash[block.Hash()] = receipts[i]
	}
	backend.headHash = chain[len(chain)-1].Hash()
	backend.finalizedHash = chain[3].Hash()

	preBorLogs := makeBorLogs(preBorAddr, preBorTopic)
	writeBorReceiptForTest(t, db, cfg, chain[1], receipts[1], preBorLogs)
	backend.borReceipts[chain[1].Hash()] = &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   preBorLogs,
	}

	postBorLogs := makeBorLogs(postBorAddr, postBorTopic)
	writeBorReceiptForTest(t, db, cfg, chain[3], receipts[3], postBorLogs)
	backend.borReceipts[chain[3].Hash()] = &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   postBorLogs,
	}

	backend.startFilterMaps(16, false, filtermaps.RangeTestParams)
	t.Cleanup(backend.stopFilterMaps)

	return &historicalBorLogsHarness{
		api:             api,
		preBlock:        chain[1],
		missingBorBlock: chain[2],
		postBlock:       chain[3],
		preBorAddr:      preBorAddr,
		preBorTopic:     preBorTopic,
		postAddr:        postAddr,
		postTopic:       postTopic,
		postBorAddr:     postBorAddr,
	}
}

// TestBlockSubscription tests if a block subscription returns block hashes for posted chain events.
// It creates multiple subscriptions:
// - one at the start and should receive all posted chain events and a second (blockHashes)
// - one that is created after a cutoff moment and uninstalled after a second cutoff moment (blockHashes[cutoff1:cutoff2])
// - one that is created after the second cutoff moment (blockHashes[cutoff2:])
func TestBlockSubscription(t *testing.T) {
	t.Parallel()

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, true)
		genesis      = &core.Genesis{
			Config:  params.TestChainConfig,
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		_, chain, _ = core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 10, func(i int, gen *core.BlockGen) {})
		chainEvents []core.ChainEvent
	)

	for _, blk := range chain {
		chainEvents = append(chainEvents, core.ChainEvent{Header: blk.Header()})
	}

	chan0 := make(chan *types.Header)
	sub0 := api.events.SubscribeNewHeads(chan0)
	chan1 := make(chan *types.Header)
	sub1 := api.events.SubscribeNewHeads(chan1)

	go func() { // simulate client
		i1, i2 := 0, 0
		for i1 != len(chainEvents) || i2 != len(chainEvents) {
			select {
			case header := <-chan0:
				if chainEvents[i1].Header.Hash() != header.Hash() {
					t.Errorf("sub0 received invalid hash on index %d, want %x, got %x", i1, chainEvents[i1].Header.Hash(), header.Hash())
				}

				i1++
			case header := <-chan1:
				if chainEvents[i2].Header.Hash() != header.Hash() {
					t.Errorf("sub1 received invalid hash on index %d, want %x, got %x", i2, chainEvents[i2].Header.Hash(), header.Hash())
				}

				i2++
			}
		}

		sub0.Unsubscribe()
		sub1.Unsubscribe()
	}()

	time.Sleep(1 * time.Second)

	for _, e := range chainEvents {
		backend.chainFeed.Send(e)
	}

	<-sub0.Err()
	<-sub1.Err()
}

// TestPendingTxFilter tests whether pending tx filters retrieve all pending transactions that are posted to the event mux.
func TestPendingTxFilter(t *testing.T) {
	t.Parallel()

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, true)

		transactions = []*types.Transaction{
			types.NewTransaction(0, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(1, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(2, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(3, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(4, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
		}

		hashes []common.Hash
	)

	fid0 := api.NewPendingTransactionFilter(nil)

	time.Sleep(1 * time.Second)
	backend.txFeed.Send(core.NewTxsEvent{Txs: transactions})

	timeout := time.Now().Add(1 * time.Second)

	for {
		results, err := api.GetFilterChanges(fid0)
		if err != nil {
			t.Fatalf("Unable to retrieve logs: %v", err)
		}

		h := results.([]common.Hash)

		hashes = append(hashes, h...)
		if len(hashes) >= len(transactions) {
			break
		}
		// check timeout
		if time.Now().After(timeout) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if len(hashes) != len(transactions) {
		t.Errorf("invalid number of transactions, want %d transactions(s), got %d", len(transactions), len(hashes))
		return
	}

	for i := range hashes {
		if hashes[i] != transactions[i].Hash() {
			t.Errorf("hashes[%d] invalid, want %x, got %x", i, transactions[i].Hash(), hashes[i])
		}
	}
}

// TestPendingTxFilterFullTx tests whether pending tx filters retrieve all pending transactions that are posted to the event mux.
func TestPendingTxFilterFullTx(t *testing.T) {
	t.Parallel()

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, true)

		transactions = []*types.Transaction{
			types.NewTransaction(0, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(1, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(2, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(3, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(4, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
		}

		txs []*ethapi.RPCTransaction
	)

	fullTx := true
	fid0 := api.NewPendingTransactionFilter(&fullTx)

	time.Sleep(1 * time.Second)
	backend.txFeed.Send(core.NewTxsEvent{Txs: transactions})

	timeout := time.Now().Add(1 * time.Second)

	for {
		results, err := api.GetFilterChanges(fid0)
		if err != nil {
			t.Fatalf("Unable to retrieve logs: %v", err)
		}

		tx := results.([]*ethapi.RPCTransaction)

		txs = append(txs, tx...)
		if len(txs) >= len(transactions) {
			break
		}
		// check timeout
		if time.Now().After(timeout) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if len(txs) != len(transactions) {
		t.Errorf("invalid number of transactions, want %d transactions(s), got %d", len(transactions), len(txs))
		return
	}

	for i := range txs {
		if txs[i].Hash != transactions[i].Hash() {
			t.Errorf("hashes[%d] invalid, want %x, got %x", i, transactions[i].Hash(), txs[i].Hash)
		}
	}
}

// TestLogFilterCreation test whether a given filter criteria makes sense.
// If not it must return an error.
func TestLogFilterCreation(t *testing.T) {
	var (
		db     = rawdb.NewMemoryDatabase()
		_, sys = newTestFilterSystem(db, Config{})
		api    = NewFilterAPI(sys, true)

		testCases = []struct {
			crit    FilterCriteria
			success bool
		}{
			// defaults
			{FilterCriteria{}, true},
			// valid block number range
			{FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(2)}, true},
			// "mined" block range to pending
			{FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, true},
			// from block "higher" than to block
			{FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(1)}, false},
			// from block "higher" than to block
			{FilterCriteria{FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(100)}, false},
			// from block "higher" than to block
			{FilterCriteria{FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(100)}, false},
			// from block "higher" than to block
			{FilterCriteria{FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, false},
			// topics more than 4
			{FilterCriteria{Topics: [][]common.Hash{{}, {}, {}, {}, {}}}, false},
		}
	)

	for i, test := range testCases {
		id, err := api.NewFilter(test.crit)
		if err != nil && test.success {
			t.Errorf("expected filter creation for case %d to success, got %v", i, err)
		}

		if err == nil {
			api.UninstallFilter(id)

			if !test.success {
				t.Errorf("expected testcase %d to fail with an error", i)
			}
		}
	}
}

// TestInvalidLogFilterCreation tests whether invalid filter log criteria results in an error
// when the filter is created.
func TestInvalidLogFilterCreation(t *testing.T) {
	t.Parallel()

	var (
		db     = rawdb.NewMemoryDatabase()
		_, sys = newTestFilterSystem(db, Config{LogQueryLimit: 1000})
		api    = NewFilterAPI(sys, true)
	)

	// different situations where log filter creation should fail.
	// Reason: fromBlock > toBlock
	testCases := []FilterCriteria{
		0: {FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())},
		1: {FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(100)},
		2: {FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(100)},
		3: {Topics: [][]common.Hash{{}, {}, {}, {}, {}}},
		4: {Addresses: make([]common.Address, api.logQueryLimit+1)},
	}

	for i, test := range testCases {
		if _, err := api.NewFilter(test); err == nil {
			t.Errorf("Expected NewFilter for case #%d to fail", i)
		}
	}
}

// TestInvalidGetLogsRequest tests invalid getLogs requests
func TestInvalidGetLogsRequest(t *testing.T) {
	t.Parallel()

	var (
		genesis = &core.Genesis{
			Config:  params.TestChainConfig,
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		db, blocks, _    = core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 10, func(i int, gen *core.BlockGen) {})
		_, sys           = newTestFilterSystem(db, Config{LogQueryLimit: 10})
		api              = NewFilterAPI(sys, true)
		blockHash        = blocks[0].Hash()
		unknownBlockHash = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	)

	api.SetChainConfig(params.BorUnittestChainConfig)

	// Insert the blocks into the chain so filter can look them up
	blockchain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	if n, err := blockchain.InsertChain(blocks, false); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	type testcase struct {
		f   FilterCriteria
		err error
	}
	testCases := []testcase{
		{
			f:   FilterCriteria{BlockHash: &blockHash, FromBlock: big.NewInt(100)},
			err: errBlockHashWithRange,
		},
		{
			f:   FilterCriteria{BlockHash: &blockHash, ToBlock: big.NewInt(500)},
			err: errBlockHashWithRange,
		},
		{
			f:   FilterCriteria{BlockHash: &blockHash, FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64())},
			err: errBlockHashWithRange,
		},
		{
			f:   FilterCriteria{BlockHash: &unknownBlockHash},
			err: errUnknownBlock,
		},
		{
			f:   FilterCriteria{BlockHash: &blockHash, Topics: [][]common.Hash{{}, {}, {}, {}, {}}},
			err: errExceedMaxTopics,
		},
		{
			f:   FilterCriteria{BlockHash: &blockHash, Topics: [][]common.Hash{{}, {}, {}, {}, {}}},
			err: errExceedMaxTopics,
		},
		{
			f:   FilterCriteria{BlockHash: &blockHash, Addresses: make([]common.Address, api.logQueryLimit+1)},
			err: errExceedLogQueryLimit,
		},
	}

	for i, test := range testCases {
		_, err := api.GetLogs(t.Context(), test.f)
		if !errors.Is(err, test.err) {
			t.Errorf("case %d: wrong error: %q\nwant: %q", i, err, test.err)
		}
	}
}

// TestInvalidGetRangeLogsRequest tests getLogs with invalid block range
func TestInvalidGetRangeLogsRequest(t *testing.T) {
	t.Parallel()

	var (
		db     = rawdb.NewMemoryDatabase()
		_, sys = newTestFilterSystem(db, Config{})
		api    = NewFilterAPI(sys, true)
	)

	api.SetChainConfig(params.BorTestChainConfig)

	if _, err := api.GetLogs(t.Context(), FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(1)}); err != errInvalidBlockRange {
		t.Errorf("Expected Logs for invalid range return error, but got: %v", err)
	}
}

func TestGetLogsAutoMergesPreMadhugiriBorLogs(t *testing.T) {
	t.Parallel()

	harness := newHistoricalBorLogsHarness(t, false)
	preBlockHash := harness.preBlock.Hash()
	missingBorBlockHash := harness.missingBorBlock.Hash()

	logs, err := harness.api.GetLogs(t.Context(), FilterCriteria{
		BlockHash: &preBlockHash,
	})
	if err != nil {
		t.Fatalf("GetLogs blockHash returned error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 merged pre-fork bor log, got %d", len(logs))
	}
	if logs[0].Address != harness.preBorAddr {
		t.Fatalf("expected pre-fork bor log address %s, got %s", harness.preBorAddr.Hex(), logs[0].Address.Hex())
	}
	if len(logs[0].Topics) != 1 || logs[0].Topics[0] != harness.preBorTopic {
		t.Fatalf("expected pre-fork bor log topic %s, got %v", harness.preBorTopic.Hex(), logs[0].Topics)
	}

	logs, err = harness.api.GetLogs(t.Context(), FilterCriteria{
		BlockHash: &missingBorBlockHash,
	})
	if err != nil {
		t.Fatalf("GetLogs missing-sidecar blockHash returned error: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs when pre-fork bor sidecar data is absent, got %d", len(logs))
	}
}

func TestGetLogsKeepsPostMadhugiriResultsCanonical(t *testing.T) {
	t.Parallel()

	harness := newHistoricalBorLogsHarness(t, false)
	postBlockHash := harness.postBlock.Hash()

	logs, err := harness.api.GetLogs(t.Context(), FilterCriteria{
		BlockHash: &postBlockHash,
	})
	if err != nil {
		t.Fatalf("GetLogs post-fork blockHash returned error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected only canonical post-fork log, got %d", len(logs))
	}
	if logs[0].Address != harness.postAddr {
		t.Fatalf("expected post-fork canonical log address %s, got %s", harness.postAddr.Hex(), logs[0].Address.Hex())
	}

	logs, err = harness.api.GetLogs(t.Context(), FilterCriteria{
		FromBlock: new(big.Int).SetUint64(harness.preBlock.NumberU64()),
		ToBlock:   big.NewInt(rpc.LatestBlockNumber.Int64()),
	})
	if err != nil {
		t.Fatalf("GetLogs range returned error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected pre-fork bor log plus post-fork canonical log, got %d", len(logs))
	}
	if logs[0].Address != harness.preBorAddr {
		t.Fatalf("expected first log to be pre-fork bor log %s, got %s", harness.preBorAddr.Hex(), logs[0].Address.Hex())
	}
	if logs[1].Address != harness.postAddr {
		t.Fatalf("expected second log to be post-fork canonical log %s, got %s", harness.postAddr.Hex(), logs[1].Address.Hex())
	}
	for _, log := range logs {
		if log.Address == harness.postBorAddr {
			t.Fatalf("unexpected post-fork bor sidecar log leaked into standard historical query")
		}
	}
}

func TestGetFilterLogsAutoMergesPreMadhugiriBorLogs(t *testing.T) {
	t.Parallel()

	harness := newHistoricalBorLogsHarness(t, false)
	preBlockHash := harness.preBlock.Hash()

	id, err := harness.api.NewFilter(FilterCriteria{
		BlockHash: &preBlockHash,
	})
	if err != nil {
		t.Fatalf("NewFilter blockHash returned error: %v", err)
	}
	t.Cleanup(func() {
		harness.api.UninstallFilter(id)
	})

	logs, err := harness.api.GetFilterLogs(t.Context(), id)
	if err != nil {
		t.Fatalf("GetFilterLogs blockHash returned error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 merged pre-fork bor log from filter id, got %d", len(logs))
	}
	if logs[0].Address != harness.preBorAddr {
		t.Fatalf("expected pre-fork bor log address %s, got %s", harness.preBorAddr.Hex(), logs[0].Address.Hex())
	}
}

func TestGetFilterLogsKeepsPostMadhugiriResultsCanonical(t *testing.T) {
	t.Parallel()

	harness := newHistoricalBorLogsHarness(t, false)

	id, err := harness.api.NewFilter(FilterCriteria{
		FromBlock: new(big.Int).SetUint64(harness.preBlock.NumberU64()),
		ToBlock:   big.NewInt(rpc.LatestBlockNumber.Int64()),
	})
	if err != nil {
		t.Fatalf("NewFilter range returned error: %v", err)
	}
	t.Cleanup(func() {
		harness.api.UninstallFilter(id)
	})

	logs, err := harness.api.GetFilterLogs(t.Context(), id)
	if err != nil {
		t.Fatalf("GetFilterLogs range returned error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected pre-fork bor log plus post-fork canonical log from filter id, got %d", len(logs))
	}
	if logs[0].Address != harness.preBorAddr {
		t.Fatalf("expected first filter log to be pre-fork bor log %s, got %s", harness.preBorAddr.Hex(), logs[0].Address.Hex())
	}
	if logs[1].Address != harness.postAddr {
		t.Fatalf("expected second filter log to be post-fork canonical log %s, got %s", harness.postAddr.Hex(), logs[1].Address.Hex())
	}
	for _, log := range logs {
		if log.Address == harness.postBorAddr {
			t.Fatalf("unexpected post-fork bor sidecar log leaked into filter-id historical query")
		}
	}
}

// TestExceedLogQueryLimit tests getLogs with too many addresses or topics
func TestExceedLogQueryLimit(t *testing.T) {
	t.Parallel()

	// Test with custom config (LogQueryLimit = 5 for easier testing)
	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{LogQueryLimit: 5})
		api          = NewFilterAPI(sys, true)
		gspec        = &core.Genesis{
			Config:  params.TestChainConfig,
			Alloc:   types.GenesisAlloc{},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
	)

	_, err := gspec.Commit(db, triedb.NewDatabase(db, nil))
	if err != nil {
		t.Fatal(err)
	}
	chain, _ := core.GenerateChain(gspec.Config, gspec.ToBlock(), ethash.NewFaker(), db, 1000, func(i int, gen *core.BlockGen) {})

	options := core.DefaultConfig().WithStateScheme(rawdb.HashScheme)
	options.TxLookupLimit = 0 // index all txs
	bc, err := core.NewBlockChain(db, gspec, ethash.NewFaker(), options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bc.InsertChain(chain[:600], false)
	if err != nil {
		t.Fatal(err)
	}

	backend.startFilterMaps(200, false, filtermaps.RangeTestParams)
	defer backend.stopFilterMaps()

	addresses := make([]common.Address, 6)
	for i := range addresses {
		addresses[i] = common.HexToAddress("0x1234567890123456789012345678901234567890")
	}

	topics := make([]common.Hash, 6)
	for i := range topics {
		topics[i] = common.HexToHash("0x123456789012345678901234567890123456789001234567890012345678901234")
	}

	// Test that 5 addresses do not result in error
	// Add FromBlock and ToBlock to make it similar to other invalid tests
	if _, err := api.GetLogs(t.Context(), FilterCriteria{
		FromBlock: big.NewInt(0),
		ToBlock:   big.NewInt(100),
		Addresses: addresses[:5],
	}); err != nil {
		t.Errorf("Expected GetLogs with 5 addresses to return with no error, got: %v", err)
	}

	// Test that 6 addresses fails with correct error
	if _, err := api.GetLogs(t.Context(), FilterCriteria{
		FromBlock: big.NewInt(0),
		ToBlock:   big.NewInt(100),
		Addresses: addresses,
	}); err != errExceedLogQueryLimit {
		t.Errorf("Expected GetLogs with 6 addresses to return errExceedLogQueryLimit, got: %v", err)
	}

	// Test that 5 topics at one position do not result in error
	if _, err := api.GetLogs(t.Context(), FilterCriteria{
		FromBlock: big.NewInt(0),
		ToBlock:   big.NewInt(100),
		Addresses: addresses[:1],
		Topics:    [][]common.Hash{topics[:5]},
	}); err != nil {
		t.Errorf("Expected GetLogs with 5 topics at one position to return with no error, got: %v", err)
	}

	// Test that 6 topics at one position fails with correct error
	if _, err := api.GetLogs(t.Context(), FilterCriteria{
		FromBlock: big.NewInt(0),
		ToBlock:   big.NewInt(100),
		Addresses: addresses[:1],
		Topics:    [][]common.Hash{topics},
	}); err != errExceedLogQueryLimit {
		t.Errorf("Expected GetLogs with 6 topics at one position to return errExceedLogQueryLimit, got: %v", err)
	}
}

// TestLogFilter tests whether log filters match the correct logs that are posted to the event feed.
func TestLogFilter(t *testing.T) {
	t.Parallel()

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, true)

		firstAddr      = common.HexToAddress("0x1111111111111111111111111111111111111111")
		secondAddr     = common.HexToAddress("0x2222222222222222222222222222222222222222")
		thirdAddress   = common.HexToAddress("0x3333333333333333333333333333333333333333")
		notUsedAddress = common.HexToAddress("0x9999999999999999999999999999999999999999")
		firstTopic     = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
		secondTopic    = common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
		notUsedTopic   = common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

		// posted twice, once as regular logs and once as pending logs.
		allLogs = []*types.Log{
			{Address: firstAddr},
			{Address: firstAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 1},
			{Address: secondAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 1},
			{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 2},
			{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 3},
		}

		testCases = []struct {
			crit     FilterCriteria
			expected []*types.Log
			id       rpc.ID
		}{
			// match all
			0: {FilterCriteria{}, allLogs, ""},
			// match none due to no matching addresses
			1: {FilterCriteria{Addresses: []common.Address{{}, notUsedAddress}, Topics: [][]common.Hash{nil}}, []*types.Log{}, ""},
			// match logs based on addresses, ignore topics
			2: {FilterCriteria{Addresses: []common.Address{firstAddr}}, allLogs[:2], ""},
			// match none due to no matching topics (match with address)
			3: {FilterCriteria{Addresses: []common.Address{secondAddr}, Topics: [][]common.Hash{{notUsedTopic}}}, []*types.Log{}, ""},
			// match logs based on addresses and topics
			4: {FilterCriteria{Addresses: []common.Address{thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, allLogs[3:5], ""},
			// match logs based on multiple addresses and "or" topics
			5: {FilterCriteria{Addresses: []common.Address{secondAddr, thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, allLogs[2:5], ""},
			// all "mined" logs with block num >= 2
			6: {FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, allLogs[3:], ""},
			// all "mined" logs
			7: {FilterCriteria{ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, allLogs, ""},
			// all "mined" logs with 1>= block num <=2 and topic secondTopic
			8: {FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(2), Topics: [][]common.Hash{{secondTopic}}}, allLogs[3:4], ""},
			// match all logs due to wildcard topic
			9: {FilterCriteria{Topics: [][]common.Hash{nil}}, allLogs[1:], ""},
		}
	)

	// create all filters
	for i := range testCases {
		testCases[i].id, _ = api.NewFilter(testCases[i].crit)
	}

	// raise events
	time.Sleep(1 * time.Second)

	if nsend := backend.logsFeed.Send(allLogs); nsend == 0 {
		t.Fatal("Logs event not delivered")
	}

	if nsend := backend.pendingLogsFeed.Send(allLogs); nsend == 0 {
		t.Fatal("Pending logs event not delivered")
	}

	for i, tt := range testCases {
		var fetched []*types.Log

		timeout := time.Now().Add(1 * time.Second)

		for { // fetch all expected logs
			results, err := api.GetFilterChanges(tt.id)
			if err != nil {
				t.Fatalf("test %d: unable to fetch logs: %v", i, err)
			}

			fetched = append(fetched, results.([]*types.Log)...)
			if len(fetched) >= len(tt.expected) {
				break
			}
			// check timeout
			if time.Now().After(timeout) {
				break
			}

			time.Sleep(100 * time.Millisecond)
		}

		if len(fetched) != len(tt.expected) {
			t.Errorf("invalid number of logs for case %d, want %d log(s), got %d", i, len(tt.expected), len(fetched))
			return
		}

		for l := range fetched {
			if fetched[l].Removed {
				t.Errorf("expected log not to be removed for log %d in case %d", l, i)
			}

			if !reflect.DeepEqual(fetched[l], tt.expected[l]) {
				t.Errorf("invalid log on index %d for case %d", l, i)
			}
		}
	}
}

// TestPendingTxFilterDeadlock tests if the event loop hangs when pending
// txes arrive at the same time that one of multiple filters is timing out.
// Please refer to #22131 for more details.
func TestPendingTxFilterDeadlock(t *testing.T) {
	t.Parallel()

	timeout := 100 * time.Millisecond

	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{Timeout: timeout})
		api          = NewFilterAPI(sys, true)
		done         = make(chan struct{})
	)

	go func() {
		// Bombard feed with txes until signal was received to stop
		i := uint64(0)

		for {
			select {
			case <-done:
				return
			default:
			}

			tx := types.NewTransaction(i, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil)
			backend.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})

			i++
		}
	}()

	// Create a bunch of filters that will
	// timeout either in 100ms or 200ms
	subs := make([]*Subscription, 20)
	for i := range subs {
		fid := api.NewPendingTransactionFilter(nil)
		api.filtersMu.Lock()
		f, ok := api.filters[fid]
		api.filtersMu.Unlock()
		if !ok {
			t.Fatalf("Filter %s should exist", fid)
		}
		subs[i] = f.s
		// Wait for at least one tx to arrive in filter
		for {
			hashes, err := api.GetFilterChanges(fid)
			if err != nil {
				t.Fatalf("Filter should exist: %v\n", err)
			}

			if len(hashes.([]common.Hash)) > 0 {
				break
			}

			runtime.Gosched()
		}
	}

	// Wait until filters have timed out and have been uninstalled.
	for _, sub := range subs {
		select {
		case <-sub.Err():
		case <-time.After(1 * time.Second):
			t.Fatalf("Filter timeout is hanging")
		}
	}
}

// TestTransactionReceiptsSubscription tests the transaction receipts subscription functionality
func TestTransactionReceiptsSubscription(t *testing.T) {
	t.Parallel()

	const txNum = 5

	// Setup test environment
	var (
		db           = rawdb.NewMemoryDatabase()
		backend, sys = newTestFilterSystem(db, Config{})
		api          = NewFilterAPI(sys, true)
		key1, _      = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr1        = crypto.PubkeyToAddress(key1.PublicKey)
		signer       = types.NewLondonSigner(big.NewInt(1))
		genesis      = &core.Genesis{
			Alloc:   types.GenesisAlloc{addr1: {Balance: big.NewInt(1000000000000000000)}}, // 1 ETH
			Config:  params.TestChainConfig,
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		_, chain, _ = core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 1, func(i int, gen *core.BlockGen) {
			// Add transactions to the block
			for j := 0; j < txNum; j++ {
				toAddr := common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268")
				tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
					Nonce:    uint64(j),
					GasPrice: gen.BaseFee(),
					Gas:      21000,
					To:       &toAddr,
					Value:    big.NewInt(1000),
					Data:     nil,
				}), signer, key1)
				gen.AddTx(tx)
			}
		})
	)

	// Insert the blocks into the chain
	blockchain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	if n, err := blockchain.InsertChain(chain, false); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	// Prepare test data
	receipts := blockchain.GetReceiptsByHash(chain[0].Hash())
	if receipts == nil {
		t.Fatalf("failed to get receipts")
	}

	chainEvent := core.ChainEvent{
		Header:       chain[0].Header(),
		Receipts:     receipts,
		Transactions: chain[0].Transactions(),
	}

	txHashes := make([]common.Hash, txNum)
	for i := 0; i < txNum; i++ {
		txHashes[i] = chain[0].Transactions()[i].Hash()
	}

	testCases := []struct {
		name                    string
		filterTxHashes          []common.Hash
		expectedReceiptTxHashes []common.Hash
		expectError             bool
	}{
		{
			name:                    "no filter - should return all receipts",
			filterTxHashes:          nil,
			expectedReceiptTxHashes: txHashes,
			expectError:             false,
		},
		{
			name:                    "single tx hash filter",
			filterTxHashes:          []common.Hash{txHashes[0]},
			expectedReceiptTxHashes: []common.Hash{txHashes[0]},
			expectError:             false,
		},
		{
			name:                    "multiple tx hashes filter",
			filterTxHashes:          []common.Hash{txHashes[0], txHashes[1], txHashes[2]},
			expectedReceiptTxHashes: []common.Hash{txHashes[0], txHashes[1], txHashes[2]},
			expectError:             false,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			receiptsChan := make(chan []*ReceiptWithTx)
			sub := api.events.SubscribeTransactionReceipts(tc.filterTxHashes, receiptsChan)

			// Send chain event
			backend.chainFeed.Send(chainEvent)

			// Wait for receipts
			timeout := time.After(1 * time.Second)
			var receivedReceipts []*types.Receipt
			for {
				select {
				case receiptsWithTx := <-receiptsChan:
					for _, receiptWithTx := range receiptsWithTx {
						receivedReceipts = append(receivedReceipts, receiptWithTx.Receipt)
					}
				case <-timeout:
					t.Fatalf("timeout waiting for receipts")
				}
				if len(receivedReceipts) >= len(tc.expectedReceiptTxHashes) {
					break
				}
			}

			// Verify receipt count
			if len(receivedReceipts) != len(tc.expectedReceiptTxHashes) {
				t.Errorf("Expected %d receipts, got %d", len(tc.expectedReceiptTxHashes), len(receivedReceipts))
			}

			// Verify specific transaction hashes are present
			if tc.expectedReceiptTxHashes != nil {
				receivedHashes := make(map[common.Hash]bool)
				for _, receipt := range receivedReceipts {
					receivedHashes[receipt.TxHash] = true
				}

				for _, expectedHash := range tc.expectedReceiptTxHashes {
					if !receivedHashes[expectedHash] {
						t.Errorf("Expected receipt for tx %x not found", expectedHash)
					}
				}
			}

			// Cleanup
			sub.Unsubscribe()
			<-sub.Err()
		})
	}
}

// TestRangeLimit verifies that RangeLimit is enforced for both GetLogs and GetBorBlockLogs.
func TestRangeLimit(t *testing.T) {
	t.Parallel()

	const limit = 100

	gspec := &core.Genesis{
		Config:  params.TestChainConfig,
		Alloc:   types.GenesisAlloc{},
		BaseFee: big.NewInt(params.InitialBaseFee),
	}

	// Shared setup: small chain with limit=100.
	db := rawdb.NewMemoryDatabase()
	_, err := gspec.Commit(db, triedb.NewDatabase(db, nil))
	if err != nil {
		t.Fatal(err)
	}
	chain, _ := core.GenerateChain(gspec.Config, gspec.ToBlock(), ethash.NewFaker(), db, 200, func(i int, gen *core.BlockGen) {})
	bc, err := core.NewBlockChain(db, gspec, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bc.InsertChain(chain[:200], false); err != nil {
		t.Fatal(err)
	}

	backend, sys := newTestFilterSystem(db, Config{RangeLimit: limit})
	api := NewFilterAPI(sys, true)
	api.SetChainConfig(params.BorUnittestChainConfig)

	backend.startFilterMaps(0, false, filtermaps.RangeTestParams)
	defer backend.stopFilterMaps()

	// Numeric ranges exceeding the limit must be rejected.
	overLimitCases := []FilterCriteria{
		{FromBlock: big.NewInt(0), ToBlock: big.NewInt(int64(limit) + 1)},
		{FromBlock: big.NewInt(0), ToBlock: big.NewInt(10000)},
	}

	for i, crit := range overLimitCases {
		_, err := api.GetLogs(t.Context(), crit)
		if !errors.Is(err, errExceedBlockRangeLimit) {
			t.Errorf("GetLogs over-limit case %d: got %v, want errExceedBlockRangeLimit", i, err)
		}

		_, err = api.GetBorBlockLogs(t.Context(), crit)
		if !errors.Is(err, errExceedBlockRangeLimit) {
			t.Errorf("GetBorBlockLogs over-limit case %d: got %v, want errExceedBlockRangeLimit", i, err)
		}
	}

	// Symbolic "latest" as toBlock must also be caught when the chain is longer than the limit.
	// The chain has 200 blocks, limit is 100, so fromBlock=0 to toBlock=latest spans 200 > 100.
	latestCrit := FilterCriteria{FromBlock: big.NewInt(0), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}
	if _, err := api.GetLogs(t.Context(), latestCrit); !errors.Is(err, errExceedBlockRangeLimit) {
		t.Errorf("GetLogs with fromBlock=0 toBlock=latest on 200-block chain: got %v, want errExceedBlockRangeLimit", err)
	}
	if _, err := api.GetBorBlockLogs(t.Context(), latestCrit); !errors.Is(err, errExceedBlockRangeLimit) {
		t.Errorf("GetBorBlockLogs with fromBlock=0 toBlock=latest on 200-block chain: got %v, want errExceedBlockRangeLimit", err)
	}

	// GetFilterLogs (eth_getFilterLogs) must also respect the limit.
	filterID, err := api.NewFilter(FilterCriteria{
		FromBlock: big.NewInt(0),
		ToBlock:   big.NewInt(int64(limit) + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetFilterLogs(t.Context(), filterID); !errors.Is(err, errExceedBlockRangeLimit) {
		t.Errorf("GetFilterLogs over-limit: got %v, want errExceedBlockRangeLimit", err)
	}

	// A range exactly at the limit (end - begin == limit) must succeed.
	atLimitCrit := FilterCriteria{FromBlock: big.NewInt(0), ToBlock: big.NewInt(int64(limit))}
	if _, err := api.GetLogs(t.Context(), atLimitCrit); errors.Is(err, errExceedBlockRangeLimit) {
		t.Error("GetLogs at exact limit should not return errExceedBlockRangeLimit")
	}
	if _, err := api.GetBorBlockLogs(t.Context(), atLimitCrit); errors.Is(err, errExceedBlockRangeLimit) {
		t.Error("GetBorBlockLogs at exact limit should not return errExceedBlockRangeLimit")
	}

	// With RangeLimit=0 (unlimited), large ranges must not trigger errExceedBlockRangeLimit.
	db2 := rawdb.NewMemoryDatabase()
	if _, err = gspec.Commit(db2, triedb.NewDatabase(db2, nil)); err != nil {
		t.Fatal(err)
	}
	chain2, _ := core.GenerateChain(gspec.Config, gspec.ToBlock(), ethash.NewFaker(), db2, 200, func(i int, gen *core.BlockGen) {})
	bc2, err := core.NewBlockChain(db2, gspec, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bc2.InsertChain(chain2[:200], false); err != nil {
		t.Fatal(err)
	}

	backend2, sys2 := newTestFilterSystem(db2, Config{RangeLimit: 0})
	api2 := NewFilterAPI(sys2, true)
	api2.SetChainConfig(params.BorUnittestChainConfig)

	backend2.startFilterMaps(0, false, filtermaps.RangeTestParams)
	defer backend2.stopFilterMaps()

	bigRangeCrit := FilterCriteria{FromBlock: big.NewInt(0), ToBlock: big.NewInt(int64(limit) * 10)}
	if _, err := api2.GetLogs(t.Context(), bigRangeCrit); errors.Is(err, errExceedBlockRangeLimit) {
		t.Error("GetLogs with RangeLimit=0 should not return errExceedBlockRangeLimit")
	}
	if _, err := api2.GetBorBlockLogs(t.Context(), bigRangeCrit); errors.Is(err, errExceedBlockRangeLimit) {
		t.Error("GetBorBlockLogs with RangeLimit=0 should not return errExceedBlockRangeLimit")
	}

	// Suppress unused variable warnings.
	_ = bc
	_ = bc2
}
