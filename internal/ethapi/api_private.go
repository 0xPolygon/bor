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
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool/locals"
	"github.com/ethereum/go-ethereum/core/types"
)

// A temporary local rejection must not prevent a producer with room from
// accepting the transaction. The caller separately records local admission.
func (api *TransactionAPI) forwardPrivateTransaction(tx *types.Transaction, localErr error) (common.Hash, error) {
	if localErr != nil && (!api.b.PrivateTxEnabled() || !locals.IsTemporaryReject(localErr)) {
		return common.Hash{}, localErr
	}
	if api.b.PrivateTxEnabled() {
		if err := api.b.SubmitPrivateTx(tx); err != nil {
			if localErr != nil {
				return common.Hash{}, fmt.Errorf("private tx rejected locally: %w; submission failed: %w", localErr, err)
			}
			return tx.Hash(), fmt.Errorf("private tx accepted locally, submission failed. reason: %w", err)
		}
	}
	return tx.Hash(), nil
}
