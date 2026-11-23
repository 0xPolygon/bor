package prometheus

import (
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/metrics"
)

func (c *collector) addGaugeVec(name string, gauge *metrics.GaugeVec) {
	name = mutateKey(name)
	values := gauge.Snapshot()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintf(c.buff, typeGaugeTpl, name)
	for _, key := range keys {
		fmt.Fprintf(c.buff, "%s{%s=%q} %d\n", name, gauge.Label(), key, values[key])
	}
	c.buff.WriteByte('\n')
}
