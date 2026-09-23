package filters

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// A websocket client that subscribes to pending logs and then stops reading
// must not hold up delivery to anyone else. The event loop feeds every
// subscription from one goroutine, so before the per-subscription writer a
// single stalled socket froze it for the full write timeout (10s).
func TestStalledSubscriberDoesNotBlockOthers(t *testing.T) {
	backend, sys := newTestFilterSystem(rawdb.NewMemoryDatabase(), Config{})
	server := rpc.NewServer("test", 0, 0)
	if err := server.RegisterName("eth", NewFilterAPI(sys, false)); err != nil {
		t.Fatalf("register filter API: %v", err)
	}
	httpServer := httptest.NewServer(server.WebsocketHandler([]string{"*"}))
	t.Cleanup(func() {
		httpServer.Close()
		server.Stop()
	})
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	criteria := map[string]any{"pending": true}

	// The stalled client shrinks its receive buffer so the server's socket
	// fills after a few notifications, then never reads again.
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(4096)
		}
		return conn, err
	}}
	stalled, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial stalled client: %v", err)
	}
	t.Cleanup(func() { stalled.Close() })
	if err := stalled.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"logs", criteria}}); err != nil {
		t.Fatalf("stalled subscribe: %v", err)
	}
	if _, _, err := stalled.ReadMessage(); err != nil {
		t.Fatalf("stalled subscribe reply: %v", err)
	}

	client, err := rpc.Dial(url)
	if err != nil {
		t.Fatalf("dial healthy client: %v", err)
	}
	t.Cleanup(client.Close)
	received := make(chan types.Log, 64)
	sub, err := client.EthSubscribe(context.Background(), received, "logs", criteria)
	if err != nil {
		t.Fatalf("healthy subscribe: %v", err)
	}
	t.Cleanup(sub.Unsubscribe)

	// Lower the limit so the stalled subscription overflows within the test
	// and the server has to close its connection. This rewrites a package
	// variable every subscription reads, so this test must not run in parallel.
	defaultLimit := subscriptionBacklogLimit
	subscriptionBacklogLimit = 64
	t.Cleanup(func() { subscriptionBacklogLimit = defaultLimit })

	// Each log is sent once the healthy client has the previous one, so it
	// keeps up by construction and only the stalled client falls behind.
	deadline := time.After(20 * time.Second)
	sent := 0
	send := func() {
		t.Helper()
		backend.pendingLogsFeed.Send([]*types.Log{{
			Address:     common.Address{0x1},
			Topics:      []common.Hash{},
			Data:        make([]byte, 16<<10),
			BlockNumber: uint64(sent),
		}})
		select {
		case log := <-received:
			if log.BlockNumber != uint64(sent) {
				t.Fatalf("log %d arrived as %d", sent, log.BlockNumber)
			}
		case err := <-sub.Err():
			t.Fatalf("healthy subscription failed after %d logs: %v", sent, err)
		case <-deadline:
			t.Fatalf("healthy client received %d logs before the deadline", sent)
		}
		sent++
	}
	dropped := subscriptionsDroppedMeter.Snapshot().Count()
	start := time.Now()
	for range 400 {
		send()
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("healthy client took %s: the stalled subscriber held up delivery", elapsed)
	}
	// How much the kernel buffers before the stalled write blocks varies by
	// platform, so keep feeding until the server gives up on that client.
	for subscriptionsDroppedMeter.Snapshot().Count() == dropped {
		send()
	}

	// The stalled client learns its subscription ended: once it drains what
	// was already sent, the connection is closed rather than left silent.
	if err := stalled.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for {
		_, _, err := stalled.ReadMessage()
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("stalled subscription's connection was left open")
		}
		break
	}
}

func TestDeliverClosesConnWhenSubscriptionIsAbandoned(t *testing.T) {
	t.Run("backlog overflow", func(t *testing.T) {
		events := make(chan int)
		release := make(chan struct{})
		defer close(release)
		closed := make(chan struct{})
		go deliver(&rpc.Subscription{ID: "stalled"}, events, func(int) error {
			<-release
			return nil
		}, func() { close(closed) })
		// The writer may already hold part of the backlog when it blocks, so
		// up to twice the limit can be taken before the overflow.
		for i := 0; i <= 2*subscriptionBacklogLimit+1; i++ {
			select {
			case events <- i:
			case <-closed:
				return
			case <-time.After(time.Second):
				t.Fatalf("deliver stopped taking events at %d without closing", i)
			}
		}
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("overflowing subscription did not close its connection")
		}
	})
	t.Run("failed write", func(t *testing.T) {
		events := make(chan int)
		closed := make(chan struct{})
		go deliver(&rpc.Subscription{ID: "broken"}, events, func(int) error {
			return errors.New("write: i/o timeout")
		}, func() { close(closed) })
		events <- 1
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("failed write did not close the connection")
		}
	})
}

// Ending a state-sync subscription must drain its channel like the others,
// or the event loop, blocked sending it an event, never takes the uninstall.
func TestUnsubscribeDrainsStateSyncChannel(t *testing.T) {
	backend, sys := newTestFilterSystem(rawdb.NewMemoryDatabase(), Config{})
	es := NewEventSystem(sys)
	sub := es.SubscribeNewDeposits(make(chan *types.StateSyncData))
	// The feed hands the event to the loop's buffered channel; give the loop
	// a moment to pick it up and block on the unread subscription channel.
	backend.stateSyncFeed.Send(core.StateSyncEvent{Data: &types.StateSyncData{ID: 1}})
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		sub.Unsubscribe()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unsubscribe deadlocked against a pending state-sync event")
	}
}
