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

package ethapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/relay"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

type privateSubmissionBackend struct {
	*testBackend
	send   func(context.Context, *types.Transaction) error
	pooled *types.Transaction
}

func (b *privateSubmissionBackend) SendTx(ctx context.Context, tx *types.Transaction) error {
	return b.send(ctx, tx)
}

func (b *privateSubmissionBackend) GetPoolTransaction(hash common.Hash) *types.Transaction {
	if b.pooled != nil && b.pooled.Hash() == hash {
		return b.pooled
	}
	return nil
}

func TestSendRawTransactionPrivateConcurrentSubmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		first, second bool
	}{
		{name: "all rejected"},
		{name: "first accepted", first: true},
		{name: "second accepted", second: true},
	} {
		t.Run(tc.name, func(t *testing.T) { testPrivateConcurrentSubmission(t, tc.first, tc.second) })
	}
}

func testPrivateConcurrentSubmission(t *testing.T, firstAccepted, secondAccepted bool) {
	t.Helper()
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{}}
	base := newTestBackend(t, 0, genesis, ethash.NewFaker(), nil)
	defer base.chain.Stop()
	base.acceptPrivateTxs = true
	store := relay.NewPrivateTxStore()
	defer store.Close()
	base.recordPrivateTxFn, base.purgePrivateTxFn = store.Add, store.Purge
	base.beginPrivateTxFn = store.Begin
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	b := &privateSubmissionBackend{testBackend: base}
	b.send = func(ctx context.Context, tx *types.Transaction) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			if firstAccepted {
				return base.SendTx(ctx, tx)
			}
			return txpool.ErrInflightTxLimitReached
		}
		if secondAccepted {
			return base.SendTx(ctx, tx)
		}
		return txpool.ErrInflightTxLimitReached
	}
	api := NewTransactionAPI(b, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, base.acc.Address)
	failed := make(chan error, 1)
	go func() {
		_, err := api.SendRawTransactionPrivate(t.Context(), raw)
		failed <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first submission did not reach admission")
	}
	hash, err := api.SendRawTransactionPrivate(t.Context(), raw)
	if secondAccepted {
		require.NoError(t, err)
		require.Equal(t, tx.Hash(), hash)
	} else {
		require.ErrorIs(t, err, txpool.ErrInflightTxLimitReached)
	}
	require.True(t, store.IsTxPrivate(tx.Hash()), "pending admission must remain private")
	release <- struct{}{}
	select {
	case err := <-failed:
		if firstAccepted {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, txpool.ErrInflightTxLimitReached)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first submission did not finish")
	}
	require.Equal(t, firstAccepted || secondAccepted, store.IsTxPrivate(tx.Hash()), "final marker must reflect successful admission")
}

func TestSendRawTransactionPrivateRelayFailurePreservesCommitment(t *testing.T) {
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{}}
	base := newTestBackend(t, 0, genesis, ethash.NewFaker(), nil)
	defer base.chain.Stop()
	base.privateTxEnabled = true
	store := relay.NewPrivateTxStore()
	defer store.Close()
	base.beginPrivateTxFn = store.Begin
	base.submitPrivateTxFn = func(tx *types.Transaction) error {
		require.True(t, store.IsTxPrivate(tx.Hash()), "local admission must remain private during relay submission")
		return errors.New("relay unavailable")
	}
	api := NewTransactionAPI(base, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, base.acc.Address)
	hash, err := api.SendRawTransactionPrivate(t.Context(), raw)
	require.ErrorContains(t, err, "accepted locally, submission failed")
	require.Equal(t, tx.Hash(), hash)
	require.True(t, store.IsTxPrivate(hash), "relay failure removed an accepted local marker")
}

func TestSendRawTransactionPrivatePanicReleasesReservation(t *testing.T) {
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{}}
	base := newTestBackend(t, 0, genesis, ethash.NewFaker(), nil)
	defer base.chain.Stop()
	base.acceptPrivateTxs = true
	store := relay.NewPrivateTxStore()
	defer store.Close()
	base.beginPrivateTxFn = store.Begin
	backend := &privateSubmissionBackend{testBackend: base}
	backend.send = func(context.Context, *types.Transaction) error { panic("pool unavailable") }
	api := NewTransactionAPI(backend, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, base.acc.Address)
	require.Panics(t, func() { _, _ = api.SendRawTransactionPrivate(t.Context(), raw) })
	require.False(t, store.IsTxPrivate(tx.Hash()), "recovered admission panic retained a reservation")
}

func TestSendRawTransactionPrivateKnownPublicRemainsVisible(t *testing.T) {
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{}}
	base := newTestBackend(t, 0, genesis, ethash.NewFaker(), nil)
	defer base.chain.Stop()
	base.acceptPrivateTxs = true
	store := relay.NewPrivateTxStore()
	defer store.Close()
	begins, admissions := 0, 0
	base.beginPrivateTxFn = func(hash common.Hash) func(bool) {
		begins++
		return store.Begin(hash)
	}
	backend := &privateSubmissionBackend{testBackend: base}
	backend.send = func(_ context.Context, tx *types.Transaction) error {
		admissions++
		require.False(t, store.IsTxPrivate(tx.Hash()), "public transaction hidden during private duplicate admission")
		return txpool.ErrAlreadyKnown
	}
	api := NewTransactionAPI(backend, new(AddrLocker))
	raw, tx := makeSelfSignedRaw(t, api, base.acc.Address)
	backend.pooled = tx
	require.False(t, store.IsTxPrivate(tx.Hash()))
	hash, err := api.SendRawTransactionPrivate(t.Context(), raw)
	require.ErrorIs(t, err, txpool.ErrAlreadyKnown)
	require.Equal(t, common.Hash{}, hash)
	require.Zero(t, begins, "known public duplicate must not reserve a private marker")
	require.Zero(t, admissions, "known duplicate must not enter admission")
	require.False(t, store.IsTxPrivate(tx.Hash()))
}
