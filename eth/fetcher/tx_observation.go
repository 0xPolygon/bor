// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package fetcher

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

// SetValidationObserver installs a synchronous observer before Start. It reuses
// admission results on the fetcher loop and must not block or change fetcher state.
func (f *TxFetcher) SetValidationObserver(observer func(string, bool, uint64, uint64, bool)) {
	f.validationObserver = observer
}

type txObservation struct {
	items, bytes uint64
	invalid      bool
}

func (f *TxFetcher) validationSummary(txs []*types.Transaction, invalid bool) txObservation {
	if f.validationObserver == nil {
		return txObservation{}
	}
	summary := txObservation{items: uint64(len(txs)), invalid: invalid}
	for _, tx := range txs {
		summary.bytes += tx.Size()
	}
	return summary
}

func (f *TxFetcher) observeDelivery(delivery *txDelivery) {
	if f.validationObserver == nil {
		return
	}
	// Request state belongs to this loop. A reply message type alone does not
	// establish that its peer, request ID and contents match an outstanding request.
	solicited := requestedDelivery(f.requests[delivery.origin], delivery)
	summary := delivery.observation
	f.validationObserver(delivery.origin, solicited, summary.items, summary.bytes, summary.invalid)
}

func requestedDelivery(request *txRequest, delivery *txDelivery) bool {
	if !delivery.direct || request == nil || request.hashes == nil || request.requestID != delivery.requestID {
		return false
	}
	if len(delivery.hashes) > len(request.hashes) {
		return false
	}
	wanted := make(map[common.Hash]struct{}, len(request.hashes))
	for _, hash := range request.hashes {
		wanted[hash] = struct{}{}
	}
	for _, hash := range delivery.hashes {
		if _, ok := wanted[hash]; !ok {
			return false
		}
		delete(wanted, hash)
	}
	return true
}

func invalidTransaction(err error) bool {
	// Context-dependent and pool-policy failures are deliberately excluded.
	return errors.Is(err, txpool.ErrInvalidSender) || errors.Is(err, txpool.ErrKZGVerificationError)
}
