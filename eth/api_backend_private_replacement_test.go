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
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
)

type privateRelayFailureBackend struct {
	*EthAPIBackend
	relayCalls int
}

func (b *privateRelayFailureBackend) PrivateTxEnabled() bool { return true }

func (b *privateRelayFailureBackend) SubmitPrivateTx(*types.Transaction) error {
	b.relayCalls++
	return errors.New("producer rejected transaction")
}

func TestSendTxPrivateRelayFailureReplacement(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "private"}[private], func(t *testing.T) {
			b := &privateRelayFailureBackend{EthAPIBackend: privateSubmissionBackend(t, false)}
			api := ethapi.NewTransactionAPI(b, new(ethapi.AddrLocker))
			original := makeTx(0, nil, nil, key)
			raw, err := original.MarshalBinary()
			require.NoError(t, err)
			hash, err := api.SendRawTransactionPrivate(t.Context(), raw)
			require.ErrorContains(t, err, "accepted locally, submission failed")
			require.Equal(t, original.Hash(), hash)
			require.NoError(t, b.eth.txPool.Sync())
			require.Equal(t, txpool.TxStatusPending, b.TxStatus(hash))
			require.True(t, b.relay.GetPrivateTxGetter().IsTxPrivate(hash))
			h := &handler{txpool: b.eth.txPool, privateTxGetter: b.relay.GetPrivateTxGetter()}
			require.NotContains(t, h.pendingTransactionHashes(), hash)

			next := makeTx(1, nil, nil, key)
			require.NoError(t, b.SendTx(t.Context(), next))
			replacement := makeTx(0, big.NewInt(50*params.GWei), big.NewInt(0), key)
			raw, err = replacement.MarshalBinary()
			require.NoError(t, err)
			if private {
				hash, err = api.SendRawTransactionPrivate(t.Context(), raw)
				require.ErrorContains(t, err, "accepted locally, submission failed")
				require.Equal(t, 2, b.relayCalls)
			} else {
				hash, err = api.SendRawTransaction(t.Context(), raw)
				require.NoError(t, err)
				require.Equal(t, 1, b.relayCalls)
			}
			require.Equal(t, replacement.Hash(), hash)
			require.NoError(t, b.eth.txPool.Sync())
			require.False(t, b.eth.txPool.Has(original.Hash()))
			require.Equal(t, txpool.TxStatusPending, b.TxStatus(replacement.Hash()))
			require.Equal(t, txpool.TxStatusPending, b.TxStatus(next.Hash()))
			require.Equal(t, private, b.relay.GetPrivateTxGetter().IsTxPrivate(replacement.Hash()))
			announcements := h.pendingTransactionHashes()
			require.NotContains(t, announcements, original.Hash())
			require.Contains(t, announcements, next.Hash())
			if private {
				require.NotContains(t, announcements, replacement.Hash())
			} else {
				require.Contains(t, announcements, replacement.Hash())
			}
		})
	}
}
