package filters

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"

	"go.uber.org/mock/gomock"
)

var (
	key1, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	addr    = crypto.PubkeyToAddress(key1.PublicKey)
)

func newTestHeader(blockNumber uint) *types.Header {
	return &types.Header{
		Number: big.NewInt(int64(blockNumber)),
	}
}

func newTestReceipt(contractAddr common.Address, topicAddress common.Hash) *types.Receipt {
	receipt := types.NewReceipt(nil, false, 0)
	receipt.Logs = []*types.Log{
		{
			Address: contractAddr,
			Topics:  []common.Hash{topicAddress},
		},
	}

	return receipt
}

func (backend *MockBackend) expectBorReceiptsFromMock(hashes []*common.Hash) {
	for _, h := range hashes {
		if h == nil {
			backend.EXPECT().GetBorBlockReceipt(gomock.Any(), gomock.Any()).Return(nil, nil)
			continue
		}

		backend.EXPECT().GetBorBlockReceipt(gomock.Any(), gomock.Any()).Return(newTestReceipt(addr, *h), nil)
	}
}

func TestBorFilters(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		hash1 = common.BytesToHash([]byte("topic1"))
		hash2 = common.BytesToHash([]byte("topic2"))
		hash3 = common.BytesToHash([]byte("topic3"))
		hash4 = common.BytesToHash([]byte("topic4"))
		db    = NewMockDatabase(ctrl)

		testBorConfig = params.TestChainConfig.Bor
	)

	backend := NewMockBackend(ctrl)

	// should return the following at all times
	backend.EXPECT().ChainDb().Return(db).AnyTimes()
	backend.EXPECT().HeaderByNumber(gomock.Any(), gomock.Any()).Return(newTestHeader(1), nil).AnyTimes()

	// Block 1
	backend.expectBorReceiptsFromMock([]*common.Hash{nil, &hash1, &hash2, &hash3, &hash4})

	filter := NewBorBlockLogsRangeFilter(backend, testBorConfig, 0, 18, []common.Address{addr}, [][]common.Hash{{hash1, hash2, hash3, hash4}})
	logs, err := filter.Logs(t.Context())

	if err != nil {
		t.Error(err)
	}

	if len(logs) != 4 {
		t.Error("expected 4 log, got", len(logs))
	}

	// Block 2
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash3})

	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 990, 999, []common.Address{addr}, [][]common.Hash{{hash3}})
	logs, _ = filter.Logs(t.Context())

	if len(logs) != 1 {
		t.Error("expected 1 log, got", len(logs))
	}

	if len(logs) > 0 && logs[0].Topics[0] != hash3 {
		t.Errorf("expected log[0].Topics[0] to be %x, got %x", hash3, logs[0].Topics[0])
	}

	// Block 3
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash2, &hash3})

	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 992, 1000, []common.Address{addr}, [][]common.Hash{{hash3}})
	logs, _ = filter.Logs(t.Context())

	if len(logs) != 1 {
		t.Error("expected 1 log, got", len(logs))
	}

	if len(logs) > 0 && logs[0].Topics[0] != hash3 {
		t.Errorf("expected log[0].Topics[0] to be %x, got %x", hash3, logs[0].Topics[0])
	}

	// Block 4
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash2, nil, &hash3})

	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 1, 16, []common.Address{addr}, [][]common.Hash{{hash1, hash2}})

	logs, _ = filter.Logs(t.Context())

	if len(logs) != 2 {
		t.Error("expected 2 log, got", len(logs))
	}

	// Block 5
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash2, nil, &hash3, &hash4, nil})

	failHash := common.BytesToHash([]byte("fail"))
	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 0, 20, nil, [][]common.Hash{{failHash}})

	logs, _ = filter.Logs(t.Context())
	if len(logs) != 0 {
		t.Error("expected 0 log, got", len(logs))
	}

	// Block 6
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash2, nil, &hash3, &hash4, nil})

	failAddr := common.BytesToAddress([]byte("failmenow"))
	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 0, 20, []common.Address{failAddr}, nil)

	logs, _ = filter.Logs(t.Context())
	if len(logs) != 0 {
		t.Error("expected 0 log, got", len(logs))
	}

	// Block 7
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash2, nil, &hash3, &hash4, nil})

	filter = NewBorBlockLogsRangeFilter(backend, testBorConfig, 0, 20, nil, [][]common.Hash{{failHash}, {hash1}})

	logs, _ = filter.Logs(t.Context())
	if len(logs) != 0 {
		t.Error("expected 0 log, got", len(logs))
	}
}

// newCancellationTestFilter builds a bor range filter wired to a mock backend
// that counts per-block header reads inside the scan loop, returning the filter
// and a pointer to that counter. A test can then assert the loop bailed out
// early (counter == 0) instead of scanning the whole range. Only the per-block
// reads are counted: the single pre-loop head lookup uses rpc.LatestBlockNumber
// (negative), while loop reads use the concrete, non-negative block number.
func newCancellationTestFilter(t *testing.T) (*BorBlockLogsFilter, *int32) {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	db := NewMockDatabase(ctrl)
	backend := NewMockBackend(ctrl)

	loopBlockReads := new(int32)
	backend.EXPECT().ChainDb().Return(db).AnyTimes()
	backend.EXPECT().HeaderByNumber(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, number rpc.BlockNumber) (*types.Header, error) {
			if number >= 0 {
				atomic.AddInt32(loopBlockReads, 1)
			}
			return newTestHeader(1), nil
		}).AnyTimes()
	backend.EXPECT().GetBorBlockReceipt(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Range large enough that an unguarded loop would iterate hundreds of times.
	filter := NewBorBlockLogsRangeFilter(backend, params.TestChainConfig.Bor, 0, 4000, []common.Address{addr}, nil)

	return filter, loopBlockReads
}

// TestBorFilterHonorsContextCancellation asserts that a range scan stops as soon
// as the request context is cancelled (e.g. the RPC client disconnected, or a
// deadline fired) instead of scanning the whole range and keeping an RPC worker
// busy with work whose result nobody will read. This mirrors the cancellation
// check upstream go-ethereum performs in eth/filters/filter.go's unindexedLogs.
func TestBorFilterHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	filter, loopBlockReads := newCancellationTestFilter(t)

	// Cancel up-front: a scan that honors cancellation must bail out before
	// doing any per-block work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logs, err := filter.Logs(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got err=%v (loop ran %d block reads, ignoring cancellation)",
			err, atomic.LoadInt32(loopBlockReads))
	}
	if got := atomic.LoadInt32(loopBlockReads); got != 0 {
		t.Fatalf("expected 0 in-loop block reads after honoring cancellation, got %d", got)
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs on a cancelled scan, got %d", len(logs))
	}
}

// TestBorFilterHonorsContextDeadline asserts the scan also stops once the
// context deadline has passed. Because the loop now observes the context, any
// deadline attached upstream (an operator-configured RPC timeout, or a deadline
// the caller sets) bounds the scan -- previously the loop ran to completion
// regardless of the deadline.
func TestBorFilterHonorsContextDeadline(t *testing.T) {
	t.Parallel()

	filter, loopBlockReads := newCancellationTestFilter(t)

	// Deadline already in the past: the scan must stop before any per-block work.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	_, err := filter.Logs(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got err=%v (loop ran %d block reads, ignoring the deadline)",
			err, atomic.LoadInt32(loopBlockReads))
	}
	if got := atomic.LoadInt32(loopBlockReads); got != 0 {
		t.Fatalf("expected 0 in-loop block reads after honoring the deadline, got %d", got)
	}
}

func TestBorFilters_SkipOnBeginAtStateSync(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	db := NewMockDatabase(ctrl)
	backend := NewMockBackend(ctrl)

	// Clone test BorConfig and set the StateSync hard-fork block to 1000.
	testBorConfig := params.TestChainConfig.Bor
	cfgCopy := *testBorConfig
	cfgCopy.MadhugiriBlock = big.NewInt(1000)

	// Return DB and a sufficiently new head so range logic runs.
	backend.EXPECT().ChainDb().Return(db).AnyTimes()
	backend.EXPECT().HeaderByNumber(gomock.Any(), gomock.Any()).Return(newTestHeader(1500), nil).AnyTimes()

	// Begin at the StateSync block: after currentSprintEnd alignment, IsMadhugiri (begin) should be true
	// and the filter should return (nil, nil) without scanning any bor receipts.
	filter := NewBorBlockLogsRangeFilter(backend, &cfgCopy, 1000, 1100, nil, nil)
	logs, err := filter.Logs(t.Context())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logs != nil {
		t.Fatalf("expected nil logs due to early return at StateSync begin, got %v", len(logs))
	}
}

func TestBorFilters_TrimEndAtStateSync(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		hash1 = common.BytesToHash([]byte("topic1"))
		hash3 = common.BytesToHash([]byte("topic3"))
		db    = NewMockDatabase(ctrl)
	)

	backend := NewMockBackend(ctrl)

	// Clone test BorConfig and set the StateSync hard-fork block to 1000.
	testBorConfig := params.TestChainConfig.Bor
	cfgCopy := *testBorConfig
	cfgCopy.MadhugiriBlock = big.NewInt(1000)

	// Always returned
	backend.EXPECT().ChainDb().Return(db).AnyTimes()
	backend.EXPECT().HeaderByNumber(gomock.Any(), gomock.Any()).Return(newTestHeader(1500), nil).AnyTimes()

	// We’ll ask for [990, 1000]. Since 1000 is the StateSync block, the filter should trim f.end to 999
	// and only scan pre-fork bor receipts. Following the pattern in existing tests, two bor-receipt
	// checks will occur for this range, so prepare two receipts in sequence.
	backend.expectBorReceiptsFromMock([]*common.Hash{&hash1, &hash3})

	// Filter for only hash3 so we expect exactly 1 matching log from the second receipt.
	filter := NewBorBlockLogsRangeFilter(backend, &cfgCopy, 990, 1000, []common.Address{addr}, [][]common.Hash{{hash3}})
	logs, err := filter.Logs(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 log (trimmed to pre-fork, matched hash3), got %d", len(logs))
	}
	if logs[0].Topics[0] != hash3 {
		t.Fatalf("expected log topic %x, got %x", hash3, logs[0].Topics[0])
	}
}
