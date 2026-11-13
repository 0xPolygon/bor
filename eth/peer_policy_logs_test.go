package eth

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

type peerTransitionLogCapture struct {
	slog.Handler
	records chan slog.Record
}

func (h *peerTransitionLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *peerTransitionLogCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "Peer abuse risk changed" {
		select {
		case h.records <- record.Clone():
		default:
		}
	}
	return nil
}

func TestPeerPolicyHandlerTransitionLogging(t *testing.T) {
	previous := log.Root()
	capture := &peerTransitionLogCapture{Handler: log.DiscardHandler(), records: make(chan slog.Record, 1)}
	log.SetDefault(log.NewLogger(capture))
	t.Cleanup(func() { log.SetDefault(previous) })
	h := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
		config.peerReputation = true
		return config
	})
	t.Cleanup(h.close)
	h.handler.observePeer("logger-peer", peerpolicy.InvalidBlock)
	select {
	case record := <-capture.records:
		fields := make(map[string]any)
		record.Attrs(func(attr slog.Attr) bool { fields[attr.Key] = attr.Value.Any(); return true })
		require.Equal(t, "logger-peer", fields["peer_id"])
		require.Equal(t, uint64(60), fields["risk"])
		require.Equal(t, "observe", fields["mode"])
		require.Equal(t, "abuse", fields["score_type"])
	case <-time.After(5 * time.Second):
		t.Fatal("enabled handler did not log peer abuse transition")
	}
}
