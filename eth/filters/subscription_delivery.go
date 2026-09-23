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
func deliver[T any](sub *rpc.Subscription, events <-chan T, notify func(T) error, closeConn func()) {
	queue := &backlog[T]{wake: make(chan struct{}, 1)}
	failed := make(chan struct{})
	done := make(chan struct{})
	defer close(done)

	go func() {
		if err := queue.write(notify, done); err != nil {
			log.Debug("Ending RPC subscription after a failed write", "id", sub.ID, "err", err)
			close(failed)
		}
	}()

	for {
		select {
		case event := <-events:
			if !queue.push(event) {
				log.Debug("Ending RPC subscription that stopped reading", "id", sub.ID, "backlog", subscriptionBacklogLimit)
				abandon(closeConn)
				return
			}
		case <-failed:
			abandon(closeConn)
			return
		case <-sub.Err():
			return
		}
	}
}

// abandon records a subscription the server gave up on and closes its
// connection. Closing can wait on the connection's ping loop, so it runs on
// its own goroutine rather than hold up the events deliver drains.
func abandon(closeConn func()) {
	subscriptionsDroppedMeter.Mark(1)
	go closeConn()
}

// backlog holds a subscription's undelivered events. It is a pair of slices
// the writer swaps under one lock, rather than a buffered channel, so an idle
// subscription holds no preallocated buffer and a keeping-up one reuses the
// same two without allocating.
type backlog[T any] struct {
	mu     sync.Mutex
	queued []T
	wake   chan struct{}
}

// push queues an event for the writer, or reports false when the backlog is
// already at subscriptionBacklogLimit.
func (b *backlog[T]) push(event T) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queued) >= subscriptionBacklogLimit {
		return false
	}
	b.queued = append(b.queued, event)
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

// take returns everything queued and keeps drained, the writer's finished
// batch, as the next queue. A drained batch that grew past a small size is
// dropped instead, so one burst does not pin its high-water buffer.
func (b *backlog[T]) take(drained []T) []T {
	if cap(drained) > 64 {
		drained = nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.queued
	b.queued = drained[:0]
	return batch
}

// write sends queued events in order until done closes, returning the first
// failed write.
func (b *backlog[T]) write(notify func(T) error, done <-chan struct{}) error {
	var batch []T
	for {
		select {
		case <-b.wake:
		case <-done:
			return nil
		}
		for batch = b.take(batch); len(batch) > 0; batch = b.take(batch) {
			if stopped, err := writeBatch(batch, notify, done); stopped {
				return err
			}
		}
	}
}

// writeBatch sends one batch, clearing each slot so the batch can be reused,
// and reports whether to stop: done closed, or a write failed.
func writeBatch[T any](batch []T, notify func(T) error, done <-chan struct{}) (bool, error) {
	var zero T
	for i, event := range batch {
		batch[i] = zero
		select {
		case <-done:
			return true, nil
		default:
		}
		if err := notify(event); err != nil {
			return true, err
		}
	}
	return false, nil
}
