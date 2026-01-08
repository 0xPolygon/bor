// Copyright 2021 The go-ethereum Authors
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

package types

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// StateSyncTx is a system transaction in Bor that introduces state sync events.
type StateSyncTx struct {
	StateSyncData []*StateSyncData
}

func (tx *StateSyncTx) copy() TxData {
	if tx == nil {
		return nil
	}
	out := &StateSyncTx{}
	if tx.StateSyncData != nil {
		out.StateSyncData = make([]*StateSyncData, len(tx.StateSyncData))
		for i, d := range tx.StateSyncData {
			if d != nil {
				c := *d
				out.StateSyncData[i] = &c
			} else {
				out.StateSyncData[i] = nil
			}
		}
	}
	return out
}

// accessors for innerTx.
func (tx *StateSyncTx) txType() byte           { return StateSyncTxType }
func (tx *StateSyncTx) chainID() *big.Int      { return nil }
func (tx *StateSyncTx) accessList() AccessList { return nil }
func (tx *StateSyncTx) data() []byte           { return []byte{} }
func (tx *StateSyncTx) gas() uint64            { return 0 }
func (tx *StateSyncTx) gasPrice() *big.Int     { return big.NewInt(0) }
func (tx *StateSyncTx) gasTipCap() *big.Int    { return big.NewInt(0) }
func (tx *StateSyncTx) gasFeeCap() *big.Int    { return big.NewInt(0) }
func (tx *StateSyncTx) value() *big.Int        { return big.NewInt(0) }
func (tx *StateSyncTx) nonce() uint64          { return 0 }
func (tx *StateSyncTx) to() *common.Address    { return &common.Address{} }

func (tx *StateSyncTx) effectiveGasPrice(dst *big.Int, baseFee *big.Int) *big.Int {
	return big.NewInt(0)
}

func (tx *StateSyncTx) rawSignatureValues() (v, r, s *big.Int) {
	return big.NewInt(0), big.NewInt(0), big.NewInt(0)
}

func (tx *StateSyncTx) setSignatureValues(chainID, v, r, s *big.Int) {
	panic("setSignatureValues called on StateSyncTx")
}

func (tx *StateSyncTx) encode(buf *bytes.Buffer) error {
	if tx == nil {
		return errors.New("nil StateSyncTx")
	}
	enc := make([]StateSyncData, 0, len(tx.StateSyncData))
	for _, d := range tx.StateSyncData {
		if d == nil {
			continue
		}
		enc = append(enc, *d)
	}
	return rlp.Encode(buf, enc)
}

func (tx *StateSyncTx) decode(b []byte) error {
	if tx == nil {
		return errors.New("nil StateSyncTx")
	}
	var dec []StateSyncData
	if err := rlp.DecodeBytes(b, &dec); err != nil {
		return err
	}
	tx.StateSyncData = make([]*StateSyncData, len(dec))
	for i, e := range dec {
		tx.StateSyncData[i] = &StateSyncData{
			ID:       e.ID,
			Contract: e.Contract,
			Data:     e.Data,
			TxHash:   e.TxHash,
		}
	}
	return nil
}

func (tx *StateSyncTx) sigHash(chainID *big.Int) common.Hash {
	panic("sigHash called on StateSyncTx")
}
