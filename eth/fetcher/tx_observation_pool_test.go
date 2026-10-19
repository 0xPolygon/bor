package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPeerPolicyDisabledPoolCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		requested, delivered int
		want                 DeliveryProvenance
	}{
		{"matching single", 1, 1, RequestedDelivery},
		{"partial reply", 2, 1, RequestedDelivery},
		{"incomplete matching summary", 2, 2, UnknownDelivery},
		{"incomplete mixed summary", 1, 2, UnknownDelivery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pool *txpool.TxPool
			f := NewTxFetcher(pool.Has, func(txs []*types.Transaction) []error {
				return pool.Add(txs, false)
			}, func(string, uint64, []common.Hash) error { return nil }, func(string) {})
			f.requests["peer"] = &txRequest{requestID: 7, hashes: testTxsHashes[:tc.requested]}
			observations := make(chan DeliveryProvenance, 2)
			f.SetValidationObserver(func(_ string, provenance DeliveryProvenance, items, bytes uint64, invalid bool) {
				if items != uint64(tc.delivered) || bytes == 0 || invalid {
					provenance = DeliveryProvenance(255)
				}
				observations <- provenance
			})
			f.Start()
			defer f.Stop()
			for _, want := range []DeliveryProvenance{tc.want, UnknownDelivery} {
				if err := f.Enqueue("peer", testTxs[:tc.delivered], true, 7); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-observations:
					if got != want {
						t.Fatalf("delivery provenance = %v, want %v", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("missing delivery observation")
				}
			}
		})
	}
}
