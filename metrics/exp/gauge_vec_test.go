package exp

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
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
