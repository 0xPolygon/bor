package exp

import (
	"encoding/json"
	"expvar"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestGaugeVecEndpoint(t *testing.T) {
	registry := metrics.NewRegistry()
	values := map[string]int64{}
	name := t.Name() + "/eth/peerpolicy/peer_risk"
	if err := registry.Register(name, metrics.NewGaugeVec("peer_id", func() map[string]int64 { return values })); err != nil {
		t.Fatal(err)
	}
	handler := ExpHandler(registry)
	for _, tc := range []struct {
		name   string
		scores map[string]int64
	}{
		{"empty", map[string]int64{}},
		{"peers", map[string]int64{"peer-a": 60, "peer-b": 20}},
		{"expired_and_evicted", map[string]int64{"peer-a": 0}},
		{"empty_again", map[string]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values = tc.scores
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/debug/metrics", nil))
			if response.Code != 200 {
				t.Fatalf("status = %d", response.Code)
			}
			if strings.Count(response.Body.String(), fmt.Sprintf("%q:", name)) > 1 {
				t.Fatal("registry vector duplicated an existing JSON key")
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			var got map[string]int64
			if err := json.Unmarshal(document[name], &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.scores) {
				t.Fatalf("scores = %v, want %v", got, tc.scores)
			}
		})
	}
}

func TestGaugeVecGroupEndpoint(t *testing.T) {
	registry := metrics.NewRegistry()
	calls := int64(0)
	group := metrics.NewGaugeVecGroup(func() map[string]map[string]int64 {
		calls++
		return map[string]map[string]int64{"risk": {"peer": calls}, "behavior": {"peer": calls}}
	})
	for _, name := range []string{"risk", "behavior"} {
		if err := registry.Register(t.Name()+"/"+name, group.Gauge(name, "peer_id")); err != nil {
			t.Fatal(err)
		}
	}
	handler := ExpHandler(registry)
	for want := int64(1); want <= 2; want++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/debug/metrics", nil))
		var document map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"risk", "behavior"} {
			var values map[string]int64
			if err := json.Unmarshal(document[t.Name()+"/"+name], &values); err != nil {
				t.Fatal(err)
			}
			if values["peer"] != want {
				t.Fatalf("%s = %v, want peer:%d", name, values, want)
			}
		}
		if calls != want {
			t.Fatalf("collections = %d, want %d", calls, want)
		}
	}
}

func TestPeerRiskUnregisterRemovesJSONLabels(t *testing.T) {
	registry := metrics.NewRegistry()
	tracker := peerpolicy.New(new(mclock.Simulated))
	if err := tracker.RegisterPeerMetrics(registry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker.UnregisterPeerMetrics)
	tracker.Observe("risky-peer", peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
	handler := ExpHandler(registry)
	name := "eth/peerpolicy/peer_risk"
	if got := scrapeGaugeVec(t, handler, name); !reflect.DeepEqual(got, map[string]int64{"risky-peer": 60}) {
		t.Fatalf("initial risk = %v", got)
	}
	tracker.UnregisterPeerMetrics()
	if got := scrapeGaugeVec(t, handler, name); got != nil {
		t.Fatalf("unregistered risk labels retained: %v", got)
	}
	if expvar.Get(name) != nil {
		t.Fatal("registry vector leaked into global expvar")
	}
}

func TestGaugeVecPreservesGlobalMap(t *testing.T) {
	name := t.Name()
	global, exists := expvar.Get(name).(*expvar.Map)
	if !exists {
		global = new(expvar.Map).Init()
		expvar.Publish(name, global)
	}
	sentinel := new(expvar.Int)
	sentinel.Set(7)
	global.Set("sentinel", sentinel)
	registry := metrics.NewRegistry()
	if err := registry.Register(name, metrics.NewGaugeVec("peer_id", func() map[string]int64 {
		return map[string]int64{"peer": 60}
	})); err != nil {
		t.Fatal(err)
	}
	handler := ExpHandler(registry)
	if got := scrapeGaugeVec(t, handler, name); !reflect.DeepEqual(got, map[string]int64{"peer": 60}) {
		t.Fatalf("registry vector = %v", got)
	}
	registry.Unregister(name)
	if got := scrapeGaugeVec(t, handler, name); !reflect.DeepEqual(got, map[string]int64{"sentinel": 7}) {
		t.Fatalf("unrelated global map changed: %v", got)
	}
	if global.Get("peer") != nil || global.Get("sentinel") != sentinel {
		t.Fatal("vector modified global expvar map")
	}
}

func TestGaugeVecConcurrentRegistryIsolation(t *testing.T) {
	name := t.Name()
	var workers sync.WaitGroup
	for _, label := range []string{"left", "right"} {
		registry := metrics.NewRegistry()
		if err := registry.Register(name, metrics.NewGaugeVec("peer_id", func() map[string]int64 {
			return map[string]int64{label: 20}
		})); err != nil {
			t.Fatal(err)
		}
		handler := ExpHandler(registry)
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				if got := scrapeGaugeVec(t, handler, name); !reflect.DeepEqual(got, map[string]int64{label: 20}) {
					t.Errorf("registry %s received another registry's labels: %v", label, got)
				}
			}
			registry.Unregister(name)
			if got := scrapeGaugeVec(t, handler, name); got != nil {
				t.Errorf("registry %s retained labels: %v", label, got)
			}
		}()
	}
	workers.Wait()
	if expvar.Get(name) != nil {
		t.Fatal("registry vectors leaked into global expvar")
	}
}

func scrapeGaugeVec(t *testing.T, handler http.Handler, name string) map[string]int64 {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/debug/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Count(response.Body.String(), fmt.Sprintf("%q:", name)) > 1 {
		t.Fatal("registry vector duplicated an existing JSON key")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document[name] == nil {
		return nil
	}
	var got map[string]int64
	if err := json.Unmarshal(document[name], &got); err != nil {
		t.Fatal(err)
	}
	return got
}
