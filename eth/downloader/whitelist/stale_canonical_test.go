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
	// Not parallel: it marks MilestoneStaleCanonicalMeter, which other tests read.

	byNum, canonical := canonicalTestChain(320, 363)
	milestone := byNum(360) // whitelisted milestone: block 360 with its canonical hash
	current := byNum(363)   // local head is beyond the milestone

	// The downloader's late segment: blocks 330 and 331, both canonical.
	stale := []*types.Header{byNum(330), byNum(331)}

	valid, err := isValidChain(current, stale, true, milestone.Number.Uint64(), milestone.Hash(), canonical, "milestone")
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
	valid, err := isValidChain(current, fork, true, 360, canon.Hash(), nil, "milestone")
	require.NoError(t, err)
	require.False(t, valid, "fork segment below milestone accepted without canonical oracle")

	// With an oracle that maps the numbers to different (canonical) hashes it
	// must still be rejected.
	other := func(n uint64) common.Hash {
		return (&types.Header{Number: new(big.Int).SetUint64(n), Extra: []byte("canon")}).Hash()
	}
	valid, err = isValidChain(current, fork, true, 360, canon.Hash(), other, "milestone")
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
	valid, err := isValidChain(current, segment, true, milestone.Number.Uint64(), milestone.Hash(), canonical, "milestone")
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
	// Not parallel: it asserts on the package-level stale-canonical meters.

	byNum, _ := canonicalTestChain(320, 363)

	reader := NewMockChainReader()
	for n := uint64(320); n <= 363; n++ {
		reader.SetBlock(n, types.NewBlockWithHeader(byNum(n)))
	}
	reader.SetCurrentBlock(byNum(363))

	s := NewMockServiceWithBlockchain(rawdb.NewMemoryDatabase(), reader)
	s.ProcessCheckpoint(340, byNum(340).Hash())
	s.ProcessMilestone(360, byNum(360).Hash())

	checkpointBefore := CheckpointStaleCanonicalMeter.Snapshot().Count()
	milestoneBefore := MilestoneStaleCanonicalMeter.Snapshot().Count()

	// Late re-import of already-canonical blocks below both whitelisted entries.
	stale := []*types.Header{byNum(330), byNum(331)}
	valid, err := s.IsValidChain(byNum(363), stale)
	require.NoError(t, err)
	require.True(t, valid, "stale canonical re-import rejected through Service.IsValidChain")

	// Both services took the accept path and each reported it once.
	require.Equal(t, int64(1), CheckpointStaleCanonicalMeter.Snapshot().Count()-checkpointBefore, "checkpoint stale-canonical meter")
	require.Equal(t, int64(1), MilestoneStaleCanonicalMeter.Snapshot().Count()-milestoneBefore, "milestone stale-canonical meter")

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

// TestServiceWithoutBlockchainKeepsStrictBelowWhitelist: with no chain reader
// the oracle cannot prove anything canonical, so a segment below the
// whitelisted entry keeps the historical strict rejection.
func TestServiceWithoutBlockchainKeepsStrictBelowWhitelist(t *testing.T) {
	t.Parallel()

	byNum, _ := canonicalTestChain(320, 363)

	s := NewMockService(rawdb.NewMemoryDatabase())
	s.SetBlockchain(nil)
	s.ProcessMilestone(360, byNum(360).Hash())

	valid, err := s.IsValidChain(byNum(363), []*types.Header{byNum(330), byNum(331)})
	require.NoError(t, err)
	require.False(t, valid, "segment below the milestone accepted without a chain reader")
}

// TestServiceAcceptsCanonicalReimportWhileLocked covers the second gate the
// milestone service applies once this node has voted on a milestone candidate
// (GetVoteOnHash locks the service until the candidate finalizes, which on a
// validator is the normal state). While locked, IsReorgAllowed refuses every
// segment ending at or below the locked number. A late re-import of blocks
// that are already canonical must still pass, both below the whitelisted
// milestone and in the gap between the whitelisted milestone and the locked
// candidate, while a fork in that range is still rejected.
func TestServiceAcceptsCanonicalReimportWhileLocked(t *testing.T) {
	// Not parallel: it asserts on the package-level stale-canonical meters.

	byNum, _ := canonicalTestChain(320, 363)

	reader := NewMockChainReader()
	for n := uint64(320); n <= 363; n++ {
		reader.SetBlock(n, types.NewBlockWithHeader(byNum(n)))
	}
	reader.SetCurrentBlock(byNum(363))

	s := NewMockServiceWithBlockchain(rawdb.NewMemoryDatabase(), reader)
	s.ProcessMilestone(340, byNum(340).Hash())

	// This node voted for the candidate ending at 362: the service is locked.
	require.True(t, s.LockMutex(362))
	s.UnlockMutex(true, "milestone-362", 362, byNum(362).Hash())
	require.True(t, s.milestoneService.(*milestone).Locked)

	// Sanity: while locked, IsReorgAllowed refuses anything ending at or below 362.
	require.False(t, s.milestoneService.(*milestone).IsReorgAllowed([]*types.Header{byNum(350), byNum(351)}, 362, byNum(362).Hash()))

	lockedBefore := MilestoneLockedCanonicalMeter.Snapshot().Count()
	staleBefore := MilestoneStaleCanonicalMeter.Snapshot().Count()

	// Canonical re-import below the whitelisted milestone: both gates exempt it.
	valid, err := s.IsValidChain(byNum(363), []*types.Header{byNum(330), byNum(331)})
	require.NoError(t, err)
	require.True(t, valid, "canonical re-import below the whitelisted milestone rejected while locked")
	require.Equal(t, int64(1), MilestoneStaleCanonicalMeter.Snapshot().Count()-staleBefore, "stale-canonical meter")
	require.Equal(t, int64(1), MilestoneLockedCanonicalMeter.Snapshot().Count()-lockedBefore, "locked-canonical meter")

	// Canonical re-import between the whitelisted milestone and the locked
	// candidate: only the lock gate is in play, and it must let it through.
	valid, err = s.IsValidChain(byNum(363), []*types.Header{byNum(350), byNum(351)})
	require.NoError(t, err)
	require.True(t, valid, "canonical re-import below the locked candidate rejected")
	require.Equal(t, int64(2), MilestoneLockedCanonicalMeter.Snapshot().Count()-lockedBefore, "locked-canonical meter")
	require.Equal(t, int64(1), MilestoneStaleCanonicalMeter.Snapshot().Count()-staleBefore, "stale-canonical meter must not double count")

	// A fork below the locked candidate is still a contradiction of the vote.
	fork350 := &types.Header{Number: big.NewInt(350), ParentHash: byNum(349).Hash(), Extra: []byte("fork")}
	fork351 := &types.Header{Number: big.NewInt(351), ParentHash: fork350.Hash(), Extra: []byte("fork")}
	valid, err = s.IsValidChain(byNum(363), []*types.Header{fork350, fork351})
	require.NoError(t, err)
	require.False(t, valid, "fork below the locked candidate accepted")

	// A segment that carries the locked block with a different hash is still rejected.
	fork362 := &types.Header{Number: big.NewInt(362), ParentHash: byNum(361).Hash(), Extra: []byte("fork")}
	valid, err = s.IsValidChain(byNum(363), []*types.Header{byNum(361), fork362, {Number: big.NewInt(363), ParentHash: fork362.Hash()}})
	require.NoError(t, err)
	require.False(t, valid, "segment contradicting the locked candidate accepted")
}
