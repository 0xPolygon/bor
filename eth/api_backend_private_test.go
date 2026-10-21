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
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/locals"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/relay"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

func privateSubmissionBackend(t *testing.T, withLocal bool) *EthAPIBackend {
	t.Helper()
	b := initBackend(withLocal)
	b.relay = relay.Init(false, false, false, true, nil)
	b.relay.SetTxPoolChecker(b.eth.txPool.Has)
	t.Cleanup(func() {
		b.relay.Close()
		require.NoError(t, b.eth.txPool.Close())
		b.eth.blockchain.Stop()
	})
	return b
}

func TestSendTxPrivateAdmission(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "private"}[private], func(t *testing.T) {
			b := privateSubmissionBackend(t, true)
			first := pricedSetCodeTx(0, 250000, uint256.NewInt(25*params.GWei), uint256.NewInt(25*params.GWei), key, []unsignedAuth{{nonce: 0, key: key}})
			require.NoError(t, b.SendTx(t.Context(), first))
			require.NoError(t, b.eth.txPool.Sync())
			tx := makeTx(1, nil, nil, key)
			if private {
				b.RecordPrivateTx(tx.Hash())
			}
			err := b.SendTx(t.Context(), tx)
			if private {
				require.ErrorIs(t, err, txpool.ErrInflightTxLimitReached)
			} else {
				require.NoError(t, err)
			}
			require.False(t, b.eth.txPool.Has(tx.Hash()))
		})
	}
}

func TestSendTxPrivateJournal(t *testing.T) {
	b := privateSubmissionBackend(t, false)
	path := filepath.Join(t.TempDir(), "transactions.rlp")
	tracker := locals.New(path, time.Minute, gspec.Config, b.eth.txPool)
	b.eth.localTxTracker = tracker
	require.NoError(t, tracker.Start())
	t.Cleanup(func() { require.NoError(t, tracker.Stop()) })
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, time.Second, time.Millisecond)
	private, public := makeTx(0, nil, nil, key), makeTx(1, nil, nil, key)
	b.RecordPrivateTx(private.Hash())
	require.NoError(t, b.SendTx(t.Context(), private))
	require.NoError(t, b.SendTx(t.Context(), public))
	require.True(t, b.eth.txPool.Has(private.Hash()))
	require.True(t, b.eth.txPool.Has(public.Hash()))
	file, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	stream := rlp.NewStream(file, 0)
	var saved types.Transaction
	require.NoError(t, stream.Decode(&saved))
	require.Equal(t, public.Hash(), saved.Hash(), "only public transactions belong in the ordinary retry journal")
	require.ErrorIs(t, stream.Decode(&saved), io.EOF)
}
