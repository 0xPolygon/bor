// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package fetcher

import (
	"errors"

	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

// SetValidationObserver installs a synchronous observer before Start. It reuses
// admission results and must not block or change fetcher state.
func (f *TxFetcher) SetValidationObserver(observer func(string, bool, uint64, uint64, bool)) {
	f.validationObserver = observer
}

func (f *TxFetcher) observeValidation(peer string, direct bool, txs []*types.Transaction, invalid bool) {
	if f.validationObserver == nil {
		return
	}
	var bytes uint64
	for _, tx := range txs {
		bytes += tx.Size()
	}
	f.validationObserver(peer, direct, uint64(len(txs)), bytes, invalid)
}

func invalidTransaction(err error) bool {
	// Context-dependent and pool-policy failures are deliberately excluded.
	return errors.Is(err, txpool.ErrInvalidSender) || errors.Is(err, txpool.ErrKZGVerificationError)
}
