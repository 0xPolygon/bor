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
var subscriptionBacklogLimit = 4096

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
// The backlog is a slice rather than a buffered channel so an idle or
// keeping-up subscription holds no preallocated buffer.
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
		for {
			select {
			case <-wake:
			case <-done:
				return
			}
			for {
				mu.Lock()
				if len(backlog) == 0 {
					backlog = nil
					mu.Unlock()
					break
				}
				event := backlog[0]
				var zero T
				backlog[0] = zero
				backlog = backlog[1:]
				mu.Unlock()
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
		}
	}()

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
				subscriptionsDroppedMeter.Mark(1)
				// Closing can wait on the connection's ping loop; it must not
				// hold up the events this goroutine drains for the loop.
				go closeConn()
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		case <-failed:
			subscriptionsDroppedMeter.Mark(1)
			go closeConn()
			return
		case <-sub.Err():
			return
		}
	}
}
