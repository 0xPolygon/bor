package fetcher

import (
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
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
// (missing trie node), or execution against the witness disagreeing with the
// header (state root, receipt root, gas used, bloom, requests hash). Every other
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
