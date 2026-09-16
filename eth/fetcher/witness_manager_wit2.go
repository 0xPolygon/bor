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

// verifyAgainstSignedHash applies the WIT2 size oracle to a received witness.
// When a BP-signed announcement is on file, a witness whose encoded size is
// within the accepted band around the signed WitnessSize is accepted for import
// (ok=true) — because witnesses are non-deterministic, a differing hash is NOT a
// failure; only an oversized witness is rejected (and the serving peer struck).
//
// body (the canonical bytes for the pre-import serving cache) is returned ONLY
// when the witness is byte-identical to the BP's (hash match). A within-band but
// non-identical variant is imported locally but returns body=nil, so it is not
// re-served or relayed: the serving and relay fast-paths carry only the BP's own
// bytes, keyed by the signed hash, so a downstream byte check against that hash
// stays meaningful.
//
// body is also nil on the WIT1 path (no signed announcement). ok is false only
// when the witness is oversized or a local EncodeRLP failure occurs; the latter
// is the local node's own error, not a peer fault, so it does not strike.
func (m *witnessManager) verifyAgainstSignedHash(peer string, hash common.Hash, witness *stateless.Witness) (body []byte, witnessHash common.Hash, ok bool) {
	if m.parentSignedWitnessHash == nil {
		return nil, common.Hash{}, true
	}
	expected, expectedSize, has := m.parentSignedWitnessHash(hash)
	if !has || m.isSignedHashQuarantined(hash) {
		// No signed announcement on file, or it has been quarantined after
		// distinct servers repeatedly served oversized bytes: fall back to the
		// WIT1 path so import-time execution arbitrates the bytes.
		return nil, common.Hash{}, true
	}
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		log.Warn("[wm] Failed to encode received witness for size check", "peer", peer, "hash", hash, "err", err)
		m.handleWitnessFetchFailureExt(hash, "", fmt.Errorf("witness encode failed: %w", err), false)
		return nil, common.Hash{}, false
	}
	encoded := buf.Bytes()
	actualSize := uint64(len(encoded))
	actual := stateless.WitnessCommitHash(encoded)

	// Non-determinism-tolerant acceptance (WIT2 size oracle).
	//
	// Witnesses are NOT deterministic across nodes: BlockSTM speculative reads
	// make honest nodes collect different-but-valid trie-node sets, so a valid
	// witness routinely hashes differently from the BP-signed WitnessHash. We
	// therefore do NOT reject or strike on hash divergence. Instead the
	// BP-signed WitnessSize is used as a size oracle: accept for import any
	// witness whose encoded size is within acceptableWitnessSizeCeiling(signedSize)
	// and let import-time state-root execution arbitrate content-correctness.
	// Content-correctness is the responsibility of the producer that signed the
	// announcement (via the header producer binding), not of a relaying or
	// serving peer — preserving WIT2's core property of relaying/serving a
	// trusted witness before self-validating it. A within-band witness is
	// re-served/relayed only when it is byte-identical to the BP's (hash match);
	// a valid non-deterministic variant is imported locally but not re-served, so
	// the signed hash stays a faithful identifier of the bytes on the fast-path.
	//
	// Only an oversized witness — beyond the signed-size band and the retained
	// gas-derived absolute cap — is rejected here, since it exceeds any
	// plausible non-deterministic variation. The serving peer is struck (first
	// occurrence per (peer, block), reusing the distinct-server bookkeeping);
	// when distinct servers all oversize the same block the signed size is
	// quarantined and the block falls back to the WIT1 page-count path.
	ceiling := m.acceptableWitnessSizeCeiling(expectedSize)
	if witnessSizeExceedsCeiling(actualSize, ceiling) {
		witnessOversizedMeter.Mark(1)
		quarantined, firstForPeer := m.recordSignedHashMismatch(hash, peer)
		if quarantined {
			log.Warn("[wm] BP-signed witness size band exceeded by distinct servers; quarantining to WIT1 fallback so the block can import",
				"block", hash, "signedSize", expectedSize, "ceiling", ceiling)
		} else {
			log.Warn("[wm] Witness exceeds BP-signed size band; not caching, retrying with another peer",
				"peer", peer, "block", hash, "signedSize", expectedSize, "ceiling", ceiling, "received", actualSize)
		}
		if firstForPeer && peer != "" && m.parentStrikeWitnessServer != nil {
			m.parentStrikeWitnessServer(peer)
		}
		m.handleWitnessFetchFailureExt(hash, "", errors.New("witness exceeds signed size band"), false)
		return nil, common.Hash{}, false
	}

	// Within band: forget any earlier oversize noise for this block.
	m.clearSignedHashMismatch(hash)

	if actual != expected {
		// A valid, non-deterministic variant of the BP's witness. Accept it for
		// import (state-root execution validates), but return body=nil so it is
		// NOT cached for pre-import serving or relayed — those fast-paths carry
		// only the BP's own bytes so a downstream check against the signed hash
		// stays meaningful. Observability only.
		witnessHashDivergenceMeter.Mark(1)
		return nil, common.Hash{}, true
	}
	// Byte-identical to the BP's witness: safe to serve/relay under the signed hash.
	return encoded, expected, true
}

// wit2SizeBandMultiplier bounds how many times the BP-signed witness size a
// received witness may reach before it is treated as oversized. Wide enough to
// absorb honest non-determinism (observed ±~9% node-set spread) with large
// margin, tight enough to reject gross bloat. Tune against the live inter-node
// size-spread distribution before hardening.
const wit2SizeBandMultiplier = 3

// bytesPerMiB matches the unit PageSize is expressed in (page size is 15 MiB).
const bytesPerMiB = 1024 * 1024

// acceptableWitnessSizeCeiling returns the maximum encoded witness byte size
// accepted for a block whose BP-signed witness size is signedSize. It is
// min(wit2SizeBandMultiplier*signedSize, absolute), where the absolute cap is
// the pre-existing gas-derived page ceiling expressed in bytes — retained so the
// accepted size stays bounded even when the signed size is implausibly large,
// and so a witness with no useful signed size still has a hard bound.
func (m *witnessManager) acceptableWitnessSizeCeiling(signedSize uint64) uint64 {
	band := signedSize * wit2SizeBandMultiplier
	absBytes := m.calculatePageThreshold() * maxPageSizeMB * bytesPerMiB
	if absBytes == 0 {
		return band
	}
	return min(band, absBytes)
}

// witnessSizeExceedsCeiling reports whether an encoded witness of actualSize
// bytes is oversized relative to ceiling. A witness exactly at the ceiling is
// accepted; only a strictly larger one is rejected.
func witnessSizeExceedsCeiling(actualSize, ceiling uint64) bool {
	return actualSize > ceiling
}

// signedHashMismatchQuarantineThreshold is how many DISTINCT servers must serve
// bytes that fail the BP-signed-hash check for a block before we stop trusting
// that signed hash and fall back to WIT1. One bad server cannot trigger it; a
// hash that distinct honest servers all disagree with is itself the suspect.
const signedHashMismatchQuarantineThreshold = 2

// wit2QuarantineStateTTL bounds how long a wit2MismatchPeers/wit2Quarantined
// entry can survive without being touched again, so a mismatch that arrives
// after the block's own pending-removal exits already ran (and therefore will
// never call clearSignedHashMismatch again) still gets cleaned up eventually
// instead of leaking for the process lifetime. Generous relative to the
// seconds-scale window a block normally resolves in, so it never interferes
// with legitimate in-flight quarantine tracking.
const wit2QuarantineStateTTL = 2 * time.Minute

// isSignedHashQuarantined reports whether the signed hash for a block has been
// quarantined (distinct servers repeatedly mismatched it).
func (m *witnessManager) isSignedHashQuarantined(hash common.Hash) bool {
	m.wit2QuarantineMu.Lock()
	defer m.wit2QuarantineMu.Unlock()
	_, ok := m.wit2Quarantined[hash]
	return ok
}

// recordSignedHashMismatch records that peer served bytes mismatching the
// signed hash for block hash. quarantined is true the moment the distinct-server
// threshold is reached and the hash is newly quarantined (so the caller logs the
// downgrade exactly once). firstForPeer is true only when this is peer's first
// recorded mismatch for this block, so the caller can strike a peer at most once
// per (peer, block) rather than once per retry — the retry loop re-hits the same
// sole announce-known peer every ~gatherSlack, and an honest server serving
// canonical bytes against a bad/stale BP hash would otherwise be jailed in ~1s.
// An empty peer string is ignored.
func (m *witnessManager) recordSignedHashMismatch(hash common.Hash, peer string) (quarantined bool, firstForPeer bool) {
	if peer == "" {
		return false, false
	}
	m.wit2QuarantineMu.Lock()
	defer m.wit2QuarantineMu.Unlock()
	m.wit2StateExpiry[hash] = time.Now().Add(wit2QuarantineStateTTL)
	if _, done := m.wit2Quarantined[hash]; done {
		return false, false
	}
	peers := m.wit2MismatchPeers[hash]
	if peers == nil {
		peers = make(map[string]struct{})
		m.wit2MismatchPeers[hash] = peers
	}
	_, seen := peers[peer]
	firstForPeer = !seen
	peers[peer] = struct{}{}
	if len(peers) >= signedHashMismatchQuarantineThreshold {
		m.wit2Quarantined[hash] = struct{}{}
		delete(m.wit2MismatchPeers, hash)
		return true, firstForPeer
	}
	return false, firstForPeer
}

// clearSignedHashMismatch drops all mismatch/quarantine state for a block. Call
// it once the block's witness is resolved or the request is abandoned so the
// maps stay bounded by in-flight fetches.
func (m *witnessManager) clearSignedHashMismatch(hash common.Hash) {
	m.wit2QuarantineMu.Lock()
	defer m.wit2QuarantineMu.Unlock()
	delete(m.wit2MismatchPeers, hash)
	delete(m.wit2Quarantined, hash)
	delete(m.wit2StateExpiry, hash)
}

// cleanupWit2QuarantineState removes wit2MismatchPeers/wit2Quarantined entries
// whose TTL has lapsed. Backstops the 4 pending-removal exits for the case a
// mismatch response arrives after they already ran for a hash (so none of
// them will run again for it): without this sweep such an entry would leak
// for the process lifetime. Called from the same ticker that expires
// witnessUnavailable.
func (m *witnessManager) cleanupWit2QuarantineState() {
	now := time.Now()
	cleaned := 0
	m.wit2QuarantineMu.Lock()
	for hash, expiry := range m.wit2StateExpiry {
		if now.After(expiry) {
			delete(m.wit2StateExpiry, hash)
			delete(m.wit2MismatchPeers, hash)
			delete(m.wit2Quarantined, hash)
			cleaned++
		}
	}
	m.wit2QuarantineMu.Unlock()
	if cleaned > 0 {
		log.Debug("[wm] Cleaned up expired wit2 quarantine state", "removed", cleaned)
	}
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
