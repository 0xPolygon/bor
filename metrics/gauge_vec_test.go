package metrics

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGaugeVecGroupIteration(t *testing.T) {
	for _, kind := range []string{"standard", "ordered", "prefixed"} {
		t.Run(kind, func(t *testing.T) {
			registry := NewRegistry()
			switch kind {
			case "ordered":
				registry = NewOrderedRegistry()
			case "prefixed":
				registry = NewPrefixedRegistry("prefix/")
			}
			var calls int64
			group := NewGaugeVecGroup(func() map[string]map[string]int64 {
				calls++
				return map[string]map[string]int64{"a": {"peer": calls}, "b": {"peer": calls}}
			})
			for _, name := range []string{"a", "b"} {
				require.NoError(t, registry.Register(name, group.Gauge(name, "peer_id")))
			}
			registry.Each(func(string, interface{}) {})
			require.Zero(t, calls, "listing names must not collect")
			for want := int64(1); want <= 2; want++ {
				registry.Each(func(_ string, metric interface{}) {
					gauge := metric.(*GaugeVec)
					values := gauge.Snapshot()
					require.Equal(t, want, values["peer"])
					values["peer"] = -1
					require.Equal(t, want, gauge.Snapshot()["peer"], "snapshot maps must be independent")
				})
				require.Equal(t, want, calls)
			}
			require.Equal(t, int64(3), registry.Get("a").(*GaugeVec).Snapshot()["peer"])
			require.Equal(t, int64(4), registry.Get("b").(*GaugeVec).Snapshot()["peer"])
		})
	}
}

func TestGaugeVecGroupFilteredIteration(t *testing.T) {
	registry := NewRegistry()
	calls := 0
	group := NewGaugeVecGroup(func() map[string]map[string]int64 {
		calls++
		return map[string]map[string]int64{"value": {"peer": 20}}
	})
	require.NoError(t, registry.Register("other/value", group.Gauge("value", "peer_id")))
	child := NewPrefixedChildRegistry(registry, "selected/")
	child.Each(func(string, interface{}) { t.Fatal("unrelated metric included") })
	require.Zero(t, calls)
}

func TestGaugeVecGroupConcurrentCollections(t *testing.T) {
	registry := NewRegistry()
	var calls atomic.Int64
	group := NewGaugeVecGroup(func() map[string]map[string]int64 {
		value := calls.Add(1)
		return map[string]map[string]int64{"a": {"peer": value}, "b": {"peer": value}}
	})
	for _, name := range []string{"a", "b"} {
		require.NoError(t, registry.Register(name, group.Gauge(name, "peer_id")))
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 10 {
				sample := registry.GetAll()
				a := sample["a"]["values"].(map[string]int64)["peer"]
				b := sample["b"]["values"].(map[string]int64)["peer"]
				if a != b {
					t.Errorf("inconsistent group: %d != %d", a, b)
				}
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(160), calls.Load())
}
