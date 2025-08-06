// eth/state_sync_test.go
package eth

import (
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

func TestCheckStateSyncConsistency_WithMocks(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t0 := time.Date(2025, 8, 6, 0, 0, 0, 0, time.UTC)
	startHdr := &types.Header{Number: big.NewInt(1), Time: uint64(t0.Unix())}
	endHdr := &types.Header{Number: big.NewInt(2000), Time: uint64(t0.Add(time.Hour).Unix())}
	currentHdr := &types.Header{
		Number: big.NewInt(3000),
	}

	// mocks
	mockHP := mocks.NewMockHeaderProvider(ctrl)
	mockHP.
		EXPECT().
		HeaderByNumber(gomock.Any(), rpc.BlockNumber(1)).
		Return(startHdr, nil)
	mockHP.
		EXPECT().
		HeaderByNumber(gomock.Any(), rpc.BlockNumber(2)).
		Return(endHdr, nil)
	mockHP.
		EXPECT().
		CurrentHeader().
		Return(currentHdr)

	mockGen := bor.NewMockGenesisContract(ctrl)
	mockGen.
		EXPECT().
		LastStateId(
			gomock.Any(),
			currentHdr.Number.Uint64(),
			currentHdr.Hash(),
		).
		Return(big.NewInt(0), nil)

	mockHeimdall := mocks.NewMockIHeimdallClient(ctrl)
	// 1) findBoundaryStateSync(0) → t0+10m
	mockHeimdall.
		EXPECT().
		StateSyncEventById(gomock.Any(), uint64(0)).
		Return(&clerk.EventRecordWithTime{Time: t0.Add(10 * time.Minute)}, nil)
	// 2) throttle concurrency
	mockHeimdall.
		EXPECT().
		StateFetchLimit().
		Return(uint64(1))
	// 3) one missing tx
	expectedTx := common.HexToHash("0xDEADBEEF")
	mockHeimdall.
		EXPECT().
		StateSyncEventsList(gomock.Any(), uint64(0)).
		Return([]*clerk.EventRecordWithTime{{
			EventRecord: clerk.EventRecord{TxHash: expectedTx},
			Time:        t0.Add(10 * time.Minute),
		}}, nil)

	borStub := &gb.Bor{
		GenesisContractsClient: mockGen,
		HeimdallClient:         mockHeimdall,
	}

	db := rawdb.NewMemoryDatabase()

	// invoke SUT
	eth := &Ethereum{chainDb: db}
	missing, err := eth.checkStateSyncConsistency(1, 2, mockHP, borStub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// assert
	if len(missing) != 1 || missing[0] != expectedTx {
		t.Fatalf("got %v, want [%s]", missing, expectedTx.Hex())
	}
}
