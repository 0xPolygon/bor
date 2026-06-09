package eth

import (
	"bytes"
	"crypto/rand"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSignedWitnessCachePutIfNewerSuppressesDuplicates verifies that the
// per-(blockHash) relay-window dedup blocks immediate re-relay of the same
// announcement. Without this, A→B→A bouncing would amplify a single signed
// announcement into a gossip storm.
func TestSignedWitnessCachePutIfNewerSuppressesDuplicates(t *testing.T) {
	c := newSignedWitnessCache()
	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xaaaa"),
		BlockNumber: 100,
		WitnessHash: common.HexToHash("0xbbbb"),
		Signature:   make([]byte, wit.SignatureLength),
	}
	if !c.putIfNewer(ann) {
		t.Fatal("first put should succeed")
	}
	if c.putIfNewer(ann) {
		t.Fatal("immediate re-put within window should be suppressed")
	}
	if _, ok := c.get(ann.BlockHash); !ok {
		t.Fatal("entry should still be present after suppressed put")
	}
}

// TestSignedWitnessCacheTTLExpiry checks that stale entries don't linger past
// the TTL. This prevents stale signatures from being re-served indefinitely
// for blocks long since imported and pruned.
func TestSignedWitnessCacheTTLExpiry(t *testing.T) {
	c := newSignedWitnessCache()
	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xcafe"),
		BlockNumber: 1,
		WitnessHash: common.HexToHash("0xdead"),
		Signature:   make([]byte, wit.SignatureLength),
	}
	c.putIfNewer(ann)
	// Force the receivedAt back beyond TTL.
	c.mu.Lock()
	c.entries[ann.BlockHash].receivedAt = time.Now().Add(-2 * wit2AnnounceTTL)
	c.mu.Unlock()
	if _, ok := c.get(ann.BlockHash); ok {
		t.Fatal("expired entry should not be returned")
	}
}

// TestVerifySignedAnnouncementRejectsBadLength catches sloppy callers passing
// truncated signatures. Without this guard, ecrecover panics or silently
// recovers a garbage address.
func TestVerifySignedAnnouncementRejectsBadLength(t *testing.T) {
	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0x01"),
		BlockNumber: 1,
		WitnessHash: common.HexToHash("0x02"),
		Signature:   []byte{0x00, 0x01, 0x02},
	}
	if _, err := verifySignedAnnouncement(ann); err == nil {
		t.Fatal("expected error for short signature")
	}
}

// TestVerifySignedAnnouncementRoundTrip signs an announcement with a known
// key and verifies recovery yields the same address. This is the core
// authentication property; if it breaks, every signed announcement on the
// network silently fails verification.
func TestVerifySignedAnnouncementRoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	expectedSigner := crypto.PubkeyToAddress(key.PublicKey)

	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xfeedface"),
		BlockNumber: 42,
		WitnessHash: common.HexToHash("0xc0ffee00"),
	}
	digest := wit.WitnessAnnouncementSigningHash(ann.BlockHash, ann.BlockNumber, ann.WitnessHash)
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ann.Signature = sig

	got, err := verifySignedAnnouncement(ann)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != expectedSigner {
		t.Fatalf("recovered signer = %s, want %s", got.Hex(), expectedSigner.Hex())
	}
}

// TestVerifySignedAnnouncementWalletSemantics mirrors what wallet.SignData
// does in production (keccak256(preimage) before signing) to guard against
// the regression where the producer pre-hashes a 32-byte digest and the
// wallet hashes again — producing signatures the verifier cannot recover.
// The test fails iff the producer/verifier preimage-vs-digest contract
// drifts.
func TestVerifySignedAnnouncementWalletSemantics(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	expectedSigner := crypto.PubkeyToAddress(key.PublicKey)

	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xab"),
		BlockNumber: 99,
		WitnessHash: common.HexToHash("0xcd"),
	}
	// Production wallet path: SignData hashes its input once, then signs.
	preimage := wit.WitnessAnnouncementSigningPreImage(ann.BlockHash, ann.BlockNumber, ann.WitnessHash)
	walletDigest := crypto.Keccak256(preimage)
	sig, err := crypto.Sign(walletDigest, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ann.Signature = sig

	got, err := verifySignedAnnouncement(ann)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != expectedSigner {
		t.Fatalf("recovered signer = %s, want %s — preimage/digest contract is broken", got.Hex(), expectedSigner.Hex())
	}
}

// TestVerifySignedAnnouncementDetectsTampering ensures that flipping any
// field in the announcement causes verification to recover a different
// address (or fail outright). This is the load-bearing property for the
// blame-separation argument: a signature ties a specific BP to a specific
// (BlockHash, BlockNumber, WitnessHash) tuple and nothing else.
func TestVerifySignedAnnouncementDetectsTampering(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)

	original := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xa1"),
		BlockNumber: 7,
		WitnessHash: common.HexToHash("0xb2"),
	}
	digest := wit.WitnessAnnouncementSigningHash(original.BlockHash, original.BlockNumber, original.WitnessHash)
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper with WitnessHash but reuse the signature.
	tampered := original
	tampered.WitnessHash = common.HexToHash("0xb3")
	tampered.Signature = sig

	got, err := verifySignedAnnouncement(tampered)
	if err != nil {
		// If err is non-nil, tampering was caught at the structural level.
		return
	}
	if got == signer {
		t.Fatal("tampered announcement recovered original signer; signature is not bound to the message")
	}
}

// TestPeerWit2TrackerRateLimitConsumesTokens guards Fix-7: the per-peer
// rate-limit must reject burst-exceeding traffic without dropping the peer.
// Honest peers running normal block cadence should never trip this; the test
// pins the budget so a regression that loosens the cap is caught.
func TestPeerWit2TrackerRateLimitConsumesTokens(t *testing.T) {
	tr := newPeerWit2Tracker()
	if !tr.allow("p1", wit2AnnounceBurstCap) {
		t.Fatal("first burst-cap-sized batch must fit")
	}
	if tr.allow("p1", 1) {
		t.Fatal("immediate next announcement must be rejected when bucket is empty")
	}
}

// TestPeerWit2TrackerStrikeDisconnectThreshold pins the strike-threshold
// behavior. Below the threshold, strike returns false (peer kept). At the
// threshold it returns true so the handler disconnects. Honest peers
// occasionally producing one bad announce should never trigger; sustained
// misbehavior must.
func TestPeerWit2TrackerStrikeDisconnectThreshold(t *testing.T) {
	tr := newPeerWit2Tracker()
	for i := 0; i < wit2MisbehaviorStrikeLimit-1; i++ {
		if tr.strike("p1") {
			t.Fatalf("disconnect signaled at strike %d, want only at %d", i+1, wit2MisbehaviorStrikeLimit)
		}
	}
	if !tr.strike("p1") {
		t.Fatalf("disconnect must signal at strike %d", wit2MisbehaviorStrikeLimit)
	}
}

// TestSignedWitnessCacheRejectsConflictingWitnessHash is the Fix-6 invariant
// at the cache layer: only the FIRST valid signed announcement for a given
// blockHash wins. A second announcement with a different WitnessHash —
// possibly from a forked producer or a compromised key in a later window —
// must be rejected, otherwise it would poison the cache mid-fetch and drop
// honest peers serving the original bytes.
func TestSignedWitnessCacheRejectsConflictingWitnessHash(t *testing.T) {
	c := newSignedWitnessCache()
	first := wit.SignedWitnessAnnouncement{
		BlockHash:   common.HexToHash("0xabcd"),
		BlockNumber: 50,
		WitnessHash: common.HexToHash("0x1111"),
		Signature:   make([]byte, wit.SignatureLength),
	}
	if !c.putIfNewer(first) {
		t.Fatal("first put should succeed")
	}

	conflict := first
	conflict.WitnessHash = common.HexToHash("0x2222")
	if c.putIfNewer(conflict) {
		t.Fatal("second put with different WitnessHash must be rejected")
	}
	got, ok := c.get(first.BlockHash)
	if !ok {
		t.Fatal("first announcement must remain cached after conflict rejection")
	}
	if got.WitnessHash != first.WitnessHash {
		t.Fatalf("cache poisoned: WitnessHash=%s want=%s", got.WitnessHash.Hex(), first.WitnessHash.Hex())
	}
}

// TestPendingWitnessBodyCacheEvictsOldest covers the LRU-style eviction when
// the cache reaches capacity. Without it, long-running nodes accumulate
// witness bodies indefinitely (~50MB each) and run out of memory.
func TestPendingWitnessBodyCacheEvictsOldest(t *testing.T) {
	c := newPendingWitnessBodyCache(2)
	c.put(common.HexToHash("0x01"), []byte("first"), common.HexToHash("0xa"))
	time.Sleep(time.Millisecond)
	c.put(common.HexToHash("0x02"), []byte("second"), common.HexToHash("0xb"))
	time.Sleep(time.Millisecond)
	c.put(common.HexToHash("0x03"), []byte("third"), common.HexToHash("0xc"))

	if _, _, ok := c.get(common.HexToHash("0x01")); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, _, ok := c.get(common.HexToHash("0x02")); !ok {
		t.Fatal("middle entry should still be present")
	}
	if _, _, ok := c.get(common.HexToHash("0x03")); !ok {
		t.Fatal("newest entry should still be present")
	}
}

// TestPendingWitnessBodyCacheDropClearsEntry guards the explicit drop path
// used when a witness has been written to chain storage and no longer needs
// in-flight serving.
func TestPendingWitnessBodyCacheDropClearsEntry(t *testing.T) {
	c := newPendingWitnessBodyCache(4)
	hash := common.HexToHash("0xdead")
	c.put(hash, []byte("x"), common.HexToHash("0xaa"))
	c.drop(hash)
	if _, _, ok := c.get(hash); ok {
		t.Fatal("entry should be gone after drop")
	}
}

// TestHandleWitnessBroadcastSkipsCacheWhenNoSignature guards the Fix-5
// invariant: bytes received via NewWitness broadcast are NOT exposed for
// pre-import serving when no BP-signed witnessHash is on file. Otherwise an
// honest relayer with a malicious upstream would serve unverified bytes and
// be dropped by downstream peers as if it had lied.
func TestHandleWitnessBroadcastSkipsCacheWhenNoSignature(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(7777)}
	witness, err := stateless.NewWitness(header, nil)
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}

	// No signed announcement on file → broadcast must NOT populate the
	// pre-import serving cache.
	if err := witH.handleWitnessBroadcast(peer, witness); err != nil {
		t.Fatalf("handleWitnessBroadcast: %v", err)
	}
	hash := header.Hash()
	if _, _, ok := h.handler.pendingWitnessBodies.get(hash); ok {
		t.Fatal("pendingWitnessBodies populated without a signed witnessHash; bytes are unverified for serving")
	}
}

// TestDeferredAnnounceCachePerPeerCap is the regression for W-1: a single peer
// must not be able to monopolise the deferred-announce queue and evict honest
// header-racing announces. The cache is keyed by blockHash, so bounding the
// claimed BlockNumber is no defence (an attacker just reuses a near-tip number
// with distinct fake hashes). The effective bound is per-peer: one peer may
// hold at most capacity/divisor slots; honest peers keep theirs.
func TestDeferredAnnounceCachePerPeerCap(t *testing.T) {
	// capacity 16 → perPeerCap = 16/8 = 2, small enough to exercise cheaply.
	c := newDeferredAnnounceCache(16)
	require.Equal(t, 2, c.perPeerCap)

	mkAnn := func(n byte) wit.SignedWitnessAnnouncement {
		return wit.SignedWitnessAnnouncement{
			BlockHash:   common.Hash{n},
			BlockNumber: uint64(n),
			WitnessHash: common.Hash{0xff, n},
			Signature:   make([]byte, wit.SignatureLength),
		}
	}

	// One peer fills its share, then its next NEW-hash put is dropped.
	c.put(mkAnn(1), "attacker")
	c.put(mkAnn(2), "attacker")
	c.put(mkAnn(3), "attacker")
	assert.True(t, c.has(common.Hash{1}))
	assert.True(t, c.has(common.Hash{2}))
	assert.False(t, c.has(common.Hash{3}),
		"third new-hash deferral from a saturating peer must be dropped by the per-peer cap")

	// An honest peer is unaffected by the attacker's saturation.
	c.put(mkAnn(10), "honest")
	assert.True(t, c.has(common.Hash{10}),
		"honest peer must not be starved by a peer that filled its own share")

	// Draining one of the peer's entries returns a credit so it can defer again
	// — the cap tracks *live* entries, it is not a lifetime quota.
	if _, ok := c.take(common.Hash{1}); !ok {
		t.Fatal("take should return the live entry")
	}
	c.put(mkAnn(3), "attacker")
	assert.True(t, c.has(common.Hash{3}),
		"after a drain freed a slot, the peer may defer a new hash again")

	// Re-deferring an existing hash (same peer) is an overwrite, not a new
	// slot, so it must never be rejected by the cap even at the limit.
	c.put(mkAnn(2), "attacker") // attacker currently holds {2},{3} == cap
	c.put(mkAnn(2), "attacker") // overwrite, must succeed
	assert.True(t, c.has(common.Hash{2}))
}

// TestSignedAnnounceDoesNotMarkPeerAsBodyHolder is the load-bearing
// regression test for the announce/body separation. A WIT2 peer that has
// only relayed a signed announcement (no body) MUST NOT show up in
// peersWithoutWitness's complement — i.e. it must not be selected as a body
// fetch target by getOnePeerWithWitness. Otherwise the fetcher will ask a
// relay-only peer for bytes, get nothing, and drop an honest peer.
func TestSignedAnnounceDoesNotMarkPeerAsBodyHolder(t *testing.T) {
	hash := common.HexToHash("0xfa11")
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: 1,
		WitnessHash: common.HexToHash("0xab"),
		Signature:   make([]byte, wit.SignatureLength),
	}

	// Outbound announce path (this node forwarding to peer): must NOT mark
	// peer as a body-holder.
	peer.AsyncSendSignedWitnessAnnouncement(ann)

	if peer.KnownWitnessContainsHash(hash) {
		t.Fatal("AsyncSendSignedWitnessAnnouncement marked peer as body-holder; body fetch will pick a relay-only peer and drop it")
	}
	if !peer.KnownAnnounceContainsHash(hash) {
		t.Fatal("AsyncSendSignedWitnessAnnouncement should mark announce-known so we don't re-relay")
	}
}

// TestEmptyGetWitnessForSignedHashPushesBodyOnArrival pins the serving-side
// cure for the WIT2 stateless regression. In an all-WIT2 fleet no peer is ever
// marked as a body-holder (the full-body broadcast is never sent and the WIT1
// hash-announce that would mark it is not used between WIT2 peers), so a
// stateless consumer always fetches from an announce-only relayer that does not
// yet hold the body. The relayer answers GetWitness empty and the consumer is
// left polling. WIT1 stays in lockstep precisely because its hash-announce both
// implies the sender holds the body and marks it as a holder, so the first pull
// lands.
//
// The fix records the asking peer as "waiting" when we answer empty for a hash
// we hold a BP-signed announcement for (so we know the witness exists), and
// pushes the full body to those waiters the moment we obtain it — restoring the
// WIT1-style hand-off without flooding (only peers that actually asked, and at
// most one body each, exactly what a pull would have cost).
func TestEmptyGetWitnessForSignedHashPushesBodyOnArrival(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(7777)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	witness, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, witness.EncodeRLP(&buf))
	bodyBytes := buf.Bytes()
	witnessHash := stateless.WitnessCommitHash(bodyBytes)

	// We hold a BP-signed announcement for this hash (the witness provably
	// exists) but not the body yet — neither in-flight cache nor chain storage.
	h.handler.signedWitnesses.putIfNewer(wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: witnessHash,
		Signature:   make([]byte, wit.SignatureLength),
	})

	// Peer asks for the body before we have it → empty response. This must
	// register the peer as waiting for the body.
	resp, err := witH.handleGetWitness(peer, &wit.GetWitnessPacket{
		RequestId:         1,
		GetWitnessRequest: &wit.GetWitnessRequest{WitnessPages: []wit.WitnessPageRequest{{Hash: hash, Page: 0}}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp))
	require.Equal(t, uint64(0), resp[0].TotalPages, "precondition: body absent, must serve empty")
	require.False(t, peer.KnownWitnessContainsHash(hash), "peer must not yet be treated as a body-holder")

	// Body arrives (our own paged fetch verified it, or a broadcast delivered
	// it). Populating the serving cache must push the full body to the waiting
	// peer so it imports immediately rather than re-polling us with empty
	// GetWitness — which is the stateless lag we measured on devnet.
	h.handler.cacheVerifiedWitnessForServing(hash, bodyBytes, witnessHash)

	require.True(t, peer.KnownWitnessContainsHash(hash),
		"waiting peer was not pushed the witness body on arrival; stateless consumer keeps polling (the regression)")
}

// TestFlushWitnessWaitersForImportedPushesFromChainStorage covers the dominant
// production path the fetch/broadcast push hooks miss: a full / producing node
// obtains a witness by generating it during native block import (it lands in
// chain storage, not the in-flight cache, and arrives via no gossip broadcast).
// The chain-head flush must still deliver it to a peer that asked before the
// node held it — this is what was missing in the first fix attempt, where
// stateless peers of a producing node (e.g. S1↔BP1) saw no lag improvement
// because BP1 never triggered a push.
func TestFlushWitnessWaitersForImportedPushesFromChainStorage(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(8888)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	witness, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, witness.EncodeRLP(&buf))
	bodyBytes := buf.Bytes()
	witnessHash := stateless.WitnessCommitHash(bodyBytes)

	h.handler.signedWitnesses.putIfNewer(wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: witnessHash,
		Signature:   make([]byte, wit.SignatureLength),
	})

	// Peer asks before we hold the body → empty, registers as waiter.
	_, err = witH.handleGetWitness(peer, &wit.GetWitnessPacket{
		RequestId:         1,
		GetWitnessRequest: &wit.GetWitnessRequest{WitnessPages: []wit.WitnessPageRequest{{Hash: hash, Page: 0}}},
	})
	require.NoError(t, err)
	require.False(t, peer.KnownWitnessContainsHash(hash))

	// Native import: witness lands in chain storage only. The chain-head flush
	// must push it to the waiting peer.
	rawdb.WriteWitness(h.chain.DB(), hash, bodyBytes)
	h.handler.flushWitnessWaitersForImported(hash)

	require.True(t, peer.KnownWitnessContainsHash(hash),
		"chain-head flush did not push a natively-imported witness to the waiting peer")
}

// TestHandleGetWitnessServesFromInFlightCache is the load-bearing behavioral
// test for the WIT2 pre-import serving claim: a node that has received the
// witness body over gossip but has not yet imported it (chain storage empty)
// must still be able to serve `GetWitness` requests from the in-flight cache.
// Without this path, multi-hop WIT2 fast-propagation has no body source until
// each hop's chain-write completes — collapsing the entire benefit of the
// design.
func TestHandleGetWitnessServesFromInFlightCache(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWitPeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(4242)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	// Smaller than PageSize so the response fits in a single page.
	bodyBytes := make([]byte, 1*1024*1024)
	rand.Read(bodyBytes)

	// Body is in the in-flight cache only; chain storage is empty.
	h.handler.pendingWitnessBodies.put(hash, bodyBytes, crypto.Keccak256Hash(bodyBytes))
	require.Nil(t, rawdb.ReadWitnessSize(h.chain.DB(), hash),
		"precondition: chain must have no witness for this hash")

	resp, err := witH.handleGetWitness(peer, &wit.GetWitnessPacket{
		RequestId:         1,
		GetWitnessRequest: &wit.GetWitnessRequest{WitnessPages: []wit.WitnessPageRequest{{Hash: hash, Page: 0}}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp))
	assert.Equal(t, hash, resp[0].Hash)
	assert.Equal(t, uint64(1), resp[0].TotalPages)
	require.Equal(t, len(bodyBytes), len(resp[0].Data),
		"in-flight cache served fewer bytes than expected — pre-import path is not wired")
	assert.Equal(t, bodyBytes[:64], resp[0].Data[:64])
}

// TestHandleGetWitnessMetadataServesFromInFlightCache mirrors the above for
// the metadata path: a peer asking for metadata before chain-write should
// receive Available=true with the correct size from the in-flight cache.
// This is what lets a downstream relayer compute pagination without waiting.
func TestHandleGetWitnessMetadataServesFromInFlightCache(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWitPeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(4243)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	bodyBytes := make([]byte, 7*1024*1024) // forces TotalPages = 1 (under 15MB)
	rand.Read(bodyBytes)
	h.handler.pendingWitnessBodies.put(hash, bodyBytes, crypto.Keccak256Hash(bodyBytes))
	require.Nil(t, rawdb.ReadWitnessSize(h.chain.DB(), hash))

	resp, err := witH.handleGetWitnessMetadata(peer, &wit.GetWitnessMetadataPacket{
		RequestId: 1,
		GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
			Hashes: []common.Hash{hash},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp))
	assert.True(t, resp[0].Available, "metadata must report Available when only the in-flight cache holds the body")
	assert.Equal(t, uint64(len(bodyBytes)), resp[0].WitnessSize)
	assert.Equal(t, uint64(1), resp[0].TotalPages)
	assert.Equal(t, header.Number.Uint64(), resp[0].BlockNumber)
}

// TestHandleGetWitnessPrefersCacheOverChain documents the chosen precedence:
// when both sources hold a witness, the in-flight cache wins. Locks the choice
// in so a refactor can't silently reverse it. Cache-first is correct because
// the cache is what the BP-signed announcement points at; the chain copy is
// only valid once chain-write has finished, which the cache entry implies has
// not yet happened or has just happened with identical bytes.
func TestHandleGetWitnessPrefersCacheOverChain(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWitPeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(4244)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	cacheBytes := make([]byte, 4*1024*1024)
	rand.Read(cacheBytes)
	chainBytes := make([]byte, 4*1024*1024)
	rand.Read(chainBytes)

	rawdb.WriteWitness(h.chain.DB(), hash, chainBytes)
	h.handler.pendingWitnessBodies.put(hash, cacheBytes, crypto.Keccak256Hash(cacheBytes))

	resp, err := witH.handleGetWitness(peer, &wit.GetWitnessPacket{
		RequestId:         1,
		GetWitnessRequest: &wit.GetWitnessRequest{WitnessPages: []wit.WitnessPageRequest{{Hash: hash, Page: 0}}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp))
	assert.Equal(t, cacheBytes[:64], resp[0].Data[:64],
		"handler must prefer the in-flight cache; got bytes that look like chain storage")
}

// TestCanonicalWitnessHashUsesStoredBytesDirectly is the regression for the
// optimization that skips decode/re-encode on the producer announce path: as
// long as Witness.EncodeRLP is canonical-deterministic, stored bytes are
// already canonical and can be hashed in place. If a future change re-
// introduces a non-canonical write path, this test fails and the producer-
// side WitnessHash silently diverges from what verifiers compute.
func TestCanonicalWitnessHashUsesStoredBytesDirectly(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	header := &types.Header{Number: big.NewInt(7777)}
	hash := header.Hash()

	// Build a synthetic witness, encode canonically once, store the bytes.
	w, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)
	for i := 0; i < 64; i++ {
		buf := make([]byte, 256)
		rand.Read(buf)
		w.AddState(map[string][]byte{string(buf): buf})
	}
	canonical := encodeWitnessForTest(t, w)
	rawdb.WriteWitness(h.chain.DB(), hash, canonical)

	got, ok := h.handler.canonicalWitnessHash(hash)
	require.True(t, ok)

	want := stateless.WitnessCommitHash(canonical)
	require.Equal(t, want, got,
		"canonicalWitnessHash must hash stored canonical bytes directly; if this fails, EncodeRLP determinism has regressed or the helper added back a re-encode")
}

func encodeWitnessForTest(t *testing.T, w *stateless.Witness) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, w.EncodeRLP(&buf))
	return buf.Bytes()
}

// TestVerifyScheduledProducerDeferredWhenHeaderUnknown is the regression for
// the cosend race: when the signed announce arrives before the block is
// imported, verifyScheduledProducer must report headerAvailable=false so the
// caller defers (no relay, no strike). Without this branch, valid WIT2
// announces would draw strikes for honest relayers during normal operation.
func TestVerifyScheduledProducerDeferredWhenHeaderUnknown(t *testing.T) {
	// borEngine is unused on the nil-header branch — verifyScheduledProducer
	// short-circuits before calling Author. Pass nil to keep the test free of
	// engine setup; if a future change reorders the branch and starts deref-
	// erencing borEngine here, the test will panic and we'll catch it.
	ok, headerAvailable := verifyScheduledProducer(nil, nil, common.Address{}, 100, common.HexToHash("0xfeed"))
	if ok {
		t.Fatal("nil header must not validate as ok")
	}
	if headerAvailable {
		t.Fatal("nil header must report headerAvailable=false so caller defers without striking")
	}
}

// TestHandleSignedWitnessAnnouncementsBadSigDoesNotMarkAnnounceKnown is the
// regression for the verification-ordering bug: handleSignedWitnessAnnouncements
// must not mark a peer as announce-known until the announcement has passed the
// signature/producer-binding gate. The previous order called
// peer.AddKnownAnnounce(hash) unconditionally before acceptSignedAnnouncement,
// so a peer relaying a structurally invalid announcement still became
// announce-known for that hash. Two bad consequences flowed from that:
//   - this node refused to ever relay a *valid* later announcement back to that
//     peer for the same hash, leaving them unable to recover;
//   - this node short-circuited its own re-evaluation paths when a good
//     announcement for the same hash arrived from another peer, because the
//     original sender's announce-known bit served as a relay-suppression hint.
//
// Using a structurally invalid signature (length 3) is sufficient to drive the
// reject path through verifySignedAnnouncement → strikeWit2Peer without needing
// a bor engine or block header.
func TestHandleSignedWitnessAnnouncementsBadSigDoesNotMarkAnnounceKnown(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	blockHash := common.HexToHash("0xfeedface")
	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   blockHash,
		BlockNumber: 1,
		WitnessHash: common.HexToHash("0xc0ffee"),
		Signature:   []byte{0x00, 0x01, 0x02}, // structurally invalid
	}

	if err := witH.handleSignedWitnessAnnouncements(peer, []wit.SignedWitnessAnnouncement{ann}); err != nil {
		t.Fatalf("handleSignedWitnessAnnouncements: %v", err)
	}

	if peer.KnownAnnounceContainsHash(blockHash) {
		t.Fatal("peer marked announce-known despite invalid signature; verification ordering is broken")
	}
	if _, ok := h.handler.signedWitnesses.get(blockHash); ok {
		t.Fatal("signed announcement cached despite invalid signature")
	}
}

// TestPendingWitnessBodyCacheGetEvictsExpired pins the leak fix for the TTL
// path. Before the fix, get() returned false on expiry but left the entry in
// the map; gcLocked only ran from put(), so a node that stopped receiving new
// witnesses retained up to capacity (10) full witness blobs (~50 MiB each)
// indefinitely, producing a long-lived OOM risk under bursty traffic.
//
// The contract this test enforces: any get() that observes an expired entry
// MUST delete it in place so memory pressure does not persist past the TTL.
func TestPendingWitnessBodyCacheGetEvictsExpired(t *testing.T) {
	c := newPendingWitnessBodyCache(4)
	hash := common.HexToHash("0xfade")
	c.put(hash, []byte("expensive-body"), common.HexToHash("0xab"))

	// Force the entry's receivedAt back beyond the TTL, mirroring the same
	// approach used by TestSignedWitnessCacheTTLExpiry above.
	c.mu.Lock()
	c.entries[hash].receivedAt = time.Now().Add(-2 * wit2AnnounceTTL)
	c.mu.Unlock()

	if _, _, ok := c.get(hash); ok {
		t.Fatal("expired entry must not be returned")
	}

	c.mu.RLock()
	entriesAfter := len(c.entries)
	c.mu.RUnlock()
	if entriesAfter != 0 {
		t.Fatalf("expired entry must be deleted on get; len(entries)=%d, want 0", entriesAfter)
	}
}

// TestDeferredSignedAnnounceDrainedAfterHeaderArrives is the regression for
// the cosend-race liveness gap: when a signed announcement arrives before the
// corresponding block header (block + announce travel independently and can
// race in either order), the handler MUST retain the announcement and re-
// evaluate it once the header arrives, rather than dropping it on the floor
// and silently degrading subsequent witness fetches to the unsigned WIT1
// fallback path.
//
// Without this:
//  1. announce arrives → header-unknown → acceptSignedAnnouncement returns
//     false, announcement is forgotten.
//  2. block arrives shortly after, but no second announce reaches us (sparse
//     mesh, single-cosend window) → signedWitnesses never holds the hash.
//  3. fetcher selects a peer, gets bytes, parentSignedWitnessHash returns
//     false → byte-verification skipped, WIT2 trust model silently leaks.
//
// The deferred queue holds the announcement until the chain catches up; the
// drain (here invoked directly; in production fired from the chainHeadCh
// subscription) re-runs verification and caches the hash on success.
func TestDeferredSignedAnnounceDrainedAfterHeaderArrives(t *testing.T) {
	h := newTestHandler()
	defer h.close()
	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	header := &types.Header{Number: big.NewInt(99_999)} // NOT in chain
	blockHash := header.Hash()

	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   blockHash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: common.HexToHash("0xc0ffee01"),
	}
	digest := wit.WitnessAnnouncementSigningHash(ann.BlockHash, ann.BlockNumber, ann.WitnessHash)
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ann.Signature = sig

	// Phase 1: header is not yet local. The announce must be deferred — not
	// cached, not relayed, not credited to the sender as announce-known.
	if err := witH.handleSignedWitnessAnnouncements(peer, []wit.SignedWitnessAnnouncement{ann}); err != nil {
		t.Fatalf("handleSignedWitnessAnnouncements: %v", err)
	}
	if _, ok := h.handler.signedWitnesses.get(blockHash); ok {
		t.Fatal("announce cached prematurely; verification should defer when header is unknown")
	}
	if peer.KnownAnnounceContainsHash(blockHash) {
		t.Fatal("peer marked announce-known on deferred path; re-relay recovery is suppressed")
	}
	if !h.handler.deferredAnnounces.has(blockHash) {
		t.Fatal("deferred-announce queue did not retain the announce; the race window is uncovered")
	}

	// Phase 2: header arrives. Drain the queue (production wires this from
	// the chainHeadCh subscription on each new block).
	rawdb.WriteHeader(h.chain.DB(), header)
	h.handler.drainDeferredAnnouncesFor(blockHash)

	if _, ok := h.handler.signedWitnesses.get(blockHash); !ok {
		t.Fatal("announce not cached after header arrival; drain is broken")
	}
	if h.handler.deferredAnnounces.has(blockHash) {
		t.Fatal("deferred entry should be cleared after successful drain")
	}
}

// TestVerifyScheduledProducerRejectsBlockNumberMismatch covers the case where
// the local header is present but disagrees with the announce on block
// number. This is a confirmed bad announce and the caller must strike, so
// headerAvailable must be true.
func TestVerifyScheduledProducerRejectsBlockNumberMismatch(t *testing.T) {
	header := &types.Header{Number: big.NewInt(50)}
	ok, headerAvailable := verifyScheduledProducer(nil, header, common.Address{}, 51, header.Hash())
	if ok {
		t.Fatal("number mismatch must not validate")
	}
	if !headerAvailable {
		t.Fatal("with header present, headerAvailable must be true so the caller strikes the relayer")
	}
}

// TestHandleWitnessBroadcastByteMismatchNotInjected guards the verification
// boundary of the broadcast path: when a BP-signed witnessHash is on file and
// a broadcast body does NOT match it, the witness must be fully rejected — not
// cached for serving, sender not marked as a body-holder, and not injected
// into the fetcher. Anything less makes the full-body broadcast a bypass of
// the byte verification the paged-fetch path enforces.
func TestHandleWitnessBroadcastByteMismatchNotInjected(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(7778)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	witness, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)

	// Signed announcement on file commits to a DIFFERENT witnessHash than the
	// broadcast bytes will hash to.
	h.handler.signedWitnesses.putIfNewer(wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: common.HexToHash("0xdeadbeef"),
		Signature:   make([]byte, wit.SignatureLength),
	})

	require.NoError(t, witH.handleWitnessBroadcast(peer, witness))

	if _, _, ok := h.handler.pendingWitnessBodies.get(hash); ok {
		t.Fatal("byte-mismatched broadcast populated the pre-import serving cache")
	}
	if peer.KnownWitnessContainsHash(hash) {
		t.Fatal("byte-mismatched broadcast marked the sender as a body-holder; fetcher would pull garbage from it")
	}
}

// TestHandleWitnessBroadcastDropsUnknownHeader restores the F-3 audit fix:
// with no BP-signed announcement on file (WIT1 fallback), an unsolicited
// witness broadcast is only accepted for a block header we actually know.
// Without the gate, a peer can make us RLP-decode and inject arbitrary 16MB
// bodies keyed by hashes of its own choosing.
func TestHandleWitnessBroadcastDropsUnknownHeader(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	// Unknown header, no signed announcement → dropped, sender not marked.
	unknown := &types.Header{Number: big.NewInt(424242)}
	unknownWitness, err := stateless.NewWitness(unknown, nil)
	require.NoError(t, err)
	require.NoError(t, witH.handleWitnessBroadcast(peer, unknownWitness))
	if peer.KnownWitnessContainsHash(unknown.Hash()) {
		t.Fatal("broadcast for unknown header was accepted; unsolicited bodies are cacheable on the sender's word")
	}

	// Same broadcast for a locally known header → accepted (WIT1 path).
	known := &types.Header{Number: big.NewInt(7779)}
	rawdb.WriteHeader(h.chain.DB(), known)
	knownWitness, err := stateless.NewWitness(known, nil)
	require.NoError(t, err)
	require.NoError(t, witH.handleWitnessBroadcast(peer, knownWitness))
	if !peer.KnownWitnessContainsHash(known.Hash()) {
		t.Fatal("broadcast for known header was not accepted; WIT1 fallback broken")
	}
}

// TestWaiterPushSkipsOversizedWitness bounds the waiter-push cure for the
// stateless regression: a witness whose canonical encoding exceeds the wit
// protocol message cap must NOT be full-pushed via NewWitness — the receiver
// would reject the message as too large and drop us as a protocol violator.
// Oversized witnesses stay on the paged pull path (we hold servable bytes by
// the time a push could fire, so the waiter's backed-off poll succeeds).
func TestWaiterPushSkipsOversizedWitness(t *testing.T) {
	h := newTestHandler()
	defer h.close()

	witH := (*witHandler)(h.handler)
	peer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	header := &types.Header{Number: big.NewInt(7780)}
	hash := header.Hash()
	rawdb.WriteHeader(h.chain.DB(), header)

	witness, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)
	FillWitnessWithDeterministicRandomState(witness, witnessPushMaxSize+1024*1024)
	var buf bytes.Buffer
	require.NoError(t, witness.EncodeRLP(&buf))
	bodyBytes := buf.Bytes()
	require.Greater(t, len(bodyBytes), witnessPushMaxSize, "fixture must exceed the push cap")
	witnessHash := stateless.WitnessCommitHash(bodyBytes)

	h.handler.signedWitnesses.putIfNewer(wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: witnessHash,
		Signature:   make([]byte, wit.SignatureLength),
	})

	// Register the peer as a waiter: it asks for the body before we hold it.
	resp, err := witH.handleGetWitness(peer, &wit.GetWitnessPacket{
		RequestId:         1,
		GetWitnessRequest: &wit.GetWitnessRequest{WitnessPages: []wit.WitnessPageRequest{{Hash: hash, Page: 0}}},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), resp[0].TotalPages, "precondition: body absent, must serve empty")

	// Body arrives. The push must be skipped — encoded size is over the wit
	// message cap — leaving the waiter on the paged pull path.
	h.handler.cacheVerifiedWitnessForServing(hash, bodyBytes, witnessHash)

	if peer.KnownWitnessContainsHash(hash) {
		t.Fatal("oversized witness was full-pushed via NewWitness; receiver would drop us as a protocol violator")
	}
}
