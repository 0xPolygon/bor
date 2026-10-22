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
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func (b *EthAPIBackend) isPrivateTx(hash common.Hash) bool {
	if b.relay == nil {
		return false
	}
	getter := b.relay.GetPrivateTxGetter()
	return getter != nil && getter.IsTxPrivate(hash)
}

// SendPrivateTx reports actual pool admission and never enters the public journal.
func (b *EthAPIBackend) SendPrivateTx(ctx context.Context, tx *types.Transaction) error {
	return b.eth.txPool.Add([]*types.Transaction{tx}, false)[0]
}
