package fetcher

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPeerPolicyTransactionClassification(t *testing.T) {
	for _, test := range []struct {
		err     error
		invalid bool
	}{
		{nil, false}, {txpool.ErrInvalidSender, true}, {txpool.ErrKZGVerificationError, true},
		{fmt.Errorf("wrapped: %w", txpool.ErrInvalidSender), true},
		{core.ErrNonceTooLow, false}, {core.ErrInsufficientFunds, false},
		{txpool.ErrAlreadyKnown, false}, {txpool.ErrUnderpriced, false},
		{txpool.ErrReplaceUnderpriced, false}, {txpool.ErrTxGasPriceTooLow, false},
		{txpool.ErrAccountLimitExceeded, false},
	} {
		if got := invalidTransaction(test.err); got != test.invalid {
			t.Errorf("%v: invalid=%v", test.err, got)
		}
	}
}

func TestPeerPolicyTransactionObserver(t *testing.T) {
	f := &TxFetcher{}
	txs := []*types.Transaction{types.NewTx(&types.LegacyTx{Nonce: 1}), types.NewTx(&types.LegacyTx{Nonce: 2})}
	calls := 0
	f.SetValidationObserver(func(id string, direct bool, count, bytes uint64, invalid bool) {
		calls++
		if id != "peer" || !direct || count != 2 || bytes != txs[0].Size()+txs[1].Size() || !invalid {
			t.Fatal("incorrect transaction observation")
		}
	})
	f.observeValidation("peer", true, txs, true)
	if calls != 1 {
		t.Fatal("missing observation")
	}
}
