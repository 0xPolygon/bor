//go:build lls_shadow

package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

// In shadow mode no tx may leave the node: every per-peer send path is a
// no-op (a zero Peer would panic if any of them reached the queues/wire).
func TestShadowPeerSendsNoTransactions(t *testing.T) {
	shadowtrace.SetEnabledForTest(true)
	t.Cleanup(func() { shadowtrace.SetEnabledForTest(false) })

	p := &Peer{}
	hashes := []common.Hash{{1}, {2}}

	if got := p.QueueTransactions(hashes); got != nil {
		t.Fatalf("QueueTransactions retained %d hashes", len(got))
	}

	if got := p.QueuePooledTransactionHashes(hashes); got != nil {
		t.Fatalf("QueuePooledTransactionHashes retained %d hashes", len(got))
	}

	p.AsyncSendTransactions(hashes)
	p.AsyncSendPooledTransactionHashes(hashes)

	if err := p.SendTransactions(types.Transactions{types.NewTx(&types.LegacyTx{})}); err != nil {
		t.Fatalf("SendTransactions: %v", err)
	}
}
