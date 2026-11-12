package heimdallgrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

type fakeClerkQueryClient struct {
	types.QueryClient
	req *types.RecordListWithTimeRequest
	res *types.RecordListWithTimeResponse
	err error
}

func (f *fakeClerkQueryClient) GetRecordListWithTime(_ context.Context, req *types.RecordListWithTimeRequest, _ ...grpc.CallOption) (*types.RecordListWithTimeResponse, error) {
	f.req = req

	return f.res, f.err
}

func TestStateSyncEvents_SinglePage(t *testing.T) {
	t.Parallel()

	recordTime := time.Unix(900, 0).UTC()
	fake := &fakeClerkQueryClient{res: &types.RecordListWithTimeResponse{EventRecords: []types.EventRecord{
		{Id: 5, Contract: "0x0000000000000000000000000000000000001001", Data: []byte{0x05}, TxHash: "0x02", LogIndex: 3, BorChainId: "137", RecordTime: recordTime},
		{Id: 6, Contract: "0x0000000000000000000000000000000000001001", Data: []byte{0x06}, TxHash: "0x03", LogIndex: 4, BorChainId: "137", RecordTime: recordTime},
	}}}
	client := &HeimdallGRPCClient{clerkQueryClient: fake}

	events, err := client.StateSyncEvents(t.Context(), 5, 1_000)
	require.NoError(t, err)

	require.Equal(t, uint64(5), fake.req.FromId)
	require.Equal(t, time.Unix(1_000, 0), fake.req.ToTime)
	require.Equal(t, uint64(stateFetchLimit), fake.req.Pagination.Limit)

	require.Len(t, events, 2)
	require.Equal(t, uint64(5), events[0].ID)
	require.Equal(t, "137", events[0].ChainID)
	require.Equal(t, common.HexToAddress("0x1001"), events[0].Contract)
	require.Equal(t, []byte{0x05}, []byte(events[0].Data))
	require.Equal(t, common.HexToHash("0x02"), events[0].TxHash)
	require.Equal(t, uint64(3), events[0].LogIndex)
	require.Equal(t, recordTime, events[0].Time)
	require.Equal(t, uint64(6), events[1].ID)
}

func TestStateSyncEvents_Error(t *testing.T) {
	t.Parallel()

	client := &HeimdallGRPCClient{clerkQueryClient: &fakeClerkQueryClient{err: errors.New("unavailable")}}

	events, err := client.StateSyncEvents(t.Context(), 1, 1_000)
	require.Error(t, err)
	require.Nil(t, events)
}
