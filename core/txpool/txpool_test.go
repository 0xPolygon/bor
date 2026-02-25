package txpool

import (
	"testing"

	"github.com/ethereum/go-ethereum/core"
)

// TestSubscribeRebroadcastTransactionsNilPool tests that calling
// SubscribeRebroadcastTransactions on a nil TxPool returns a valid no-op
// subscription.
func TestSubscribeRebroadcastTransactionsNilPool(t *testing.T) {
	var pool *TxPool // nil pool

	ch := make(chan core.StuckTxsEvent, 1)
	sub := pool.SubscribeRebroadcastTransactions(ch)

	// Verify the subscription is valid even for nil pool
	if sub == nil {
		t.Fatal("expected non-nil subscription")
	}

	// Unsubscribe should work without issues
	sub.Unsubscribe()

	// Channel should be empty (no events should be sent)
	select {
	case event := <-ch:
		t.Fatalf("unexpected event: %v", event)
	default:
		// Expected - no events
	}
}
