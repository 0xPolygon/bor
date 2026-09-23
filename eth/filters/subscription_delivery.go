package filters

import (
	"sync"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rpc"
)

// subscriptionBacklogLimit bounds the events one RPC subscription may hold
// while its connection is not taking writes. It matches the event system's
// own burst allowance (txChanSize), so only a client that is not reading, not
// a burst, reaches it. A variable only so tests can lower it.
var subscriptionBacklogLimit = txChanSize

var subscriptionsDroppedMeter = metrics.NewRegisteredMeter("eth/filters/subscriptions/dropped", nil)

// deliver relays events from the event loop to one RPC subscription until the
// client unsubscribes. The event loop feeds every subscription from a single
// goroutine with blocking sends, so the socket writes happen on a separate
// goroutine: a client that stops reading holds up only itself, never the
// loop and with it every other subscriber and the chain feeds behind it.
// A subscription that fails a write or falls subscriptionBacklogLimit events
// behind ends, and closeConn tells the client so, rather than silently skip
// events it can no longer be sent.
//
// The backlog is a pair of slices the writer swaps under one lock, rather
// than a buffered channel, so an idle subscription holds no preallocated
// buffer and a keeping-up one reuses the same two without allocating.
func deliver[T any](sub *rpc.Subscription, events <-chan T, notify func(T) error, closeConn func()) {
	var (
		mu      sync.Mutex
		backlog []T
		wake    = make(chan struct{}, 1)
		failed  = make(chan struct{})
		done    = make(chan struct{})
	)
	defer close(done)

	go func() {
		var batch []T
		for {
			select {
			case <-wake:
			case <-done:
				return
			}
			for {
				mu.Lock()
				batch, backlog = backlog, batch[:0]
				mu.Unlock()
				if len(batch) == 0 {
					break
				}
				for i, event := range batch {
					var zero T
					batch[i] = zero
					select {
					case <-done:
						return
					default:
					}
					if err := notify(event); err != nil {
						log.Debug("Ending RPC subscription after a failed write", "id", sub.ID, "err", err)
						close(failed)
						return
					}
				}
				// Keep the spare slice for reuse only while it is small, so one
				// burst does not pin its high-water buffer for the lifetime of
				// the subscription.
				if cap(batch) > 64 {
					batch = nil
				}
			}
		}
	}()

	abandon := func() {
		subscriptionsDroppedMeter.Mark(1)
		// Closing can wait on the connection's ping loop; it must not hold
		// up the events this goroutine drains for the loop.
		go closeConn()
	}
	for {
		select {
		case event := <-events:
			mu.Lock()
			full := len(backlog) >= subscriptionBacklogLimit
			if !full {
				backlog = append(backlog, event)
			}
			mu.Unlock()
			if full {
				log.Debug("Ending RPC subscription that stopped reading", "id", sub.ID, "backlog", subscriptionBacklogLimit)
				abandon()
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		case <-failed:
			abandon()
			return
		case <-sub.Err():
			return
		}
	}
}
