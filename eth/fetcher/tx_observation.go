// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package fetcher

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
)

// SetValidationObserver installs a synchronous observer before Start. It reuses
// admission results on the fetcher loop and must not block or change fetcher state.
// Its final arguments report newly admitted transactions, complete pool outcomes,
// and the arrival time before admission so reconnects can reject stale credit.
func (f *TxFetcher) SetValidationObserver(observer func(string, DeliveryProvenance, uint64, uint64, bool, uint64, bool, mclock.AbsTime)) {
	f.validationObserver = observer
}

type txObservation struct {
	items, bytes, accepted uint64
	invalid, complete      bool
	receivedAt             mclock.AbsTime
}

func (f *TxFetcher) validationSummary(txs []*types.Transaction, invalid bool, accepted uint64, complete bool, receivedAt mclock.AbsTime) txObservation {
	if f.validationObserver == nil {
		return txObservation{}
	}
	if !complete {
		accepted = 0
	}
	summary := txObservation{items: uint64(len(txs)), invalid: invalid, accepted: accepted, complete: complete, receivedAt: receivedAt}
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
	f.validationObserver(delivery.origin, solicited, summary.items, summary.bytes, summary.invalid, summary.accepted, summary.complete, summary.receivedAt)
}

type DeliveryProvenance uint8

const (
	UnknownDelivery DeliveryProvenance = iota
	UnsolicitedDelivery
	RequestedDelivery
)

func requestedDelivery(request *txRequest, delivery *txDelivery) DeliveryProvenance {
	if !delivery.direct {
		return UnsolicitedDelivery
	}
	if request == nil || request.hashes == nil || request.requestID != delivery.requestID {
		return UnknownDelivery
	}
	// Pool admission can return fewer results than received transactions (for
	// example when the stateless pool is disabled). Do not exempt uncorrelated bytes.
	if uint64(len(delivery.hashes)) != delivery.observation.items {
		return UnknownDelivery
	}
	if len(delivery.hashes) > len(request.hashes) {
		return UnsolicitedDelivery
	}
	wanted := make(map[common.Hash]struct{}, len(request.hashes))
	for _, hash := range request.hashes {
		wanted[hash] = struct{}{}
	}
	for _, hash := range delivery.hashes {
		if _, ok := wanted[hash]; !ok {
			return UnsolicitedDelivery
		}
		delete(wanted, hash)
	}
	return RequestedDelivery
}

func invalidTransaction(err error) bool {
	// Context-dependent and pool-policy failures are deliberately excluded.
	return errors.Is(err, txpool.ErrInvalidSender) || errors.Is(err, txpool.ErrKZGVerificationError)
}
