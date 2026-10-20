// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"errors"
	"slices"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
)

func TestSendTxPrivatePublicDuplicate(t *testing.T) {
	b := privateSubmissionBackend(t, false)
	tx := makeTx(0, nil, nil, key)
	if err := b.SendTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := b.eth.txPool.Sync(); err != nil {
		t.Fatal(err)
	}
	initial := &handler{txpool: b.eth.txPool, privateTxGetter: b.relay.GetPrivateTxGetter()}
	if !slices.Contains(initial.pendingTransactionHashes(), tx.Hash()) {
		t.Fatal("fixture transaction was not publicly announceable before private call")
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
	_, err = api.SendRawTransactionPrivate(t.Context(), raw)
	if !errors.Is(err, txpool.ErrAlreadyKnown) {
		t.Fatalf("private resubmit error = %v", err)
	}
	if b.relay.GetPrivateTxGetter().IsTxPrivate(tx.Hash()) {
		t.Error("failed private resubmit marked an already-public transaction private")
	}
	h := &handler{txpool: b.eth.txPool, privateTxGetter: b.relay.GetPrivateTxGetter()}
	if !slices.Contains(h.pendingTransactionHashes(), tx.Hash()) {
		t.Error("already-public transaction disappeared from production pending announcements")
	}
}

func TestSendTxPrivateRejectedThenPublic(t *testing.T) {
	b := privateSubmissionBackend(t, true)
	first := pricedSetCodeTx(0, 250000, uint256.NewInt(25*params.GWei), uint256.NewInt(25*params.GWei), key, []unsignedAuth{{nonce: 0, key: key}})
	if err := b.SendTx(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	tx := makeTx(1, nil, nil, key)
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
	_, err = api.SendRawTransactionPrivate(t.Context(), raw)
	if !errors.Is(err, txpool.ErrInflightTxLimitReached) {
		t.Fatalf("private submission error = %v", err)
	}
	if b.eth.txPool.Has(tx.Hash()) {
		t.Fatal("rejected transaction entered pool")
	}
	if b.relay.GetPrivateTxGetter().IsTxPrivate(tx.Hash()) {
		t.Error("rejected transaction left a private marker")
	}
	b.eth.txPool.Clear()
	if err := b.SendTx(t.Context(), makeTx(0, nil, nil, key)); err != nil {
		t.Fatal(err)
	}
	if err := b.SendTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := b.eth.txPool.Sync(); err != nil {
		t.Fatal(err)
	}
	if !b.eth.txPool.Has(tx.Hash()) {
		t.Fatal("later public submission was not admitted")
	}
	h := &handler{txpool: b.eth.txPool, privateTxGetter: b.relay.GetPrivateTxGetter()}
	if !slices.Contains(h.pendingTransactionHashes(), tx.Hash()) {
		t.Error("later public submission was suppressed by failed private marker")
	}
}

func TestSendTxPrivateRejectedMarkers(t *testing.T) {
	b := privateSubmissionBackend(t, true)
	first := pricedSetCodeTx(0, 250000, uint256.NewInt(25*params.GWei), uint256.NewInt(25*params.GWei), key, []unsignedAuth{{nonce: 0, key: key}})
	if err := b.SendTx(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
	retained := 0
	for nonce := uint64(1); nonce <= 32; nonce++ {
		tx := makeTx(nonce, nil, nil, key)
		raw, err := tx.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		_, err = api.SendRawTransactionPrivate(t.Context(), raw)
		if err == nil {
			t.Fatalf("submission %d unexpectedly admitted", nonce)
		}
		if b.eth.txPool.Has(tx.Hash()) {
			t.Fatal("rejected transaction entered pool")
		}
		if b.relay.GetPrivateTxGetter().IsTxPrivate(tx.Hash()) {
			retained++
		}
	}
	if retained != 0 {
		t.Fatalf("%d of 32 failed submissions retained markers", retained)
	}
}

func TestSendTxPrivateExistingCommitment(t *testing.T) {
	b := privateSubmissionBackend(t, false)
	tx := makeTx(0, nil, nil, key)
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
	if _, err = api.SendRawTransactionPrivate(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if !b.relay.GetPrivateTxGetter().IsTxPrivate(tx.Hash()) {
		t.Fatal("successful private submission did not commit its marker")
	}
	if _, err = api.SendRawTransactionPrivate(t.Context(), raw); !errors.Is(err, txpool.ErrAlreadyKnown) {
		t.Fatalf("repeat private error = %v", err)
	}
	if !b.relay.GetPrivateTxGetter().IsTxPrivate(tx.Hash()) {
		t.Fatal("already-known private retry removed its previous commitment")
	}
}
