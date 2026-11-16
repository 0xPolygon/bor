package heimdall

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// messageCapture records the messages logged while it is the default handler.
type messageCapture struct{ messages chan string }

func (m *messageCapture) Enabled(context.Context, slog.Level) bool { return true }
func (m *messageCapture) WithAttrs([]slog.Attr) slog.Handler       { return m }
func (m *messageCapture) WithGroup(string) slog.Handler            { return m }

func (m *messageCapture) Handle(_ context.Context, r slog.Record) error {
	select {
	case m.messages <- r.Message:
	default:
	}

	return nil
}

// TestStateSyncEvents_SinglePage swaps the global logger, so it must not run in parallel.
func TestStateSyncEvents_SinglePage(t *testing.T) {
	capture := &messageCapture{messages: make(chan string, 16)}
	prev := log.Root()
	log.SetDefault(log.NewLogger(capture))
	t.Cleanup(func() { log.SetDefault(prev) })

	to := time.Unix(1_000, 0)
	record := func(id uint64, at time.Time) clerkTypes.EventRecord {
		return clerkTypes.EventRecord{
			Id:         id,
			Contract:   "0x0000000000000000000000000000000000001001",
			Data:       []byte{byte(id)},
			TxHash:     "0x01",
			LogIndex:   id,
			BorChainId: "137",
			RecordTime: at,
		}
	}

	body, err := codec.NewProtoCodec(codectypes.NewInterfaceRegistry()).MarshalJSON(&clerkTypes.RecordListResponse{
		EventRecords: []clerkTypes.EventRecord{
			record(12, to.Add(-time.Second)),
			record(9, to.Add(-time.Second)), // below fromID
			record(10, to.Add(-3*time.Second)),
			record(13, to), // not before to
			record(11, to.Add(-2*time.Second)),
		},
	})
	require.NoError(t, err)

	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Equal(t, "/clerk/time", r.URL.Path)
		require.Equal(t, "10", r.URL.Query().Get("from_id"))
		require.Equal(t, "50", r.URL.Query().Get("pagination.limit"))
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := NewHeimdallClient(srv.URL, 5*time.Second)
	defer client.Close()

	events, err := client.StateSyncEvents(t.Context(), 10, to.Unix())
	require.NoError(t, err)
	require.Equal(t, 1, requests, "one call fetches one page")
	require.Contains(t, drain(capture.messages), "Fetching state sync events")
	require.Len(t, events, 3)

	for i, e := range events {
		id := uint64(10 + i)
		require.Equal(t, id, e.ID)
		require.Equal(t, "137", e.ChainID)
		require.Equal(t, common.HexToAddress("0x1001"), e.Contract)
		require.Equal(t, []byte{byte(id)}, []byte(e.Data))
		require.Equal(t, id, e.LogIndex)
		require.Equal(t, common.HexToHash("0x01"), e.TxHash)
	}
}

func TestStateSyncEvents_Errors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cases := []struct {
		name    string
		url     string
		wantErr error // nil means any error
	}{
		{"server error", srv.URL, ErrNotSuccessfulResponse},
		{"bad url", "http://bad host", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := NewHeimdallClient(tc.url, 5*time.Second)
			defer client.Close()

			events, err := client.StateSyncEvents(t.Context(), 1, 100)
			require.Error(t, err)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.Nil(t, events)
		})
	}
}

func drain(messages chan string) []string {
	var out []string

	for {
		select {
		case m := <-messages:
			out = append(out, m)
		default:
			return out
		}
	}
}
