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

const (
	// defaultReconnectDelay is the backoff between reconnection attempts.
	defaultReconnectDelay = 10 * time.Second

	// defaultWSHealthCheckInterval is how often the health registry probes all endpoints.
	defaultWSHealthCheckInterval = 10 * time.Second

	// defaultWSConsecutiveThreshold is the number of consecutive successful probes
	// needed before an endpoint is considered healthy.
	defaultWSConsecutiveThreshold = 3

	// defaultWSPromotionCooldown is how long after becoming healthy before an
	// endpoint is eligible for promotion.
	defaultWSPromotionCooldown = 60 * time.Second

	// defaultWSProbeTimeout bounds each individual WS probe dial so a
	// firewalled host can't block the health-check goroutine forever.
	defaultWSProbeTimeout = 10 * time.Second
)

// wsEndpointHealth tracks the health state of a single WS endpoint.
type wsEndpointHealth struct {
	healthy            bool
	consecutiveSuccess int
	healthySince       time.Time
	lastErr            error
}

// HeimdallWSClient represents a websocket client with auto-reconnection and failover support.
type HeimdallWSClient struct {
	conn      *websocket.Conn
	urls      []string // primary at [0], secondary at [1] (if configured)
	activeURL int      // index into urls; protected by mu
	health    []wsEndpointHealth
	events    chan *milestone.Milestone
	done      chan struct{}
	mu        sync.Mutex

	// Configurable parameters (defaults set in constructor, overridable for testing)
	reconnectDelay       time.Duration
	healthCheckInterval  time.Duration
	consecutiveThreshold int
	promotionCooldown    time.Duration
	probeTimeout         time.Duration
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

	health := make([]wsEndpointHealth, len(filtered))
	// Primary starts as healthy; others start unhealthy.
	health[0] = wsEndpointHealth{healthy: true}

	return &HeimdallWSClient{
		conn:                 nil,
		urls:                 filtered,
		health:               health,
		events:               make(chan *milestone.Milestone),
		done:                 make(chan struct{}),
		reconnectDelay:       defaultReconnectDelay,
		healthCheckInterval:  defaultWSHealthCheckInterval,
		consecutiveThreshold: defaultWSConsecutiveThreshold,
		promotionCooldown:    defaultWSPromotionCooldown,
		probeTimeout:         defaultWSProbeTimeout,
	}, nil
}

// SubscribeMilestoneEvents sends the subscription request and starts processing incoming messages.
func (c *HeimdallWSClient) SubscribeMilestoneEvents(ctx context.Context) <-chan *milestone.Milestone {
	c.tryUntilSubscribeMilestoneEvents(ctx)

	// Start the goroutine to read messages.
	go c.readMessages(ctx)

	// Start the health registry if there are multiple URLs.
	if len(c.urls) > 1 {
		go c.runWSHealthRegistry()
	}

	return c.events
}

// runWSHealthRegistry is an always-on goroutine that continuously probes ALL WS
// endpoints, requires consecutive successes before marking healthy, and enforces
// cooldown before promotion. Stopped when done channel is closed (Unsubscribe).
func (c *HeimdallWSClient) runWSHealthRegistry() {
	ticker := time.NewTicker(c.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}

		c.probeAllWSEndpoints()
		c.maybeWSPromote()
		c.maybeWSProactiveSwitch()
	}
}

// probeAllWSEndpoints probes every WS endpoint via dial (connect + immediately close).
func (c *HeimdallWSClient) probeAllWSEndpoints() {
	dialer := websocket.Dialer{
		HandshakeTimeout: c.probeTimeout,
	}

	for i := 0; i < len(c.urls); i++ {
		// Check for shutdown between individual probes.
		select {
		case <-c.done:
			return
		default:
		}

		heimdall.FailoverWSProbeAttempts.Inc(1)

		c.mu.Lock()
		url := c.urls[i]
		c.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), c.probeTimeout)
		testConn, _, err := dialer.DialContext(ctx, url, nil)
		cancel()

		c.mu.Lock()

		if err == nil {
			testConn.Close()

			c.health[i].consecutiveSuccess++
			c.health[i].lastErr = nil

			if c.health[i].consecutiveSuccess >= c.consecutiveThreshold && !c.health[i].healthy {
				c.health[i].healthy = true
				c.health[i].healthySince = time.Now()
			}

			heimdall.FailoverWSProbeSuccesses.Inc(1)
		} else {
			c.health[i].consecutiveSuccess = 0
			c.health[i].healthy = false
			c.health[i].lastErr = err
		}

		c.mu.Unlock()
	}

	// Update healthy endpoints gauge.
	c.mu.Lock()
	count := int64(0)
	for i := range c.health {
		if c.health[i].healthy {
			count++
		}
	}
	c.mu.Unlock()

	heimdall.FailoverWSHealthyEndpoints.Update(count)
}

// maybeWSPromote checks if a higher-priority URL (index < activeURL) is healthy
// and has passed cooldown. If yes, promotes to the highest-priority qualified URL.
func (c *HeimdallWSClient) maybeWSPromote() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.activeURL == 0 {
		return
	}

	for i := 0; i < c.activeURL; i++ {
		if c.health[i].healthy && time.Since(c.health[i].healthySince) >= c.promotionCooldown {
			prev := c.activeURL
			c.activeURL = i

			heimdall.FailoverWSActiveGauge.Update(int64(i))
			heimdall.FailoverWSProactiveSwitches.Inc(1)

			log.Info("WS health registry: promoted to higher-priority URL",
				"index", i, "previous", prev, "url", c.urls[i])

			// Close current connection to trigger reconnection in readMessages.
			if c.conn != nil {
				c.conn.Close()
			}

			return
		}
	}
}

// maybeWSProactiveSwitch detects if the active URL is unhealthy and switches
// to the highest-priority healthy URL.
func (c *HeimdallWSClient) maybeWSProactiveSwitch() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.health[c.activeURL].healthy {
		return
	}

	// Active is unhealthy. Find the best alternative.
	// Pass 1: healthy + cooled.
	for i := 0; i < len(c.urls); i++ {
		if i == c.activeURL {
			continue
		}

		if c.health[i].healthy && time.Since(c.health[i].healthySince) >= c.promotionCooldown {
			prev := c.activeURL
			c.activeURL = i

			heimdall.FailoverWSActiveGauge.Update(int64(i))
			heimdall.FailoverWSProactiveSwitches.Inc(1)

			log.Warn("WS health registry: proactive switch (active unhealthy, cooled target)",
				"from", prev, "to", i, "url", c.urls[i])

			if c.conn != nil {
				c.conn.Close()
			}

			return
		}
	}

	// Pass 2: healthy but NOT cooled (emergency).
	for i := 0; i < len(c.urls); i++ {
		if i == c.activeURL {
			continue
		}

		if c.health[i].healthy {
			prev := c.activeURL
			c.activeURL = i

			heimdall.FailoverWSActiveGauge.Update(int64(i))
			heimdall.FailoverWSProactiveSwitches.Inc(1)

			log.Warn("WS health registry: proactive switch (active unhealthy, uncooled target)",
				"from", prev, "to", i, "url", c.urls[i])

			if c.conn != nil {
				c.conn.Close()
			}

			return
		}
	}
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

		c.mu.Lock()
		active := c.activeURL
		c.mu.Unlock()

		url := c.urls[active]

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Error("failed to dial websocket on heimdall ws subscription", "url", url, "err", err)

			// Mark endpoint unhealthy in the registry.
			c.mu.Lock()
			c.health[active].consecutiveSuccess = 0
			c.health[active].healthy = false
			c.health[active].lastErr = err

			// Find the best healthy alternative.
			switched := false
			for i := 0; i < len(c.urls); i++ {
				if i == active && c.health[i].healthy {
					continue
				}

				if i != active && c.health[i].healthy {
					c.activeURL = i
					switched = true

					heimdall.FailoverWSSwitchCounter.Inc(1)
					heimdall.FailoverWSActiveGauge.Update(int64(i))

					log.Warn("WS URL failed, switching to healthy endpoint",
						"from", c.urls[active], "to", c.urls[i])

					break
				}
			}

			// If no healthy alternative, try next in round-robin fashion.
			if !switched && len(c.urls) > 1 {
				next := (active + 1) % len(c.urls)
				if next != active {
					c.activeURL = next

					heimdall.FailoverWSSwitchCounter.Inc(1)
					heimdall.FailoverWSActiveGauge.Update(int64(next))

					log.Warn("WS URL failed, switching to next endpoint",
						"from", c.urls[active], "to", c.urls[next])
				}
			}

			c.mu.Unlock()

			continue
		}

		c.mu.Lock()
		c.conn = conn
		// Mark this endpoint as successful.
		c.health[active].consecutiveSuccess++
		if c.health[active].consecutiveSuccess >= c.consecutiveThreshold && !c.health[active].healthy {
			c.health[active].healthy = true
			c.health[active].healthySince = time.Now()
		}
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
