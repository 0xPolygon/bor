package whitelist

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

// canonicalTestChain builds a tiny canonical chain [start, end] with real
// parent-hash linkage and returns a by-number accessor plus the canonical-hash
// oracle that SetBlockchain would wire in production.
func canonicalTestChain(start, end uint64) (byNum func(uint64) *types.Header, canonical func(uint64) common.Hash) {
	headers := make([]*types.Header, 0, end-start+1)
	var parent *types.Header
	for n := start; n <= end; n++ {
		h := &types.Header{Number: new(big.Int).SetUint64(n)}
		if parent != nil {
			h.ParentHash = parent.Hash()
		}
		headers = append(headers, h)
		parent = h
	}
	byNum = func(n uint64) *types.Header { return headers[n-start] }
	canonical = func(n uint64) common.Hash {
		if n < start || n > end {
			return common.Hash{}
		}
		return byNum(n).Hash()
	}
	return byNum, canonical
}

// TestIsValidChainAcceptsStaleCanonicalSegment reproduces the INC-192 false
// positive: the downloader re-imports blocks 330-331 that the block fetcher
// already imported, after the whitelisted milestone advanced to 360 and the
// local head to 363. Every header in the segment is canonical (their hashes
// are exactly what the milestone chain is built on), yet without the canonical
// oracle isValidChain reports the segment as invalid because it lies entirely
// below the milestone number.
func TestIsValidChainAcceptsStaleCanonicalSegment(t *testing.T) {
	t.Parallel()

	byNum, canonical := canonicalTestChain(320, 363)
	milestone := byNum(360) // whitelisted milestone: block 360 with its canonical hash
	current := byNum(363)   // local head is beyond the milestone

	// The downloader's late segment: blocks 330 and 331, both canonical.
	stale := []*types.Header{byNum(330), byNum(331)}

	valid, err := isValidChain(current, stale, true, milestone.Number.Uint64(), milestone.Hash(), canonical)
	require.NoError(t, err)
	require.True(t, valid, "canonical segment [330,331] rejected as whitelist mismatch (milestone=360 head=363): this is the INC-192 false positive")
}

// TestIsValidChainStillRejectsForkBelowMilestone guards the intended behaviour:
// a segment below the milestone that is NOT on the canonical chain must still
// be rejected once the local head is past the milestone, with or without the
// canonical oracle.
func TestIsValidChainStillRejectsForkBelowMilestone(t *testing.T) {
	t.Parallel()

	canon := &types.Header{Number: big.NewInt(360)}
	current := &types.Header{Number: big.NewInt(363)}
	fork := []*types.Header{
		{Number: big.NewInt(330), Extra: []byte("fork")},
		{Number: big.NewInt(331), Extra: []byte("fork")},
	}

	// Without a canonical oracle the strict behaviour must be kept.
	valid, err := isValidChain(current, fork, true, 360, canon.Hash(), nil)
	require.NoError(t, err)
	require.False(t, valid, "fork segment below milestone accepted without canonical oracle")

	// With an oracle that maps the numbers to different (canonical) hashes it
	// must still be rejected.
	other := func(n uint64) common.Hash {
		return (&types.Header{Number: new(big.Int).SetUint64(n), Extra: []byte("canon")}).Hash()
	}
	valid, err = isValidChain(current, fork, true, 360, canon.Hash(), other)
	require.NoError(t, err)
	require.False(t, valid, "fork segment below milestone accepted with canonical oracle")
}

// TestIsValidChainRejectsPartiallyCanonicalSegment: a segment that starts on
// the canonical chain but leaves it is a reorg attempt below finality, not a
// harmless re-import. Only a segment whose every header is canonical passes.
func TestIsValidChainRejectsPartiallyCanonicalSegment(t *testing.T) {
	t.Parallel()

	byNum, canonical := canonicalTestChain(320, 363)
	milestone := byNum(360)
	current := byNum(363)

	forked331 := &types.Header{Number: big.NewInt(331), ParentHash: byNum(330).Hash(), Extra: []byte("fork")}
	require.NotEqual(t, byNum(331).Hash(), forked331.Hash())

	segment := []*types.Header{byNum(330), forked331}
	valid, err := isValidChain(current, segment, true, milestone.Number.Uint64(), milestone.Hash(), canonical)
	require.NoError(t, err)
	require.False(t, valid, "segment with a non-canonical header below the milestone was accepted")
}

// TestServiceAcceptsStaleCanonicalReimport drives the public entry point that
// core uses (ForkChoice.ValidateReorg -> Service.IsValidChain) with the
// canonical oracle wired through SetBlockchain, and with both a checkpoint and
// a milestone whitelisted above the re-imported segment. Both services run the
// same below-whitelist branch, so both need the oracle for the re-import to be
// accepted.
func TestServiceAcceptsStaleCanonicalReimport(t *testing.T) {
	t.Parallel()

	byNum, _ := canonicalTestChain(320, 363)

	reader := NewMockChainReader()
	for n := uint64(320); n <= 363; n++ {
		reader.SetBlock(n, types.NewBlockWithHeader(byNum(n)))
	}
	reader.SetCurrentBlock(byNum(363))

	s := NewMockServiceWithBlockchain(rawdb.NewMemoryDatabase(), reader)
	s.ProcessCheckpoint(340, byNum(340).Hash())
	s.ProcessMilestone(360, byNum(360).Hash())

	// Late re-import of already-canonical blocks below both whitelisted entries.
	stale := []*types.Header{byNum(330), byNum(331)}
	valid, err := s.IsValidChain(byNum(363), stale)
	require.NoError(t, err)
	require.True(t, valid, "stale canonical re-import rejected through Service.IsValidChain")

	// A fork below the whitelisted entries is still rejected.
	fork330 := &types.Header{Number: big.NewInt(330), ParentHash: byNum(329).Hash(), Extra: []byte("fork")}
	fork331 := &types.Header{Number: big.NewInt(331), ParentHash: fork330.Hash(), Extra: []byte("fork")}
	valid, err = s.IsValidChain(byNum(363), []*types.Header{fork330, fork331})
	require.NoError(t, err)
	require.False(t, valid, "fork below the whitelisted entries accepted through Service.IsValidChain")

	// A block the local chain does not know at all (beyond the oracle's range)
	// below the whitelist is not provably canonical and is still rejected.
	unknown := &types.Header{Number: big.NewInt(300)}
	valid, err = s.IsValidChain(byNum(363), []*types.Header{unknown})
	require.NoError(t, err)
	require.False(t, valid, "unknown block below the whitelisted entries accepted")
}
