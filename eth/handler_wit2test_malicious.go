// THROWAWAY — devnet-only WIT2 attacker simulation. Never merged into the
// real PR branch, never pushed anywhere. Lives only on
// wip/devnet-instr-pr2208-v2 for local kurtosis validation of PR #2208's
// anti-abuse and signature-verification paths (scenarios #5, #6, #11, #16 in
// investigations/wit2-devnet-validation-2026-08-27.md).
//
// Activated only when WIT2_TEST_MALICIOUS_MODE is set on this node's
// container — every other node in the devnet runs the real, unmodified code
// path. Always signs with a throwaway key generated at process start; never
// touches this node's real validator key, so a misconfigured run can at
// worst make one devnet node emit garbage announcements, never sign a real
// block or leak real key material.
package eth

import (
	"crypto/ecdsa"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/wit2test"
)

// startMaliciousWit2Broadcaster is a no-op unless WIT2_TEST_MALICIOUS_MODE is
// set. See package doc comment above for the safety argument.
func (h *handler) startMaliciousWit2Broadcaster() {
	mode := wit2test.MaliciousMode()
	if mode == "" {
		return
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		log.Error("wit2test: malicious mode key generation failed; broadcaster NOT started", "err", err)
		return
	}
	log.Warn("wit2test: MALICIOUS WIT2 BROADCASTER ACTIVE — this node will emit forged announcements",
		"mode", mode, "forged_signer", crypto.PubkeyToAddress(key.PublicKey))

	switch mode {
	case "forge":
		// One forged announcement per new head: real block hash/number, a
		// fabricated witness hash (this node did not seal the block, so it
		// has no legitimate witnessHash to claim), signed by a key that is
		// never the block's producer. Exercises: receivers must reject
		// (producer != signer), must not relay it onward, and must strike
		// this peer. Low rate — isolates the single-bad-announcement path
		// from the rate limiter.
		go h.maliciousForgeLoop(key)
	case "flood":
		// High-rate forged announcements against whichever peers we're
		// connected to, to drive wit2MisbehaviorStrikeLimit (5 strikes /
		// wit2MisbehaviorWindow). Exercises the anti-abuse
		// disconnect+jail path, and — at sustained rate — the per-peer
		// rate limiter ahead of it.
		go h.maliciousFloodLoop(key)
	default:
		log.Error("wit2test: unknown WIT2_TEST_MALICIOUS_MODE, broadcaster NOT started", "mode", mode)
	}
}

func (h *handler) maliciousForgeLoop(key *ecdsa.PrivateKey) {
	headCh := make(chan core.ChainHeadEvent, 16)
	sub := h.chain.SubscribeChainHeadEvent(headCh)
	defer sub.Unsubscribe()

	for {
		select {
		case ev := <-headCh:
			if ev.Header == nil {
				continue
			}
			ann := forgeWit2Announcement(key, ev.Header.Hash(), ev.Header.Number.Uint64(), randomHash())
			h.relaySignedAnnouncement("", ann)
			log.Warn("wit2test: forged announcement sent", "block_hash", ann.BlockHash, "block_number", ann.BlockNumber)
		case <-sub.Err():
			return
		}
	}
}

func (h *handler) maliciousFloodLoop(key *ecdsa.PrivateKey) {
	ticker := time.NewTicker(50 * time.Millisecond) // ~20/s per node
	defer ticker.Stop()
	sent := 0
	for range ticker.C {
		head := h.chain.CurrentHeader()
		if head == nil {
			continue
		}
		ann := forgeWit2Announcement(key, head.Hash(), head.Number.Uint64(), randomHash())
		h.relaySignedAnnouncement("", ann)
		sent++
		if sent%100 == 0 {
			log.Warn("wit2test: flood progress", "sent", sent)
		}
	}
}

func forgeWit2Announcement(key *ecdsa.PrivateKey, blockHash common.Hash, blockNumber uint64, witnessHash common.Hash) wit.SignedWitnessAnnouncement {
	digest := wit.WitnessAnnouncementSigningHash(blockHash, blockNumber, witnessHash)
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		log.Error("wit2test: forged announcement signing failed", "err", err)
		return wit.SignedWitnessAnnouncement{}
	}
	if len(sig) == wit.SignatureLength && (sig[crypto.RecoveryIDOffset] == 27 || sig[crypto.RecoveryIDOffset] == 28) {
		sig[crypto.RecoveryIDOffset] -= 27
	}
	return wit.SignedWitnessAnnouncement{
		BlockHash:   blockHash,
		BlockNumber: blockNumber,
		WitnessHash: witnessHash,
		Signature:   sig,
	}
}

func randomHash() common.Hash {
	var h common.Hash
	rand.Read(h[:])
	return h
}
