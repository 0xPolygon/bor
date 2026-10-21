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

package relay

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPrivateTxRelayReservation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			s := &RelayService{}
			if enabled {
				s.privateTxStore = NewPrivateTxStore()
				defer s.privateTxStore.Close()
			}
			tx := types.NewTx(&types.LegacyTx{})
			finish := s.BeginPrivateTx(tx.Hash())
			if finish == nil {
				t.Fatal("missing completion callback")
			}
			if enabled && !s.privateTxStore.IsTxPrivate(tx.Hash()) {
				t.Fatal("submission was visible before admission completed")
			}
			finish(true)
			finish(false)
			if enabled && !s.privateTxStore.IsTxPrivate(tx.Hash()) {
				t.Fatal("accepted transaction lost its private marker")
			}
		})
	}
}

func TestPrivateTxRelayFailureStaysPrivate(t *testing.T) {
	store := NewPrivateTxStore()
	defer store.Close()
	s := &RelayService{privateTxStore: store}
	tx := types.NewTx(&types.LegacyTx{})
	s.BeginPrivateTx(tx.Hash())(true)
	if err := s.SubmitPrivateTransaction(tx); err == nil {
		t.Fatal("unconfigured relay unexpectedly accepted the transaction")
	}
	store.mu.Lock()
	store.txs[tx.Hash()] = time.Now().Add(-24 * time.Hour)
	store.mu.Unlock()
	store.SetTxPoolChecker(func(hash common.Hash) bool { return hash == tx.Hash() })
	store.sweepOnce()
	if !store.IsTxPrivate(tx.Hash()) {
		t.Fatal("relay failure or elapsed time made a retained private transaction public")
	}
}
