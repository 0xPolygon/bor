package heimdallws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

type HeimdallEvent string

const (
	MilestoneEventType  HeimdallEvent = "milestone"
	milestoneEventQuery string        = "tm.event='NewBlock' AND milestone.number>0"
	SpanEventType       HeimdallEvent = "span"
	spanEventQuery      string        = "tm.event='NewBlock' AND span.id>0"
)

type eventSubscription struct {
	conn *websocket.Conn
	done chan struct{}
}

// HeimdallWSClient represents a websocket client with auto-reconnection.
type HeimdallWSClient struct {
	subscriptions map[HeimdallEvent]eventSubscription
	url           string // store the URL for reconnection
	done          chan struct{}
	mu            sync.Mutex
}

// NewHeimdallWSClient creates a new WS client for Heimdall.
func NewHeimdallWSClient(url string) (*HeimdallWSClient, error) {
	return &HeimdallWSClient{
		subscriptions: make(map[HeimdallEvent]eventSubscription),
		url:           url,
		done:          make(chan struct{}),
	}, nil
}

func (c *HeimdallWSClient) GetSubscription(eventName HeimdallEvent) (eventSubscription, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sub, ok := c.subscriptions[eventName]
	return sub, ok
}

// SubscribeMilestoneEvents sends the subscription request and starts processing incoming messages
// for milestone events, returning a channel to receive incoming events.
func (c *HeimdallWSClient) SubscribeMilestoneEvents(ctx context.Context) <-chan *milestone.Milestone {
	c.tryUntilSubscribeHeimdallEvents(ctx, milestoneEventQuery, MilestoneEventType)

	events := make(chan *milestone.Milestone)

	// Start the goroutine to read messages.
	go c.readMilestoneMessages(ctx, events)

	return events
}

// SubscribeSpanEvents sends the subscription request and starts processing incoming messages
// for span events, returning a channel to receive incoming events.
func (c *HeimdallWSClient) SubscribeSpanEvents(ctx context.Context) <-chan *span.HeimdallSpanEvent {
	c.tryUntilSubscribeHeimdallEvents(ctx, spanEventQuery, SpanEventType)

	events := make(chan *span.HeimdallSpanEvent)

	// Start the goroutine to read messages.
	go c.readSpanMessages(ctx, events)

	return events
}

// tryUntilSubscribeHeimdallEvents endlessly tries to subscribe and establish a websocket connection
// for the given heimdall event type.
func (c *HeimdallWSClient) tryUntilSubscribeHeimdallEvents(ctx context.Context, eventQuery string, eventType HeimdallEvent) {
	firstTime := true
	for {
		if !firstTime {
			time.Sleep(10 * time.Second)
		}
		firstTime = false

		// Check for context cancellation.
		select {
		case <-ctx.Done():
			log.Info("Context cancelled during reconnection", "event", eventType)
			return
		case <-c.done:
			log.Info("Client unsubscribed during reconnection", "event", eventType)
			return
		default:
		}

		sub, ok := c.GetSubscription(eventType)
		if ok {
			select {
			case <-sub.done:
				log.Info("Client unsubscribed during reconnection", "event", eventType)
				return
			default:
			}
		}

		conn, _, err := websocket.DefaultDialer.Dial(c.url, nil)
		if err != nil {
			log.Error("failed to dial websocket on heimdall ws subscription", "event", eventType, "err", err)
			continue
		}

		// Build the subscription request.
		req := subscriptionRequest{
			JSONRPC: "2.0",
			Method:  "subscribe",
			ID:      0,
		}
		req.Params.Query = eventQuery

		if err := conn.WriteJSON(req); err != nil {
			log.Error("failed to send subscription request on heimdall ws subscription", "event", eventType, "err", err)
			continue
		}

		c.mu.Lock()
		milestoneEvent := eventSubscription{
			conn: conn,
			done: make(chan struct{}),
		}
		c.subscriptions[eventType] = milestoneEvent
		c.mu.Unlock()

		log.Info("Successfully connected on heimdall ws subscription", "event", eventType)
		return
	}
}

// readMilestoneMessages continuously reads messages from the websocket for milestone
// event type, handling reconnections if necessary.
func (c *HeimdallWSClient) readMilestoneMessages(ctx context.Context, events chan *milestone.Milestone) {
	defer close(events)

	sub, ok := c.GetSubscription(MilestoneEventType)
	if !ok || sub.conn == nil {
		c.tryUntilSubscribeHeimdallEvents(ctx, milestoneEventQuery, MilestoneEventType)
		sub, _ = c.GetSubscription(MilestoneEventType)
	}

	for {
		// Check if the context or unsubscribe signal is set.
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-sub.done:
			return
		default:
			// continue to process messages
		}

		conn := sub.conn
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Error("connection lost; will attempt to reconnect on heimdall ws subscription", "error", err)

			c.tryUntilSubscribeHeimdallEvents(ctx, milestoneEventQuery, MilestoneEventType)
			sub, _ = c.GetSubscription(MilestoneEventType)
			continue
		}

		var resp wsResponseMilestone
		if err := json.Unmarshal(message, &resp); err != nil {
			// Skip messages that don't match the expected format.
			continue
		}

		m := &milestone.Milestone{
			Proposer:        resp.MilestoneEvent.Proposer,
			Hash:            resp.MilestoneEvent.Hash,
			BorChainID:      resp.MilestoneEvent.BorChainID,
			MilestoneID:     resp.MilestoneEvent.MilestoneID,
			StartBlock:      resp.MilestoneEvent.StartBlock,
			EndBlock:        resp.MilestoneEvent.EndBlock,
			Timestamp:       resp.MilestoneEvent.Timestamp,
			TotalDifficulty: resp.MilestoneEvent.TotalDifficulty,
		}

		// Deliver the milestone event, respecting context cancellation.
		select {
		case events <- m:
		case <-ctx.Done():
			return
		}
	}
}

// readSpanMessages continuously reads messages from the websocket for span
// event type, handling reconnections if necessary.
func (c *HeimdallWSClient) readSpanMessages(ctx context.Context, events chan *span.HeimdallSpanEvent) {
	defer close(events)

	sub, ok := c.GetSubscription(SpanEventType)
	if !ok || sub.conn == nil {
		c.tryUntilSubscribeHeimdallEvents(ctx, spanEventQuery, SpanEventType)
		sub, _ = c.GetSubscription(SpanEventType)
	}

	for {
		// Check if the context or unsubscribe signal is set.
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-sub.done:
			return
		default:
			// continue to process messages
		}

		conn := sub.conn
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Error("connection lost; will attempt to reconnect on heimdall ws subscription", "error", err)

			c.tryUntilSubscribeHeimdallEvents(ctx, spanEventQuery, SpanEventType)
			sub, _ = c.GetSubscription(SpanEventType)
			continue
		}

		var resp wsResponseSpanEvent
		if err := json.Unmarshal(message, &resp); err != nil {
			// Skip messages that don't match the expected format.
			continue
		}

		s := &span.HeimdallSpanEvent{
			ID:            resp.SpanEvent.ID,
			StartBlock:    resp.SpanEvent.StartBlock,
			EndBlock:      resp.SpanEvent.EndBlock,
			BlockProducer: resp.SpanEvent.BlockProducer,
		}

		// Deliver the span event, respecting context cancellation.
		select {
		case events <- s:
		case <-ctx.Done():
			return
		}
	}
}

// Unsubscribe terminates websocket listener for given `eventType` and stops all read routines.
func (c *HeimdallWSClient) Unsubscribe(eventType HeimdallEvent) {
	sub, ok := c.GetSubscription(eventType)
	if !ok {
		return
	}

	// Close the subscription for the event
	if err := sub.conn.Close(); err != nil {
		log.Error("Failed to close websocket connection", "eventType", eventType, "err", err)
	}

	// Send a close signal to exit all read loops
	close(sub.done)

	// Delete the subscription
	c.mu.Lock()
	delete(c.subscriptions, eventType)
	c.mu.Unlock()
}

// Close cleanly terminates all existing websocket listeners and connections.
func (c *HeimdallWSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close the global channel sending a signal to all subscribers
	close(c.done)

	for eventType, subscription := range c.subscriptions {
		if err := subscription.conn.Close(); err != nil {
			log.Error("Failed to close websocket connection", "eventType", eventType, "err", err)
		}
		close(subscription.done)
	}
}
