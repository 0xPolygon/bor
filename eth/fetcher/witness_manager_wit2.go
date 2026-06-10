package fetcher

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/log"
)

// WIT2 fast-path tuning: how the manager re-polls announce-only relayers that
// answer "body not ready yet" while still pulling the witness themselves.
const (
	// emptyResponseFastRetries is how many consecutive "body not ready yet"
	// (empty) responses we re-poll immediately before backing off. WIT2's fast
	// signed announce reaches us ahead of the body, so the only candidate body
	// source is often an announce-only relayer that has not finished pulling +
	// importing the block. The first couple of re-polls stay immediate so we
	// pick the body up the instant the relayer obtains it (the common case);
	// after that, a relayer answering empty is genuinely waiting on its own
	// upstream and re-polling it every ~gatherSlack only hammers it.
	emptyResponseFastRetries = 2

	// emptyResponseBaseBackoff / emptyResponseMaxBackoff bound the exponential
	// backoff applied to repeated empty responses past the fast-retry window.
	// The witness provably exists (a BP signed its hash) so we never give the
	// request up here; we only slow the poll cadence to avoid the empty-poll
	// storm observed on devnet (~15x the WIT1 empty-response count).
	emptyResponseBaseBackoff = 100 * time.Millisecond
	emptyResponseMaxBackoff  = 1 * time.Second
)

// cacheVerifiedWitnessForServing forwards canonical-encoded witness bytes
// (already verified against a BP-signed witness hash by the caller) to the
// handler so other peers can fetch them pre-import. No-op when no cache
// callback is configured (legacy WIT1-only paths) or when body is empty —
// the latter signals the WIT1 path with no signed hash on file, where
// caching unverified bytes would expose us to byte-blame from downstream
// peers.
func (m *witnessManager) cacheVerifiedWitnessForServing(blockHash common.Hash, body []byte, witnessHash common.Hash) {
	if m.parentCacheWitnessForServing == nil || len(body) == 0 {
		return
	}
	m.parentCacheWitnessForServing(blockHash, body, witnessHash)
}

// verifyAgainstSignedHash returns the canonically-encoded witness bytes and
// the BP-signed witness hash they match, when a signed hash is on file and
// verification succeeds. body is nil on the WIT1 path (no signed hash to
// verify against) so callers can skip the pre-import serving cache. ok is
// false when verification fails; the offending peer has already been
// reported. Local EncodeRLP failure on a successfully-decoded witness is
// the local node's bug, not peer misbehavior, so it does not drop the peer.
func (m *witnessManager) verifyAgainstSignedHash(peer string, hash common.Hash, witness *stateless.Witness) (body []byte, witnessHash common.Hash, ok bool) {
	if m.parentSignedWitnessHash == nil {
		return nil, common.Hash{}, true
	}
	expected, has := m.parentSignedWitnessHash(hash)
	if !has {
		return nil, common.Hash{}, true
	}
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		log.Warn("[wm] Failed to encode received witness for hash check", "peer", peer, "hash", hash, "err", err)
		m.handleWitnessFetchFailureExt(hash, "", fmt.Errorf("witness encode failed: %w", err), false)
		return nil, common.Hash{}, false
	}
	encoded := buf.Bytes()
	actual := stateless.WitnessCommitHash(encoded)
	if actual != expected {
		witnessByteMismatchMeter.Mark(1)
		// We cannot blame the byte-server on signed-hash disagreement alone:
		// the announcement only proves *some* BP signed *some* hash. A faulty
		// or malicious scheduled producer that signed a bogus hash would
		// otherwise weaponise this path to disconnect every honest peer
		// serving the canonical witness. Reject the bytes (don't cache for
		// serving), back off the pending request so another peer/announcement
		// gets tried, and let import-time execution validation pin blame.
		// TODO(wit2): wire signer-quarantine once the manager has access to
		// (signer, announcement-relayer) provenance from the handler.
		log.Warn("[wm] Witness bytes do not match BP-signed hash; not caching, retrying with another peer",
			"peer", peer, "block", hash, "expected", expected, "actual", actual)
		m.handleWitnessFetchFailureExt(hash, "", errors.New("witness hash mismatch"), false)
		return nil, common.Hash{}, false
	}
	return encoded, expected, true
}

// handleWitnessBodyNotReady backs off a pending witness request after an empty
// ("body not ready yet") response, without dropping the responder and without
// giving the request up. On the WIT2 fast path the signed announce reaches us
// ahead of the body, so the only candidate source is frequently an
// announce-only relayer still pulling+importing the block; it answers empty
// until it has the bytes. The first emptyResponseFastRetries re-polls stay
// immediate to catch the body the instant the relayer obtains it; beyond that
// we back off exponentially (capped) so a relayer that is itself waiting
// upstream is not hammered every ~gatherSlack. The witness provably exists — a
// BP signed its hash — so we never discard the request here.
func (m *witnessManager) handleWitnessBodyNotReady(hash common.Hash) {
	m.mu.Lock()
	if state := m.pending[hash]; state != nil && state.announce != nil {
		state.emptyRetries++
		state.announce.time = time.Now().Add(emptyResponseBackoff(state.emptyRetries))
	}
	m.mu.Unlock()

	m.rescheduleWitness()
}

// emptyResponseBackoff returns how far into the future the next re-poll should
// be deferred after n consecutive empty responses. The first
// emptyResponseFastRetries attempts return 0 (re-poll on the next tick); past
// that the delay doubles from emptyResponseBaseBackoff up to
// emptyResponseMaxBackoff.
func emptyResponseBackoff(n int) time.Duration {
	if n <= emptyResponseFastRetries {
		return 0
	}
	shift := uint(n - emptyResponseFastRetries - 1)
	// Cap the shift so the left-shift can't overflow before the clamp below.
	if shift > 16 {
		shift = 16
	}
	d := emptyResponseBaseBackoff << shift
	if d > emptyResponseMaxBackoff {
		d = emptyResponseMaxBackoff
	}
	return d
}
