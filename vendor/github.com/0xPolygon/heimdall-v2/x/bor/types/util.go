package types

import (
	fmt "fmt"
	"sort"
	"strings"

	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// SortValidatorByAddress sorts a slice of validators by address
// to sort it we compare the values of the Signer(HeimdallAddress i.e. [20]byte)
func SortValidatorByAddress(a []staketypes.Validator) []staketypes.Validator {
	sort.Slice(a, func(i, j int) bool {
		return strings.Compare(a[i].Signer, a[j].Signer) < 0
	})

	return a
}

// SortSpansById sorts spans by SpanID
func SortSpansById(a []Span) {
	sort.Slice(a, func(i, j int) bool {
		return a[i].Id < a[j].Id
	})
}

func IsBlockCloseToSpanEnd(blockNumber, spanEnd uint64) bool {
	// Check if the block number is within 100 blocks of the span end
	return blockNumber <= spanEnd && blockNumber >= (spanEnd-100)
}

func GenerateBorCommittedSpans(latestBorBlock uint64, latestBorUsedSpan *Span) []Span {
	spans := []Span{}
	spanLength := latestBorUsedSpan.EndBlock - latestBorUsedSpan.StartBlock
	prevSpan := latestBorUsedSpan
	for latestBorBlock > prevSpan.EndBlock {
		startBlock := prevSpan.EndBlock + 1
		newSpan := Span{
			Id:                prevSpan.Id + 1,
			StartBlock:        startBlock,
			EndBlock:          startBlock + spanLength,
			BorChainId:        latestBorUsedSpan.BorChainId,
			SelectedProducers: latestBorUsedSpan.SelectedProducers,
			ValidatorSet:      latestBorUsedSpan.ValidatorSet,
		}
		spans = append(spans, newSpan)
		prevSpan = &newSpan
	}
	return spans
}

// CalcCurrentBorSpanId computes the Bor span ID corresponding to latestBorBlock,
// using latestHeimdallSpan as the reference. It returns an error if inputs are invalid
// (nil span, zero or negative span length) or if arithmetic overflow is detected.
func CalcCurrentBorSpanId(latestBorBlock uint64, latestHeimdallSpan *Span) (uint64, error) {
	if latestHeimdallSpan == nil {
		return 0, fmt.Errorf("nil Heimdall span provided")
	}
	if latestHeimdallSpan.EndBlock < latestHeimdallSpan.StartBlock {
		return 0, fmt.Errorf(
			"invalid Heimdall span: EndBlock (%d) must be >= StartBlock (%d)",
			latestHeimdallSpan.EndBlock,
			latestHeimdallSpan.StartBlock,
		)
	}

	if latestBorBlock < latestHeimdallSpan.StartBlock {
		return 0, fmt.Errorf(
			"latestBorBlock (%d) must be >= Heimdall span StartBlock (%d)",
			latestBorBlock,
			latestHeimdallSpan.StartBlock,
		)
	}

	if latestBorBlock <= latestHeimdallSpan.EndBlock {
		return latestHeimdallSpan.Id, nil
	}

	spanLength := latestHeimdallSpan.EndBlock - latestHeimdallSpan.StartBlock + 1

	offset := latestBorBlock - latestHeimdallSpan.StartBlock
	quotient := offset / spanLength

	spanId := latestHeimdallSpan.Id + quotient

	if spanId < latestHeimdallSpan.Id {
		return 0, fmt.Errorf(
			"overflow detected computing span ID: reference ID=%d quotient=%d",
			latestHeimdallSpan.Id, quotient,
		)
	}

	return spanId, nil
}
