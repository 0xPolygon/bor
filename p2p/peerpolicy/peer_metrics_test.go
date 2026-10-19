package peerpolicy

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
)

func scrapePeerMetrics(reg metrics.Registry) string {
	recorder := httptest.NewRecorder()
	prometheus.Handler(reg).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}

func TestPeerPolicyGaugeExpiryAndExemption(t *testing.T) {
	clock := new(mclock.Simulated)
	clock.Run(10 * windowWidth)
	tracker := New(clock)
	reg := metrics.NewRegistry()
	if err := tracker.RegisterPeerMetrics(reg); err != nil {
		t.Fatal(err)
	}
	defer tracker.UnregisterPeerMetrics()
	tracker.Observe("ordinary", Evidence{Reason: InvalidBlock})
	tracker.SetExempt("trusted", true)
	tracker.Observe("trusted", Evidence{Reason: InvalidBlock})
	tracker.Observe("clean", Evidence{Reason: FetcherDrop})
	got := scrapePeerMetrics(reg)
	if !strings.Contains(got, `eth_peerpolicy_peer_risk{peer_id="ordinary"} 60`) || strings.Contains(got, `peer_id="trusted"`) || strings.Contains(got, `peer_id="clean"`) {
		t.Fatalf("expected only positive non-exempt risk: %s", got)
	}
	if got := tracker.Snapshot("clean"); got.Risk != 0 || got.Windows[FetcherDrop.String()] != 1 {
		t.Fatalf("admin snapshot lost neutral evidence: %+v", got)
	}
	clock.Run(windowWidth * windowCount)
	if got := scrapePeerMetrics(reg); strings.Contains(got, "eth_peerpolicy_peer_risk{") {
		t.Fatalf("expired risk series retained: %s", got)
	}
	tracker.Observe("ordinary", Evidence{Reason: InvalidBlock})
	if got := scrapePeerMetrics(reg); !strings.Contains(got, `peer_id="ordinary"} 60`) {
		t.Fatalf("new evidence did not restore risk series: %s", got)
	}
	tracker.SetExempt("ordinary", true)
	if got := scrapePeerMetrics(reg); strings.Contains(got, "eth_peerpolicy_peer_risk{") {
		t.Fatalf("exempt peer retains risk series: %s", got)
	}
}

func TestPeerPolicyGaugeEvictionAndCleanup(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	reg := metrics.NewRegistry()
	if err := tracker.RegisterPeerMetrics(reg); err != nil {
		t.Fatal(err)
	}
	defer tracker.UnregisterPeerMetrics()
	for i := 0; i < maxPeers; i++ {
		tracker.Observe(fmt.Sprint(i), Evidence{Reason: InvalidBlock})
	}
	scrapePeerMetrics(reg)
	tracker.Observe("new", Evidence{Reason: RequestVolume})
	got := scrapePeerMetrics(reg)
	if strings.Count(got, "eth_peerpolicy_peer_risk{") != maxPeers || strings.Contains(got, `peer_id="0"`) || !strings.Contains(got, `peer_id="new"} 20`) {
		t.Fatal("scrape changed eviction order or retained an evicted series")
	}
	tracker.UnregisterPeerMetrics()
	if got := scrapePeerMetrics(reg); strings.Contains(got, "peer_risk") {
		t.Fatal("gauges remain after cleanup")
	}
	if err := tracker.RegisterPeerMetrics(reg); err != nil {
		t.Fatal(err)
	}
}

func TestPeerPolicyGaugeRegistrationOwnership(t *testing.T) {
	reg := metrics.NewRegistry()
	first, second := New(new(mclock.Simulated)), New(new(mclock.Simulated))
	if err := first.RegisterPeerMetrics(reg); err != nil {
		t.Fatal(err)
	}
	defer first.UnregisterPeerMetrics()
	if err := first.RegisterPeerMetrics(metrics.NewRegistry()); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if err := second.RegisterPeerMetrics(reg); err == nil {
		t.Fatal("registry collision accepted")
	}
	second.UnregisterPeerMetrics()
	if reg.Get(peerRiskMetric) == nil {
		t.Fatal("failed registration removed another tracker's metric")
	}
}

func TestPeerPolicyGaugeConcurrentScrapes(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	reg := metrics.NewRegistry()
	if err := tracker.RegisterPeerMetrics(reg); err != nil {
		t.Fatal(err)
	}
	defer tracker.UnregisterPeerMetrics()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			tracker.Observe(fmt.Sprint(i), Evidence{Reason: RequestVolume})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			scrapePeerMetrics(reg)
		}
	}()
	wg.Wait()
}
