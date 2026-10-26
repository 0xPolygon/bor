package prometheus

import (
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
)

func TestGaugeVec(t *testing.T) {
	reg := metrics.NewRegistry()
	values := map[string]int64{"b": 100, "a\"\n": 60}
	gauge := metrics.NewGaugeVec("peer_id", func() map[string]int64 { return values })
	if err := reg.Register("peer/risk", gauge); err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	if err := c.Add("peer/risk", reg.Get("peer/risk")); err != nil {
		t.Fatal(err)
	}
	want := "# TYPE peer_risk gauge\npeer_risk{peer_id=\"a\\\"\\n\"} 60\npeer_risk{peer_id=\"b\"} 100\n\n"
	if got := c.buff.String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	values = map[string]int64{"b": 0}
	c = newCollector()
	if err := c.Add("peer/risk", gauge); err != nil {
		t.Fatal(err)
	}
	if got := c.buff.String(); got != "# TYPE peer_risk gauge\npeer_risk{peer_id=\"b\"} 0\n\n" {
		t.Fatalf("stale snapshot: %q", got)
	}
	all := reg.GetAll()["peer/risk"]
	if all["label"] != "peer_id" || all["values"].(map[string]int64)["b"] != 0 {
		t.Fatal("registry snapshot missing")
	}
	values = map[string]int64{}
	c = newCollector()
	if err := c.Add("peer/risk", gauge); err != nil {
		t.Fatal(err)
	}
	if c.buff.String() != "# TYPE peer_risk gauge\n\n" {
		t.Fatal("empty vector retained a series")
	}
}
