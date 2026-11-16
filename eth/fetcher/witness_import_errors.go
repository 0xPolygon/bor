package fetcher

import (
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
)

// witnessAttributableMismatchErrors are the import-validation sentinels that
// mean execution against the delivered witness produced a result the header
// does not commit to. On a stateless node the witness is the only state input,
// so a mismatch is something the witness bytes could have caused.
var witnessAttributableMismatchErrors = []error{
	core.ErrStatelessStateRootMismatch,
	core.ErrGasUsedMismatch,
	core.ErrReceiptRootMismatch,
	core.ErrBloomMismatch,
	core.ErrRequestsHashMismatch,
	// The witness is not for this block or does not start from the parent's
	// state root (core/stateless.ValidateWitnessPreState). Checked before
	// execution, so a forged pre-state never reaches the mismatches above.
	stateless.ErrInvalidWitness,
}

// witnessAttributableMessageFragments cover the mismatch errors that are built
// with fmt.Errorf and have no sentinel to match on.
var witnessAttributableMessageFragments = []string{
	"invalid merkle root",                             // core.BlockValidator.ValidateState: post-state root
	"stateless self-validation root mismatch",         // core.ExecuteStateless cross-check, full-node path
	"stateless self-validation receipt root mismatch", // same, receipt root
}

// isWitnessAttributableImportError reports whether an insertChain failure is
// one the delivered witness could have caused, and so may be charged to the
// peer that served it. It is a positive allowlist: an incomplete witness
// (missing trie node), a witness that fails the pre-state check (wrong context
// header or a pre-state root that is not the parent's), or execution against
// the witness disagreeing with the header (state root, receipt root, gas used,
// bloom, requests hash). Every other
// failure — a contract bytecode missing from the local disk (fetchable and
// healed by the downloader; not something the witness carries), an interrupted
// or stopped chain, a whitelist/milestone mismatch, a header or DB error — is
// the local node's or the block's problem, and charging the server for it would
// strike honest peers on every such block. Unknown errors are not charged.
func isWitnessAttributableImportError(err error) bool {
	if err == nil {
		return false
	}
	// A contract bytecode missing from the local disk. Witnesses carry no code,
	// so the server could not have supplied it; the blob is content-addressed
	// and the downloader's self-heal fetches it. Checked first because it
	// arrives wrapped in core.ErrStatelessIncompleteState, the same sentinel
	// that wraps a missing trie node — the wrapped cause, not the sentinel,
	// decides attribution.
	var missingCode *state.MissingCodeError
	if errors.As(err, &missingCode) {
		return false
	}
	// The witness did not carry a trie node the block reads: an incomplete
	// witness is something the server did produce.
	var missingNode *trie.MissingNodeError
	if errors.As(err, &missingNode) {
		return true
	}
	for _, sentinel := range witnessAttributableMismatchErrors {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	msg := err.Error()
	for _, fragment := range witnessAttributableMessageFragments {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

// isWitnessFaultImportError reports whether an attributable insertChain failure
// is decided by the witness bytes alone, whatever the block: a witness that
// fails the pre-state check, or one missing a trie node the block reads. Only
// these strike the serving peer. An execution mismatch (state root, receipt
// root, gas used, bloom, requests hash) is attributable — the witness may be at
// fault — but it is also what every honest witness yields for an invalid block,
// so striking on it would let a producer of invalid blocks get honest witness
// servers jailed.
func isWitnessFaultImportError(err error) bool {
	if errors.Is(err, stateless.ErrInvalidWitness) {
		return true
	}
	var missingCode *state.MissingCodeError
	if errors.As(err, &missingCode) {
		return false
	}
	var missingNode *trie.MissingNodeError
	return errors.As(err, &missingNode)
}

// WitnessProvenance records what is known about who chose a witness's bytes. It
// decides how an import failure the witness could have caused is charged.
type WitnessProvenance uint8

const (
	// WitnessFromProducer: byte-identical to the verified BP-signed commitment,
	// or no WIT2 commitment lookup is configured. A failure is the producer's
	// fault; the serving peer is not charged.
	WitnessFromProducer WitnessProvenance = iota
	// WitnessUnproven: not bound to the producer. Its commitment is only
	// deferred (the block's header is not local yet, the normal case at the
	// tip) or there is none (WIT1). The bytes may still be the producer's,
	// relayed by an honest peer, so a failure excludes the source and re-fetches
	// but never strikes it.
	WitnessUnproven
	// WitnessNotProducer: within the size band of the verified BP-signed
	// commitment but not its bytes, so the serving peer chose them. A failure
	// the witness alone decides also strikes it.
	WitnessNotProducer
)

// diverged reports whether the bytes are not proven to be the producer's, so an
// attributable import failure excludes the source and re-fetches the witness.
func (p WitnessProvenance) diverged() bool { return p != WitnessFromProducer }

// maxWitnessImportRetries bounds how many times a block whose import failed with
// a witness accepted on the WIT2 size oracle alone is re-fetched from another
// source before the fetcher gives it up as it would any other failed import. A
// small bound keeps a genuinely invalid block (every honest witness fails) from
// cycling through the peer set, while still recovering from a single server
// that handed out an unusable within-band witness.
const maxWitnessImportRetries = 2

// chargeDivergedWitnessImportFailure applies the WIT2 consequence of an import
// failure to the peer that served the block's witness, when that witness's
// bytes are not proven to be the BP's (diverged) AND the failure is one the
// witness could have caused (isWitnessAttributableImportError). It excludes the
// peer as a witness source for this block, strikes it only when its bytes
// contradict the verified BP commitment (WitnessNotProducer) and the witness
// alone decides the failure (isWitnessFaultImportError), and reports whether
// the block should be handed back to the witness manager for a re-fetch (false
// once the retry budget is spent, or when the witness was not a fetched,
// diverged one).
//
// The error gate matters because "diverged" is the normal case — every node
// persists its own generated witness, so nearly every witness fetched from
// anyone but the BP differs from the signed hash. Charging every import
// failure would let a local problem (a contract bytecode missing from disk,
// which the downloader heals; an interrupted insert) strike and exclude two
// honest witness sources per block until the node has none left.
func (f *BlockFetcher) chargeDivergedWitnessImportFailure(op *blockOrHeaderInject, importErr error) bool {
	if op.witness == nil || !op.witnessDiverged || op.witnessPeer == "" {
		return false
	}
	hash := op.hash()
	if !isWitnessAttributableImportError(importErr) {
		log.Debug("Import failed for a reason the witness server did not cause; not charging it",
			"server", op.witnessPeer, "number", op.number(), "hash", hash, "err", importErr)
		return false
	}
	witnessImportFailureMeter.Mark(1)
	op.witnessImportFailures++
	strike := op.witnessNotProducer && isWitnessFaultImportError(importErr)
	log.Warn("Import failed with a witness not proven to be the BP's; re-fetching from another source",
		"server", op.witnessPeer, "strike", strike, "number", op.number(), "hash", hash, "attempt", op.witnessImportFailures, "err", importErr)
	if strike {
		f.wm.StrikeWitnessServer(op.witnessPeer)
	}
	f.wm.ExcludeWitnessSource(op.witnessPeer, hash)

	if op.fetchWitness == nil || op.witnessImportFailures >= maxWitnessImportRetries {
		log.Warn("Giving up witness re-fetch for block after repeated import failures",
			"number", op.number(), "hash", hash, "failures", op.witnessImportFailures)
		return false
	}
	witnessImportRetryMeter.Mark(1)
	return true
}
