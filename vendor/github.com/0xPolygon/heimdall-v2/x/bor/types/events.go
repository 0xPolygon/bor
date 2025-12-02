package types

// bor module event types
const (
	EventTypeProposeSpan   = "propose-span"
	EventTypeBackfillSpans = "backfill-spans"

	AttributeKeySpanID           = "span-id"
	AttributeKeySpanStartBlock   = "start-block"
	AttributeKeySpanEndBlock     = "end-block"
	AttributesKeyLatestSpanId    = "latest-span-id"
	AttributesKeyLatestBorBlock  = "latest-bor-block"
	AttributesKeyLatestBorSpanId = "latest-bor-span-id"

	AttributeValueCategory = ModuleName
)
