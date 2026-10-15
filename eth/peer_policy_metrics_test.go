package eth

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/log"

	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestPeerPolicyGaugeLifecycle(t *testing.T) {
	metrics.Enable()
	for _, enabled := range []bool{false, true} {
		h := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
			config.peerReputation = enabled
			return config
		})
		gauge, ok := metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk").(*metrics.GaugeVec)
		if ok != enabled {
			h.close()
			t.Fatalf("enabled=%v registered=%v", enabled, ok)
		}
		if enabled {
			h.handler.observePeer("test-peer", peerpolicy.InvalidBlock)
			if gauge.Snapshot()["test-peer"] != 60 {
				h.close()
				t.Fatal("handler evidence not exported")
			}
		}
		h.close()
		if metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk") != nil {
			t.Fatal("shutdown retained the collector")
		}
	}
}

type peerMetricLogCapture struct {
	slog.Handler
	failures atomic.Int64
}

func (h *peerMetricLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *peerMetricLogCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "Failed to register peer reputation gauges" {
		h.failures.Add(1)
	}
	return nil
}

func TestPeerPolicyGaugeRegistrationDiagnostic(t *testing.T) {
	metrics.Enable()
	capture := &peerMetricLogCapture{Handler: log.DiscardHandler()}
	previous := log.Root()
	log.SetDefault(log.NewLogger(capture))
	defer log.SetDefault(previous)
	config := func(c *handlerConfig) *handlerConfig { c.peerReputation = true; return c }
	first := newTestHandlerWithConfig(config)
	defer first.close()
	if capture.failures.Load() != 0 {
		t.Fatal("successful registration logged a failure")
	}
	second := newTestHandlerWithConfig(config)
	second.close()
	if capture.failures.Load() != 1 {
		t.Fatal("registration conflict not reported")
	}
	if metrics.DefaultRegistry.Get("eth/peerpolicy/peer_risk") == nil {
		t.Fatal("conflicting handler removed the first collector")
	}
}
