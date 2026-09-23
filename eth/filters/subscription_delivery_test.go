package filters

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ethereum/go-ethereum/common"
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

	const batches = 400
	go func() {
		for i := range batches {
			backend.pendingLogsFeed.Send([]*types.Log{{
				Address:     common.Address{0x1},
				Topics:      []common.Hash{},
				Data:        make([]byte, 64<<10),
				BlockNumber: uint64(i),
			}})
		}
	}()

	start := time.Now()
	deadline := time.After(20 * time.Second)
	for i := range batches {
		select {
		case log := <-received:
			if log.BlockNumber != uint64(i) {
				t.Fatalf("log %d arrived as %d", i, log.BlockNumber)
			}
		case err := <-sub.Err():
			t.Fatalf("healthy subscription failed after %d logs: %v", i, err)
		case <-deadline:
			t.Fatalf("healthy client received %d of %d logs in 20s", i, batches)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("healthy client took %s: the stalled subscriber held up delivery", elapsed)
	}
}
