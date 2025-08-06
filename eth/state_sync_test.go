// eth/state_sync_test.go
package eth

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	gb "github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/tests/bor/mocks"
	"github.com/golang/mock/gomock"
)

var (
	borTxLookupPrefixTest    = []byte(borTxLookupPrefixStrTest)
	borTxLookupPrefixStrTest = "matic-bor-tx-lookup-"
)

// testing Purpose
func borTxLookupKeyTest(hash common.Hash) []byte {
	return append(borTxLookupPrefixTest, hash.Bytes()...)
}

func TestCheckStateSyncConsistency_LargeRange(t *testing.T) {

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// --- test parameters ---
	startBlock := uint64(31)
	endBlock := uint64(2005)
	currentBlock := uint64(3008)
	blockDur := 2 * time.Second
	fetchLimit := uint64(50)
	missingStateSyncId := uint(30)

	// baseTime = epoch (for simplicity)
	baseTime := time.Unix(0, 0)

	// compute the header timestamps
	startTime := baseTime.Add(time.Duration(startBlock) * blockDur)
	endTime := baseTime.Add(time.Duration(endBlock) * blockDur)
	startHdr := &types.Header{Time: uint64(startTime.Unix())}
	endHdr := &types.Header{Time: uint64(endTime.Unix())}
	currentHdr := &types.Header{
		Number: big.NewInt(int64(currentBlock)),
	}

	// --- Mock HeaderProvider ---
	mockHP := mocks.NewMockHeaderProvider(ctrl)
	mockHP.
		EXPECT().
		HeaderByNumber(gomock.Any(), rpc.BlockNumber(startBlock)).
		Return(startHdr, nil)
	mockHP.
		EXPECT().
		HeaderByNumber(gomock.Any(), rpc.BlockNumber(endBlock)).
		Return(endHdr, nil)
	mockHP.
		EXPECT().
		CurrentHeader().
		Return(currentHdr).
		Times(2)

	// --- Mock GenesisContract ---
	lastStateID := currentBlock / 16
	mockGen := bor.NewMockGenesisContract(ctrl)
	mockGen.
		EXPECT().
		LastStateId(
			gomock.Any(),
			currentBlock,
			currentHdr.Hash(),
		).
		Return(big.NewInt(int64(lastStateID)), nil)

	// --- Mock Heimdall client ---
	mockHeimdall := mocks.NewMockIHeimdallClient(ctrl)

	// 1) StateSyncEventById → time = base + id*16*blockDur + 1s
	mockHeimdall.
		EXPECT().
		StateSyncEventById(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id uint64) (*clerk.EventRecordWithTime, error) {
			respTime := baseTime.Add(time.Duration(id*16) * blockDur).Add(time.Second)
			return &clerk.EventRecordWithTime{Time: respTime, EventRecord: clerk.EventRecord{ID: id}}, nil
		}).
		AnyTimes()

	// 2) StateFetchLimit = 50
	mockHeimdall.
		EXPECT().
		StateFetchLimit().
		Return(fetchLimit).
		AnyTimes()

	// 3) StateSyncEventsList pages through [fromId ... fromId+49], stopping when time > endTime
	mockHeimdall.
		EXPECT().
		StateSyncEventsList(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fromId uint64) ([]*clerk.EventRecordWithTime, error) {
			var out []*clerk.EventRecordWithTime
			for id := fromId; id < fromId+fetchLimit; id++ {
				respTime := baseTime.Add(time.Duration(id*16) * blockDur).Add(time.Second)
				out = append(out, &clerk.EventRecordWithTime{
					EventRecord: clerk.EventRecord{ID: id, TxHash: common.BigToHash(big.NewInt(int64(id)))},
					Time:        respTime,
				})
			}
			return out, nil
		}).
		AnyTimes()

	// assemble a Bor stub with both mocks
	borStub := &gb.Bor{
		GenesisContractsClient: mockGen,
		HeimdallClient:         mockHeimdall,
	}

	// empty DB ⇒ every event is “missing”
	db := rawdb.NewMemoryDatabase()

	// writing state sync txs for all txs except 1
	for i := 0; i < 300; i++ {
		if i == int(missingStateSyncId) {
			continue
		}
		db.Put(borTxLookupKeyTest(common.BigToHash(big.NewInt(int64(i)))), common.Hex2Bytes("00AA00"))
	}

	eth := &Ethereum{chainDb: db}
	missing, err := eth.checkStateSyncConsistency(startBlock, endBlock, mockHP, borStub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(missing) != 1 {
		t.Errorf("wrong count: got %d, want %d", len(missing), 1)
	}

	// check hash
	if missing[0] != common.BigToHash(big.NewInt(int64(missingStateSyncId))) {
		t.Errorf("only hash: got %s, want %s",
			missing[0].Hex(),
			common.BigToHash(big.NewInt(int64(missingStateSyncId))).Hex(),
		)
	}
}
