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
