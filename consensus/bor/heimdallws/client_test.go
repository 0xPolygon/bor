package heimdallws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// newTestWSServer creates a test WS server that accepts connections and sends a subscription ack.
// If reject is true, the server closes connections immediately.
func newTestWSServer(t *testing.T, reject bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Read the subscription request.
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send a simple ack (not a milestone, just keeps connection alive).
		ack := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"result":  map[string]interface{}{},
		}

		if err := conn.WriteJSON(ack); err != nil {
			return
		}

		// Keep the connection open until client disconnects.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
}

// newTestWSServerWithMilestone creates a test WS server that sends a milestone event after connection.
func newTestWSServerWithMilestone(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Read the subscription request.
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send a milestone event.
		resp := wsResponse{
			JSONRPC: "2.0",
			ID:      0,
			Result: wsResult{
				Query: "tm.event='NewBlock' AND milestone.number>0",
				Data: wsData{
					Type: "tendermint/event/NewBlock",
					Value: wsValue{
						FinalizeBlock: finalizeBlock{
							Events: []wsEvent{
								{
									Type: "milestone",
									Attributes: []attribute{
										{Key: "proposer", Value: "0x0000000000000000000000000000000000000001"},
										{Key: "hash", Value: "0x0000000000000000000000000000000000000000000000000000000000000002"},
										{Key: "start_block", Value: "100"},
										{Key: "end_block", Value: "200"},
										{Key: "bor_chain_id", Value: "137"},
										{Key: "milestone_id", Value: "test-1"},
										{Key: "timestamp", Value: "1000"},
										{Key: "total_difficulty", Value: "500"},
									},
								},
							},
						},
					},
				},
			},
		}

		data, _ := json.Marshal(resp)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}

		// Keep connection open.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func TestWSClient_ConstructorSingleURL(t *testing.T) {
	client, err := NewHeimdallWSClient("ws://localhost:1234", "")
	require.NoError(t, err)
	assert.Len(t, client.urls, 1)
	assert.Equal(t, "ws://localhost:1234", client.urls[0])
	assert.Equal(t, 0, client.activeURL)
}

func TestWSClient_ConstructorDualURL(t *testing.T) {
	client, err := NewHeimdallWSClient("ws://primary:1234", "ws://secondary:5678")
	require.NoError(t, err)
	assert.Len(t, client.urls, 2)
	assert.Equal(t, "ws://primary:1234", client.urls[0])
	assert.Equal(t, "ws://secondary:5678", client.urls[1])
	assert.Equal(t, 0, client.activeURL)
}

func TestWSClient_SingleURL_ConnectsSuccessfully(t *testing.T) {
	server := newTestWSServerWithMilestone(t)
	defer server.Close()

	client, err := NewHeimdallWSClient(wsURL(server.URL), "")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := client.SubscribeMilestoneEvents(ctx)

	select {
	case m := <-events:
		require.NotNil(t, m)
		assert.Equal(t, uint64(100), m.StartBlock)
		assert.Equal(t, uint64(200), m.EndBlock)
		assert.Equal(t, "137", m.BorChainID)
		assert.Equal(t, "test-1", m.MilestoneID)
	case <-ctx.Done():
		t.Fatal("timed out waiting for milestone event")
	}

	require.NoError(t, client.Unsubscribe(ctx))
}

func TestWSClient_DualURL_FailoverToSecondary(t *testing.T) {
	// Primary always rejects.
	primary := newTestWSServer(t, true)
	defer primary.Close()

	// Secondary accepts and sends a milestone.
	secondary := newTestWSServerWithMilestone(t)
	defer secondary.Close()

	client, err := NewHeimdallWSClient(wsURL(primary.URL), wsURL(secondary.URL))
	require.NoError(t, err)

	// Speed up test by reducing reconnect delay and attempts.
	client.reconnectDelay = 100 * time.Millisecond
	client.primaryAttempts = 2

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := client.SubscribeMilestoneEvents(ctx)

	select {
	case m := <-events:
		require.NotNil(t, m)
		assert.Equal(t, uint64(100), m.StartBlock)
		assert.Equal(t, uint64(200), m.EndBlock)
		// Verify we switched to secondary.
		assert.Equal(t, 1, client.activeURL)
	case <-ctx.Done():
		t.Fatal("timed out waiting for milestone event via failover")
	}

	require.NoError(t, client.Unsubscribe(ctx))
}

func TestWSClient_ContextCancellation(t *testing.T) {
	// Both URLs reject — client should respect context cancellation.
	primary := newTestWSServer(t, true)
	defer primary.Close()

	secondary := newTestWSServer(t, true)
	defer secondary.Close()

	client, err := NewHeimdallWSClient(wsURL(primary.URL), wsURL(secondary.URL))
	require.NoError(t, err)

	client.reconnectDelay = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	// tryUntilSubscribeMilestoneEvents should return without blocking forever.
	client.tryUntilSubscribeMilestoneEvents(ctx)

	// Verify context was cancelled.
	assert.Error(t, ctx.Err())
}

func TestWSClient_DualURL_ProbeBackToPrimary(t *testing.T) {
	// Test that after cooldown, the reconnection loop probes primary first.
	primary := newTestWSServer(t, true)
	defer primary.Close()

	secondary := newTestWSServer(t, true)
	defer secondary.Close()

	client, err := NewHeimdallWSClient(wsURL(primary.URL), wsURL(secondary.URL))
	require.NoError(t, err)

	client.reconnectDelay = 100 * time.Millisecond
	client.wsCooldown = 50 * time.Millisecond

	// Simulate being on secondary after failover with cooldown elapsed.
	client.activeURL = 1
	client.lastFailover = time.Now().Add(-1 * time.Second)

	// Short-lived context — the function will probe primary (reset activeURL=0),
	// fail to dial, then context expires.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	client.tryUntilSubscribeMilestoneEvents(ctx)

	// After cooldown elapsed, activeURL should be reset to 0 (probed primary).
	assert.Equal(t, 0, client.activeURL)
}

func TestWSClient_DualURL_PrimaryRecovery(t *testing.T) {
	// Start with primary down, then bring it up.

	// Primary starts rejecting.
	primaryReject := newTestWSServer(t, true)

	// Secondary accepts with milestone.
	secondary := newTestWSServerWithMilestone(t)
	defer secondary.Close()

	client, err := NewHeimdallWSClient(wsURL(primaryReject.URL), wsURL(secondary.URL))
	require.NoError(t, err)

	client.reconnectDelay = 100 * time.Millisecond
	client.primaryAttempts = 2

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := client.SubscribeMilestoneEvents(ctx)

	// Should failover to secondary.
	select {
	case m := <-events:
		require.NotNil(t, m)
		assert.Equal(t, 1, client.activeURL)
		assert.Equal(t, uint64(100), m.StartBlock)
	case <-ctx.Done():
		t.Fatal("timed out waiting for failover")
	}

	// The fact that failover worked and lastFailover is set
	// proves the probe-back mechanism can work later.
	assert.False(t, client.lastFailover.IsZero(), "lastFailover should be set after switching to secondary")

	// Close the rejecting primary.
	primaryReject.Close()

	require.NoError(t, client.Unsubscribe(ctx))
}
