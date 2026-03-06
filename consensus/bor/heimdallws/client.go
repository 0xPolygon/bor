package heimdallws

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrNoURLs         = errors.New("at least one WS URL required")
	ErrNoNonEmptyURLs = errors.New("at least one non-empty WS URL required")
)

const (
	// defaultReconnectDelay is the backoff between reconnection attempts.
	defaultReconnectDelay = 10 * time.Second

	// defaultWSProbeTimeout bounds each individual WS probe dial so a
	// firewalled host can't block the health-check goroutine forever.
	defaultWSProbeTimeout = 10 * time.Second
)

// HeimdallWSClient represents a websocket client with auto-reconnection and failover support.
type HeimdallWSClient struct {
	conn      *websocket.Conn
	connEpoch uint64   // incremented on each connection change; detects proactive switches
	urls      []string // primary at [0], secondary at [1] (if configured)
	registry  *heimdall.HealthRegistry
	events    chan *milestone.Milestone
	done      chan struct{}
	mu        sync.Mutex

	// Configurable parameters (defaults set in constructor, overridable for testing)
	reconnectDelay time.Duration
	probeTimeout   time.Duration
}

// NewHeimdallWSClient creates a new WS client for Heimdall with optional failover.
// The first URL is primary; additional URLs are failover candidates in priority order.
func NewHeimdallWSClient(urls ...string) (*HeimdallWSClient, error) {
	if len(urls) == 0 {
		return nil, ErrNoURLs
	}

	var filtered []string
	for _, u := range urls {
		if u != "" {
			filtered = append(filtered, u)
		}
	}

	if len(filtered) == 0 {
		return nil, ErrNoNonEmptyURLs
	}

	c := &HeimdallWSClient{
		conn:           nil,
		urls:           filtered,
		events:         make(chan *milestone.Milestone),
		done:           make(chan struct{}),
		reconnectDelay: defaultReconnectDelay,
		probeTimeout:   defaultWSProbeTimeout,
	}

	c.registry = heimdall.NewHealthRegistry(
		len(filtered),
		c.probeWSEndpoint,
		c.onWSSwitch,
		heimdall.RegistryMetrics{
			ProbeAttempts:     heimdall.FailoverWSProbeAttempts,
			ProbeSuccesses:    heimdall.FailoverWSProbeSuccesses,
			ProactiveSwitches: heimdall.FailoverWSProactiveSwitches,
			ActiveGauge:       heimdall.FailoverWSActiveGauge,
			HealthyEndpoints:  heimdall.FailoverWSHealthyEndpoints,
		},
	)

	return c, nil
}

// probeWSEndpoint dials a WS endpoint and immediately closes the connection.
func (c *HeimdallWSClient) probeWSEndpoint(i int) error {
	c.mu.Lock()
	url := c.urls[i]
	c.mu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: c.probeTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.probeTimeout)
	defer cancel()

	testConn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}

	testConn.Close()

	return nil
}

// onWSSwitch is called by the registry (under registry lock) when the active
// endpoint changes. It bumps the connection epoch, closes the current connection,
// and nils it out. The epoch change lets readMessages distinguish a proactive
// switch from a real network error, avoiding misleading logs and double-closes.
func (c *HeimdallWSClient) onWSSwitch(from, to int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connEpoch++

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// connEpochChanged reports whether the connection epoch has advanced past the
// given snapshot, indicating that a proactive switch (or reconnection) occurred.
func (c *HeimdallWSClient) connEpochChanged(epoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.connEpoch != epoch
}

// SubscribeMilestoneEvents sends the subscription request and starts processing incoming messages.
func (c *HeimdallWSClient) SubscribeMilestoneEvents(ctx context.Context) <-chan *milestone.Milestone {
	c.tryUntilSubscribeMilestoneEvents(ctx)

	// Start the goroutine to read messages.
	go c.readMessages(ctx)

	// Start the health registry if there are multiple URLs.
	if len(c.urls) > 1 {
		c.registry.Start()
	}

	return c.events
}

// tryUntilSubscribeMilestoneEvents retries connecting and subscribing until success,
// consulting the health registry to pick the best URL.
func (c *HeimdallWSClient) tryUntilSubscribeMilestoneEvents(ctx context.Context) {
	firstTime := true

	for {
		if !firstTime {
			select {
			case <-ctx.Done():
				log.Info("Context cancelled during reconnection")
				return
			case <-c.done:
				log.Info("Client unsubscribed during reconnection")
				return
			case <-time.After(c.reconnectDelay):
			}
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

		active := c.registry.Active()
		url := c.urls[active]

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Error("failed to dial websocket on heimdall ws subscription", "url", url, "err", err)

			// Mark endpoint unhealthy in the registry.
			c.registry.MarkUnhealthy(active, err)

			// Find the best healthy alternative.
			snap := c.registry.HealthSnapshot()
			switched := false

			for i := 0; i < len(c.urls); i++ {
				if i == active {
					continue
				}

				if snap[i].Healthy {
					c.registry.SetActive(i)
					switched = true

					heimdall.FailoverWSSwitchCounter.Inc(1)

					log.Warn("WS URL failed, switching to healthy endpoint",
						"from", c.urls[active], "to", c.urls[i])

					break
				}
			}

			// If no healthy alternative, try next in round-robin fashion.
			if !switched && len(c.urls) > 1 {
				next := (active + 1) % len(c.urls)
				if next != active {
					c.registry.SetActive(next)

					heimdall.FailoverWSSwitchCounter.Inc(1)

					log.Warn("WS URL failed, switching to next endpoint",
						"from", c.urls[active], "to", c.urls[next])
				}
			}

			continue
		}

		// Close previous connection if any, then set the new one.
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.conn = conn
		c.connEpoch++

		// Build the subscription request and send it under lock to avoid
		// racing with readMessages on c.conn.
		req := subscriptionRequest{
			JSONRPC: "2.0",
			Method:  "subscribe",
			ID:      0,
		}
		req.Params.Query = "tm.event='NewBlock' AND milestone.number>0"

		err = c.conn.WriteJSON(req)
		c.mu.Unlock()

		// Mark outside c.mu to prevent lock-ordering deadlock with
		// registry.mu → c.mu (onWSSwitch called from health-check goroutine).
		c.registry.MarkSuccess(active)

		if err != nil {
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

		// Grab local ref and epoch under lock to detect proactive switches.
		c.mu.Lock()
		conn := c.conn
		epoch := c.connEpoch
		c.mu.Unlock()

		if conn == nil {
			c.tryUntilSubscribeMilestoneEvents(ctx)
			continue
		}

		if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			if c.connEpochChanged(epoch) {
				// Proactive switch closed the connection; loop back to pick up the new endpoint.
				log.Info("reconnecting due to endpoint switch on heimdall ws subscription")
				continue
			}

			log.Error("failed to set read deadline on heimdall ws subscription", "err", err)

			c.tryUntilSubscribeMilestoneEvents(ctx)
			continue
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if c.connEpochChanged(epoch) {
				// Proactive switch closed the connection; loop back to pick up the new endpoint.
				log.Info("reconnecting due to endpoint switch on heimdall ws subscription")
				continue
			}

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
	select {
	case <-c.done:
		// Already unsubscribed.
	default:
		close(c.done)
	}
	c.mu.Unlock()

	// Stop the registry outside c.mu to avoid deadlock with probeWSEndpoint,
	// which acquires c.mu to read the URL while running under the registry's
	// run() goroutine.
	c.registry.Stop()

	return nil
}

// Close cleanly terminates the websocket connection.
func (c *HeimdallWSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}
