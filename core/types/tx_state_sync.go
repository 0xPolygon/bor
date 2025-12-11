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
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// Consensus-level limits for StateSyncTx.
// TODO marcello: tune these values
const (
	MaxStateSyncTxSizeBytes   = 2 * 1024 * 1024 // 2 MiB
	MaxStateSyncEntries       = 2048
	MaxStateSyncDataSizeBytes = 256 * 1024 // 256 KiB
)

var (
	ErrStateSyncTxTooLarge     = errors.New("state sync tx too large")
	ErrStateSyncTooManyEntries = errors.New("too many state sync data entries")
	ErrStateSyncDataTooLarge   = errors.New("state sync data payload too large")
)

// StateSyncTx is the system transaction of Bor to introduce fetched state sync events from Heimdall
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

	if len(tx.StateSyncData) > MaxStateSyncEntries {
		return fmt.Errorf("%w: entries=%d max=%d", ErrStateSyncTooManyEntries, len(tx.StateSyncData), MaxStateSyncEntries)
	}

	enc := make([]StateSyncData, 0, len(tx.StateSyncData))
	for i, d := range tx.StateSyncData {
		if d == nil {
			continue
		}
		if len(d.Data) > MaxStateSyncDataSizeBytes {
			return fmt.Errorf("%w: index=%d size=%d max=%d",
				ErrStateSyncDataTooLarge, i, len(d.Data), MaxStateSyncDataSizeBytes)
		}
		enc = append(enc, *d)
	}

	// Encode into a temporary buffer to check final size.
	var tmp bytes.Buffer
	if err := rlp.Encode(&tmp, enc); err != nil {
		return err
	}
	if tmp.Len() > MaxStateSyncTxSizeBytes {
		return fmt.Errorf("%w: size=%d max=%d", ErrStateSyncTxTooLarge, tmp.Len(), MaxStateSyncTxSizeBytes)
	}

	_, err := buf.Write(tmp.Bytes())
	return err
}

func (tx *StateSyncTx) decode(b []byte) error {
	if tx == nil {
		return errors.New("nil StateSyncTx")
	}

	if len(b) > MaxStateSyncTxSizeBytes {
		return fmt.Errorf("%w: size=%d max=%d", ErrStateSyncTxTooLarge, len(b), MaxStateSyncTxSizeBytes)
	}

	var dec []StateSyncData
	if err := rlp.DecodeBytes(b, &dec); err != nil {
		return err
	}

	if len(dec) > MaxStateSyncEntries {
		return fmt.Errorf("%w: entries=%d max=%d", ErrStateSyncTooManyEntries, len(dec), MaxStateSyncEntries)
	}

	tx.StateSyncData = make([]*StateSyncData, len(dec))
	for i, e := range dec {
		if len(e.Data) > MaxStateSyncDataSizeBytes {
			return fmt.Errorf("%w: index=%d size=%d max=%d",
				ErrStateSyncDataTooLarge, i, len(e.Data), MaxStateSyncDataSizeBytes)
		}
		tx.StateSyncData[i] = &StateSyncData{
			ID:       e.ID,
			Contract: e.Contract,
			Data:     e.Data,
			TxHash:   e.TxHash,
		}
	}
	return nil
}

func (tx *StateSyncTx) sigHash(_ *big.Int) common.Hash {
	panic("StateSyncTx has no sigHash")
}
