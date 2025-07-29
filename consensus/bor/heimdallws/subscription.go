package heimdallws

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
)

// subscriptionRequest represents the JSON-RPC request for subscribing.
type subscriptionRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	ID      int    `json:"id"`
	Params  struct {
		Query string `json:"query"`
	} `json:"params"`
}

// --- Structures to parse the WS response ---

// attribute represents a key/value pair in the event attributes.
type attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index"`
}

// wsEvent represents a single event in the WS response.
type wsEvent struct {
	Type       string      `json:"type"`
	Attributes []attribute `json:"attributes"`
}

// finalizeBlock corresponds to the result_finalize_block field.
type finalizeBlock struct {
	Events []wsEvent `json:"events"`
	// Other fields are omitted.
}

// wsValue represents the "value" portion of the data.
type wsValue struct {
	Block         json.RawMessage `json:"block"` // Omitted
	BlockID       json.RawMessage `json:"block_id"`
	FinalizeBlock finalizeBlock   `json:"result_finalize_block"`
}

// wsData holds the type and value returned.
type wsData struct {
	Type  string  `json:"type"`
	Value wsValue `json:"value"`
}

// wsResult holds the result object.
type wsResult struct {
	Query string `json:"query"`
	Data  wsData `json:"data"`
}

// wsResponse is the top-level response structure from the WS subscription.
type wsResponse struct {
	JSONRPC string   `json:"jsonrpc"`
	ID      int      `json:"id"`
	Result  wsResult `json:"result"`
	// "events" field is present but not needed here.
}

type milestoneEvent struct {
	Proposer        common.Address `json:"milestone.proposer"`
	StartBlock      uint64         `json:"milestone.start_block"`
	EndBlock        uint64         `json:"milestone.end_block"`
	Hash            common.Hash    `json:"milestone.hash"`
	BorChainID      string         `json:"milestone.bor_chain_id"`
	MilestoneID     string         `json:"milestone.milestone_id"`
	Timestamp       uint64         `json:"milestone.timestamp"`
	TotalDifficulty uint64         `json:"milestone.total_difficulty"`
}

type wsResponseMilestone struct {
	JSONRPC        string         `json:"jsonrpc"`
	ID             int            `json:"id"`
	Result         wsResult       `json:"result"`
	MilestoneEvent milestoneEvent `json:"events"`
}

type spanEvent struct {
	ID            uint64 `json:"span.id"`
	StartBlock    uint64 `json:"span.start_block"`
	EndBlock      uint64 `json:"span.end_block"`
	BlockProducer string `json:"span.block_producer"`
}

type wsResponseSpanEvent struct {
	JSONRPC   string    `json:"jsonrpc"`
	ID        int       `json:"id"`
	Result    wsResult  `json:"result"`
	SpanEvent spanEvent `json:"events"`
}
