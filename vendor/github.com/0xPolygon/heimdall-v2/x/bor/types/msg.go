package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	util "github.com/0xPolygon/heimdall-v2/common/hex"
)

var _ sdk.Msg = &MsgProposeSpan{}
var _ sdk.Msg = &MsgBackfillSpans{}

// NewMsgProposeSpan creates a new MsgProposeSpan instance
func NewMsgProposeSpan(
	spanID uint64,
	proposer string,
	startBlock uint64,
	endBlock uint64,
	chainId string,
	seed []byte,
	seedAuthor string,
) *MsgProposeSpan {
	return &MsgProposeSpan{
		SpanId:     spanID,
		Proposer:   util.FormatAddress(proposer),
		StartBlock: startBlock,
		EndBlock:   endBlock,
		ChainId:    chainId,
		Seed:       seed,
		SeedAuthor: seedAuthor,
	}
}

// Type returns the type of the x/bor MsgProposeSpan.
func (msg MsgProposeSpan) Type() string {
	return EventTypeProposeSpan
}

// Type returns the type of the x/bor MsgBackfillSpans.
func (msg MsgBackfillSpans) Type() string {
	return EventTypeBackfillSpans
}
