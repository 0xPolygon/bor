package heimdallws

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// defaultPrimaryAttempts is the number of consecutive failures on the primary URL
	// before switching to the secondary (~30s at 10s/attempt).
	defaultPrimaryAttempts = 3

	// defaultReconnectDelay is the backoff between reconnection attempts.
	defaultReconnectDelay = 10 * time.Second

	// defaultWSCooldown is how long to stay on secondary before probing primary again.
	defaultWSCooldown = 2 * time.Minute
)

// HeimdallWSClient represents a websocket client with auto-reconnection and failover support.
type HeimdallWSClient struct {
	conn      *websocket.Conn
	urls      []string // primary at [0], secondary at [1] (if configured)
	activeURL int      // index into urls; protected by mu
	events    chan *milestone.Milestone
	done      chan struct{}
	mu        sync.Mutex
	probing   atomic.Bool // guards against spawning multiple health-check goroutines

	// Configurable parameters (defaults set in constructor, overridable for testing)
	primaryAttempts int
	reconnectDelay  time.Duration
	wsCooldown      time.Duration
}

// NewHeimdallWSClient creates a new WS client for Heimdall with optional failover.
// The first URL is primary; additional URLs are failover candidates in priority order.
func NewHeimdallWSClient(urls ...string) (*HeimdallWSClient, error) {
	if len(urls) == 0 {
		return nil, errors.New("at least one WS URL required")
	}

	var filtered []string
	for _, u := range urls {
		if u != "" {
			filtered = append(filtered, u)
		}
	}

	if len(filtered) == 0 {
		return nil, errors.New("at least one non-empty WS URL required")
	}

	return &HeimdallWSClient{
		conn:            nil,
		urls:            filtered,
		events:          make(chan *milestone.Milestone),
		done:            make(chan struct{}),
		primaryAttempts: defaultPrimaryAttempts,
		reconnectDelay:  defaultReconnectDelay,
		wsCooldown:      defaultWSCooldown,
	}, nil
}

// SubscribeMilestoneEvents sends the subscription request and starts processing incoming messages.
func (c *HeimdallWSClient) SubscribeMilestoneEvents(ctx context.Context) <-chan *milestone.Milestone {
	c.tryUntilSubscribeMilestoneEvents(ctx)

	// Start the goroutine to read messages.
	go c.readMessages(ctx)

	return c.events
}

// startWSHealthCheck runs in a background goroutine, periodically probing
// higher-priority WS endpoints. When one responds, it updates activeURL and
// closes the current connection to trigger reconnection in readMessages.
func (c *HeimdallWSClient) startWSHealthCheck() {
	defer c.probing.Store(false)

	ticker := time.NewTicker(c.wsCooldown)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}

		c.mu.Lock()
		active := c.activeURL
		c.mu.Unlock()

		if active == 0 {
			return
		}

		// Probe URLs 0..active-1 (highest priority first).
		for i := 0; i < active; i++ {
			testConn, _, err := websocket.DefaultDialer.Dial(c.urls[i], nil)
			if err != nil {
				continue
			}
			testConn.Close()

			c.mu.Lock()
			c.activeURL = i
			conn := c.conn
			c.mu.Unlock()

			log.Info("WS health-check: promoted to higher-priority URL", "index", i, "url", c.urls[i])

			// Close current connection to trigger reconnection in readMessages.
			if conn != nil {
				conn.Close()
			}

			if i == 0 {
				return
			}

			break // keep ticking to probe even higher-priority URLs
		}
	}
}

// tryUntilSubscribeMilestoneEvents retries connecting and subscribing until success,
// with failover to secondary URL after defaultPrimaryAttempts failures on primary.
func (c *HeimdallWSClient) tryUntilSubscribeMilestoneEvents(ctx context.Context) {
	attempts := 0
	firstTime := true

	for {
		if !firstTime {
			time.Sleep(c.reconnectDelay)
		}

		firstTime = false

		// Check for context cancellation or unsubscribe.
		select {
		case <-ctx.Done():
			log.Info("Context cancelled during reconnection")
			return
		case <-c.done:
			log.Info("Client unsubscribed during reconnection")
			return
		default:
		}

		c.mu.Lock()
		active := c.activeURL
		c.mu.Unlock()

		url := c.urls[active]

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Error("failed to dial websocket on heimdall ws subscription", "url", url, "err", err)

			attempts++

			if len(c.urls) > 1 && attempts >= c.primaryAttempts {
				next := min(active+1, len(c.urls)-1)
				if next != active {
					log.Warn("WS URL failed, switching to next",
						"from", c.urls[active], "to", c.urls[next], "attempts", attempts)

					c.mu.Lock()
					c.activeURL = next
					c.mu.Unlock()

					if c.probing.CompareAndSwap(false, true) {
						go c.startWSHealthCheck()
					}
				}

				attempts = 0
			}

			continue
		}

		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()

		// Build the subscription request.
		req := subscriptionRequest{
			JSONRPC: "2.0",
			Method:  "subscribe",
			ID:      0,
		}
		req.Params.Query = "tm.event='NewBlock' AND milestone.number>0"

		if err := c.conn.WriteJSON(req); err != nil {
			log.Error("failed to send subscription request on heimdall ws subscription", "url", url, "err", err)
			continue
		}

		log.Info("successfully connected on heimdall ws subscription", "url", url)

		return
	}
}

// readMessages continuously reads messages from the websocket, handling reconnections if necessary.
func (c *HeimdallWSClient) readMessages(ctx context.Context) {
	defer close(c.events)
	for {
		// Check if the context or unsubscribe signal is set.
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
			// continue to process messages
		}

		if err := c.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			log.Error("failed to set read deadline on heimdall ws subscription", "err", err)

			c.tryUntilSubscribeMilestoneEvents(ctx)
			continue
		}

		_, message, err := c.conn.ReadMessage()
		if err != nil {
			log.Error("connection lost; will attempt to reconnect on heimdall ws subscription", "error", err)

			c.tryUntilSubscribeMilestoneEvents(ctx)
			continue
		}

		var resp wsResponse
		if err := json.Unmarshal(message, &resp); err != nil {
			// Skip messages that don't match the expected format.
			continue
		}

		// Find the milestone event.
		var milestoneEvent *wsEvent
		for _, event := range resp.Result.Data.Value.FinalizeBlock.Events {
			if event.Type == "milestone" {
				// In this case their types are set to the types of the respective iteration values
				// and their scope is the block of the "for" statement; they are re-used in each iteration.
				e := event
				milestoneEvent = &e
				break
			}
		}
		if milestoneEvent == nil {
			continue
		}

		// Map attributes for easier lookup.
		attrs := make(map[string]string)
		for _, attr := range milestoneEvent.Attributes {
			attrs[attr.Key] = attr.Value
		}

		// Build the Milestone object from attributes.
		m := &milestone.Milestone{
			Proposer:    common.HexToAddress(attrs["proposer"]),
			Hash:        common.HexToHash(attrs["hash"]),
			BorChainID:  attrs["bor_chain_id"],
			MilestoneID: attrs["milestone_id"],
		}
		if startBlock, err := strconv.ParseUint(attrs["start_block"], 10, 64); err == nil {
			m.StartBlock = startBlock
		}
		if endBlock, err := strconv.ParseUint(attrs["end_block"], 10, 64); err == nil {
			m.EndBlock = endBlock
		}
		if timestamp, err := strconv.ParseUint(attrs["timestamp"], 10, 64); err == nil {
			m.Timestamp = timestamp
		}
		if totalDifficulty, err := strconv.ParseUint(attrs["total_difficulty"], 10, 64); err == nil {
			m.TotalDifficulty = totalDifficulty
		}

		// Deliver the milestone event, respecting context cancellation.
		select {
		case c.events <- m:
		case <-ctx.Done():
			return
		}
	}
}

// Unsubscribe signals the reader goroutine to stop.
func (c *HeimdallWSClient) Unsubscribe(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		// Already unsubscribed.
	default:
		close(c.done)
	}
	return nil
}

// Close cleanly terminates the websocket connection.
func (c *HeimdallWSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Close()
}
