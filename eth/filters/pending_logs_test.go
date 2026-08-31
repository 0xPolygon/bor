package filters

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

type pendingSnapshotTestBackend struct {
	*testBackend
}

func (b *pendingSnapshotTestBackend) PendingBlockAndReceipts() (*types.Block, types.Receipts) {
	return b.pendingBlock, b.pendingReceipts
}

func TestGetLogsReadsPendingView(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	backend, _ := newTestFilterSystem(db, Config{})
	api := NewFilterAPI(NewFilterSystem(&pendingSnapshotTestBackend{backend}, Config{}), true)

	wanted := common.HexToAddress("0x1111111111111111111111111111111111111111")
	ignored := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := types.NewTransaction(0, common.Address{}, big.NewInt(0), 21_000, big.NewInt(1), nil)
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)}).WithBody(types.Body{Transactions: types.Transactions{tx}})
	backend.setPending(block, types.Receipts{{TxHash: tx.Hash(), Logs: []*types.Log{{Address: wanted}, {Address: ignored}}}})
	backend.pendingError = errors.New("split pending read")

	for _, crit := range []FilterCriteria{
		{
			FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()),
			ToBlock:   big.NewInt(rpc.PendingBlockNumber.Int64()),
			Addresses: []common.Address{wanted},
		},
		{Pending: true, Addresses: []common.Address{wanted}},
	} {
		logs, err := api.GetLogs(t.Context(), crit)
		if err != nil {
			t.Fatalf("GetLogs: %v", err)
		}
		if len(logs) != 1 || logs[0].Address != wanted {
			t.Fatalf("pending logs = %v", logs)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := api.GetLogs(cancelled, FilterCriteria{Pending: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pending logs error = %v", err)
	}
	backend.pendingReceipts = nil
	if _, err := api.GetLogs(t.Context(), FilterCriteria{Pending: true}); !errors.Is(err, errPendingLogsIncomplete) {
		t.Fatalf("incomplete pending logs error = %v", err)
	}
	backend.setPending(block, types.Receipts{{TxHash: common.HexToHash("0xdead")}})
	if _, err := api.GetLogs(t.Context(), FilterCriteria{Pending: true}); !errors.Is(err, errPendingLogsIncomplete) {
		t.Fatalf("mismatched pending receipt error = %v", err)
	}
}

func TestGetFilterLogsReadsPendingView(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	backend, sys := newTestFilterSystem(db, Config{})
	api := NewFilterAPI(sys, true)

	wanted := common.HexToAddress("0x1111111111111111111111111111111111111111")
	ignored := common.HexToAddress("0x2222222222222222222222222222222222222222")
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	backend.setPending(block, types.Receipts{{Logs: []*types.Log{{Address: wanted}, {Address: ignored}}}})

	for _, crit := range []FilterCriteria{
		{Pending: true, Addresses: []common.Address{wanted}},
		{FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), Addresses: []common.Address{wanted}},
	} {
		id, err := api.NewFilter(crit)
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		logs, err := api.GetFilterLogs(t.Context(), id)
		api.UninstallFilter(id)
		if err != nil {
			t.Fatalf("GetFilterLogs: %v", err)
		}
		if len(logs) != 1 || logs[0].Address != wanted {
			t.Fatalf("pending filter logs = %v", logs)
		}
	}
}

func TestPendingLogValidationAndFallback(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	backend, sys := newTestFilterSystem(db, Config{LogQueryLimit: 1})
	api := NewFilterAPI(sys, true)
	logs := make(chan []*types.Log)
	hash := common.HexToHash("0x1")

	tests := []struct {
		name string
		crit ethereum.FilterQuery
		want error
	}{
		{name: "topics", crit: ethereum.FilterQuery{Topics: make([][]common.Hash, maxTopics+1)}, want: errExceedMaxTopics},
		{name: "addresses", crit: ethereum.FilterQuery{Addresses: make([]common.Address, 2)}, want: errExceedLogQueryLimit},
		{name: "subtopics", crit: ethereum.FilterQuery{Topics: [][]common.Hash{{hash, hash}}}, want: errExceedLogQueryLimit},
		{name: "block hash", crit: ethereum.FilterQuery{BlockHash: &hash}, want: errInvalidBlockRange},
		{name: "from block", crit: ethereum.FilterQuery{FromBlock: big.NewInt(1)}, want: errInvalidBlockRange},
		{name: "to block", crit: ethereum.FilterQuery{ToBlock: big.NewInt(1)}, want: errInvalidBlockRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := api.events.SubscribePendingLogs(test.crit, logs); err != test.want {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	atLimit := ethereum.FilterQuery{
		Addresses: []common.Address{{}},
		Topics:    make([][]common.Hash, maxTopics),
	}
	for i := range atLimit.Topics {
		atLimit.Topics[i] = []common.Hash{hash}
	}
	sub, err := api.events.SubscribePendingLogs(atLimit, logs)
	if err != nil {
		t.Fatalf("criteria at limits: %v", err)
	}
	sub.Unsubscribe()

	pending := big.NewInt(rpc.PendingBlockNumber.Int64())
	invalidRanges := []FilterCriteria{
		{BlockHash: &hash, FromBlock: pending},
		{FromBlock: pending, ToBlock: big.NewInt(1)},
		{FromBlock: big.NewInt(1), ToBlock: pending},
		{ToBlock: pending},
	}
	for _, crit := range invalidRanges {
		if _, err := api.GetLogs(t.Context(), crit); err != errInvalidBlockRange {
			t.Fatalf("invalid pending range error = %v", err)
		}
	}
	if _, err := api.GetLogs(t.Context(), FilterCriteria{FromBlock: pending}); err != errPendingLogsUnsupported {
		t.Fatalf("missing pending view error = %v", err)
	}

	backend.setPending(types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)}), nil)
	backend.pendingError = errors.New("pending receipts unavailable")
	if _, err := api.GetLogs(t.Context(), FilterCriteria{FromBlock: pending}); err != backend.pendingError {
		t.Fatalf("pending receipt error = %v", err)
	}
	backend.pendingError = nil
	backend.pendingReceipts = types.Receipts{nil}
	if _, err := api.GetLogs(t.Context(), FilterCriteria{FromBlock: pending}); !errors.Is(err, errPendingLogsIncomplete) {
		t.Fatalf("nil pending receipt error = %v", err)
	}
}
