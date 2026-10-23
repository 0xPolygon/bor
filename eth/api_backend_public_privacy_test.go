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
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/relay"
	"github.com/ethereum/go-ethereum/internal/ethapi"
)

func TestSendTxPrivateEvictedPublicResubmission(t *testing.T) {
	for _, tracker := range []bool{false, true} {
		t.Run(map[bool]string{false: "no tracker", true: "tracker"}[tracker], func(t *testing.T) {
			b := privateSubmissionBackend(t, tracker)
			tx := makeTx(0, nil, nil, key)
			raw, err := tx.MarshalBinary()
			require.NoError(t, err)
			api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
			_, err = api.SendRawTransactionPrivate(t.Context(), raw)
			require.NoError(t, err)
			require.NoError(t, b.eth.txPool.Sync())
			b.eth.txPool.SetGasTip(new(big.Int).Add(tx.GasTipCap(), big.NewInt(1)))
			require.False(t, b.eth.txPool.Has(tx.Hash()))
			require.True(t, b.isPrivateTx(tx.Hash()))
			b.eth.txPool.SetGasTip(big.NewInt(0))
			hash, err := api.SendRawTransaction(t.Context(), raw)
			require.ErrorIs(t, err, relay.ErrPrivateTxConflict)
			require.Equal(t, common.Hash{}, hash)
			require.False(t, b.eth.txPool.Has(tx.Hash()))
			require.True(t, b.isPrivateTx(tx.Hash()))
		})
	}
}
func TestSendTxPrivatePendingPublicConflict(t *testing.T) {
	b := privateSubmissionBackend(t, true)
	tx := makeTx(0, nil, nil, key)
	finish := b.BeginPrivateTx(tx.Hash())
	require.ErrorIs(t, b.SendTx(t.Context(), tx), relay.ErrPrivateTxConflict)
	require.False(t, b.eth.txPool.Has(tx.Hash()))
	finish(false)
	require.NoError(t, b.SendTx(t.Context(), tx))
	require.False(t, b.isPrivateTx(tx.Hash()))
}

func TestSendTxPrivateResidentPublicDuplicate(t *testing.T) {
	for _, tracker := range []bool{false, true} {
		t.Run(map[bool]string{false: "no tracker", true: "tracker"}[tracker], func(t *testing.T) {
			b := privateSubmissionBackend(t, tracker)
			tx := makeTx(0, nil, nil, key)
			raw, err := tx.MarshalBinary()
			require.NoError(t, err)
			api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
			_, err = api.SendRawTransactionPrivate(t.Context(), raw)
			require.NoError(t, err)
			require.NoError(t, b.eth.txPool.Sync())
			_, err = api.SendRawTransaction(t.Context(), raw)
			require.ErrorIs(t, err, txpool.ErrAlreadyKnown)
			require.True(t, b.eth.txPool.Has(tx.Hash()))
			require.True(t, b.isPrivateTx(tx.Hash()))
			h := &handler{txpool: b.eth.txPool, privateTxGetter: b.relay.GetPrivateTxGetter()}
			require.NotContains(t, h.pendingTransactionHashes(), tx.Hash())
		})
	}
}

type privateAdmissionSubPool struct {
	txpool.SubPool
	onAdd func()
}

func (p *privateAdmissionSubPool) Add(txs []*types.Transaction, sync bool) []error {
	errors := p.SubPool.Add(txs, sync)
	p.onAdd()
	return errors
}

func TestSendTxPrivateAdmissionDuringPublicCall(t *testing.T) {
	b := privateSubmissionBackend(t, false)
	require.NoError(t, b.eth.txPool.Close())
	tx := makeTx(0, nil, nil, key)
	config := legacypool.DefaultConfig
	config.Journal = ""
	subpool := &privateAdmissionSubPool{
		SubPool: legacypool.New(config, b.eth.blockchain),
		onAdd:   func() { b.RecordPrivateTx(tx.Hash()) },
	}
	pool, err := txpool.New(config.PriceLimit, b.eth.blockchain, []txpool.SubPool{subpool})
	require.NoError(t, err)
	b.eth.txPool = pool
	b.relay.SetTxPoolChecker(pool.Has)
	require.ErrorIs(t, b.SendTx(t.Context(), tx), txpool.ErrAlreadyKnown)
	require.True(t, pool.Has(tx.Hash()))
	require.True(t, b.isPrivateTx(tx.Hash()))
}
