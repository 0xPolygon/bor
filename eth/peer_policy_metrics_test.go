package eth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/log"

	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
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

func TestPeerPolicyGaugeCollectionExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	h := &handler{peerPolicy: peerpolicy.New(clock)}
	registry := metrics.NewRegistry()
	if err := h.peerPolicy.RegisterPeerMetrics(registry); err != nil {
		t.Fatal(err)
	}
	defer h.peerPolicy.UnregisterPeerMetrics()
	h.observePeer("risky-peer", peerpolicy.InvalidBlock)
	h.observePeer("clean-peer", peerpolicy.FetcherDrop)
	h.peerPolicy.SetExempt("trusted-peer", true)
	h.observePeer("trusted-peer", peerpolicy.InvalidBlock)
	for _, scrape := range []func() string{
		func() string {
			recorder := httptest.NewRecorder()
			prometheus.Handler(registry).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
			return recorder.Body.String()
		},
		func() string {
			encoded, err := json.Marshal(registry)
			if err != nil {
				t.Fatal(err)
			}
			return string(encoded)
		},
	} {
		h.observePeer("risky-peer", peerpolicy.InvalidBlock)
		got := scrape()
		if !strings.Contains(got, "risky-peer") || strings.Contains(got, "clean-peer") || strings.Contains(got, "trusted-peer") {
			t.Fatalf("unexpected exported identities: %s", got)
		}
		clock.Run(time.Minute)
		if got := scrape(); strings.Contains(got, "risky-peer") {
			t.Fatalf("expired identity still exported: %s", got)
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
