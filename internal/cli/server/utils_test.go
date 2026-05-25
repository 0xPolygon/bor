package server

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConvertReceiptToProtoReceipt_Int64Range(t *testing.T) {
	t.Parallel()

	// Largest value that fits in int64 — must round-trip cleanly.
	t.Run("max int64 round-trips", func(t *testing.T) {
		t.Parallel()
		max := new(big.Int).SetInt64(1<<63 - 1)
		r := &types.Receipt{EffectiveGasPrice: max, BlockNumber: max}
		out, err := ConvertReceiptToProtoReceipt(r)
		require.NoError(t, err)
		require.Equal(t, int64(1<<63-1), out.EffectiveGasPrice)
		require.Equal(t, int64(1<<63-1), out.BlockNumber)
	})

	// One past max int64 — must error rather than silently truncate to a
	// negative value.
	t.Run("EffectiveGasPrice over int64 errors", func(t *testing.T) {
		t.Parallel()
		over := new(big.Int).Add(new(big.Int).SetInt64(1<<63-1), big.NewInt(1))
		r := &types.Receipt{EffectiveGasPrice: over, BlockNumber: big.NewInt(1)}
		_, err := ConvertReceiptToProtoReceipt(r)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.OutOfRange, s.Code())
		require.Contains(t, s.Message(), "effective gas price")
	})

	t.Run("BlockNumber over int64 errors", func(t *testing.T) {
		t.Parallel()
		over := new(big.Int).Add(new(big.Int).SetInt64(1<<63-1), big.NewInt(1))
		r := &types.Receipt{EffectiveGasPrice: big.NewInt(1), BlockNumber: over}
		_, err := ConvertReceiptToProtoReceipt(r)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.OutOfRange, s.Code())
		require.Contains(t, s.Message(), "block number")
	})

	// Nil big.Int fields default to 0 — preserve that behavior.
	t.Run("nil big.Int fields default to 0", func(t *testing.T) {
		t.Parallel()
		r := &types.Receipt{}
		out, err := ConvertReceiptToProtoReceipt(r)
		require.NoError(t, err)
		require.Equal(t, int64(0), out.EffectiveGasPrice)
		require.Equal(t, int64(0), out.BlockNumber)
	})
}

func TestPeerInfoToPeer(t *testing.T) {
	t.Parallel()

	info := &p2p.PeerInfo{
		ID:    "node-1",
		Enode: "enode://abc@127.0.0.1:30303",
		ENR:   "enr:-...",
		Caps:  []string{"eth/68", "snap/1"},
		Name:  "bor/v1.0.0",
	}
	info.Network.Trusted = true
	info.Network.Static = false

	out := PeerInfoToPeer(info)
	require.Equal(t, "node-1", out.Id)
	require.Equal(t, "enode://abc@127.0.0.1:30303", out.Enode)
	require.Equal(t, "enr:-...", out.Enr)
	require.Equal(t, []string{"eth/68", "snap/1"}, out.Caps)
	require.Equal(t, "bor/v1.0.0", out.Name)
	require.True(t, out.Trusted)
	require.False(t, out.Static)
}

func TestConvertTopicsToProtoTopics(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil slice", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, ConvertTopicsToProtoTopics(nil))
	})

	t.Run("preserves order and count", func(t *testing.T) {
		t.Parallel()
		topics := []common.Hash{
			common.HexToHash("0x01"),
			common.HexToHash("0x02"),
			common.HexToHash("0x03"),
		}
		out := ConvertTopicsToProtoTopics(topics)
		require.Len(t, out, 3)
		// Each element should be non-nil and have non-nil Hi/Lo.
		for i, h := range out {
			require.NotNil(t, h, "topic %d nil", i)
			require.NotNil(t, h.Hi, "topic %d Hi nil", i)
			require.NotNil(t, h.Lo, "topic %d Lo nil", i)
		}
	})
}

func TestConvertLogsToProtoLogs(t *testing.T) {
	t.Parallel()

	t.Run("empty slice returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, ConvertLogsToProtoLogs(nil))
	})

	t.Run("single log carries every field through", func(t *testing.T) {
		t.Parallel()
		log := &types.Log{
			Address:     common.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01"),
			Topics:      []common.Hash{common.HexToHash("0xaa"), common.HexToHash("0xbb")},
			Data:        []byte{0xde, 0xad, 0xbe, 0xef},
			BlockNumber: 42,
			TxHash:      common.HexToHash("0x01"),
			TxIndex:     7,
			BlockHash:   common.HexToHash("0x02"),
			Index:       3,
			Removed:     true,
		}
		out := ConvertLogsToProtoLogs([]*types.Log{log})
		require.Len(t, out, 1)
		require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, out[0].Data)
		require.Equal(t, uint64(42), out[0].BlockNumber)
		require.Equal(t, uint64(7), out[0].TxIndex)
		require.Equal(t, uint64(3), out[0].Index)
		require.True(t, out[0].Removed)
		require.Len(t, out[0].Topics, 2)
	})
}

func TestHealthStatusLevel_String(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lvl  HealthStatusLevel
		want string
	}{
		{StatusOK, "OK"},
		{StatusWarn, "WARN"},
		{StatusCritical, "CRITICAL"},
		{HealthStatusLevel(99), "UNKNOWN"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, tc.lvl.String())
	}
}

func TestHealthStatusLevel_Code(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lvl  HealthStatusLevel
		want int
	}{
		{StatusOK, 0},
		{StatusWarn, 1},
		{StatusCritical, 2},
		{HealthStatusLevel(99), -1},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, tc.lvl.Code())
	}
}

func TestHealthStatusLevel_MarshalJSON(t *testing.T) {
	t.Parallel()

	// The level marshals as a JSON string with the canonical name, not as the
	// numeric code — so wire consumers can treat it as a stable enum label.
	for _, tc := range []struct {
		lvl  HealthStatusLevel
		want string
	}{
		{StatusOK, `"OK"`},
		{StatusWarn, `"WARN"`},
		{StatusCritical, `"CRITICAL"`},
	} {
		got, err := json.Marshal(tc.lvl)
		require.NoError(t, err)
		require.Equal(t, tc.want, string(got))
	}
}

func TestResponseRecorder(t *testing.T) {
	t.Parallel()

	r := &ResponseRecorder{ResponseWriter: httptest.NewRecorder()}
	r.WriteHeader(418)
	n, err := r.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	n, err = r.Write([]byte(" world"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	// statusCode + body are captured for later replay
	require.Equal(t, 418, r.statusCode)
	require.Equal(t, "hello world", string(r.body))
}
