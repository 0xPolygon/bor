// Copyright 2024 The go-ethereum Authors
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

package stateless

import (
	"io"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// BorWitness is the canonical 3-field RLP encoding used for network
// transmission in Bor. The State field carries all proof data — both
// contract bytecodes and MPT state trie nodes — as a flat list of byte slices.
type BorWitness struct {
	Context *types.Header
	Headers []*types.Header
	State   [][]byte
}

// ExtWitness is a witness representation used by the JSON debug API and for
// backward-compatible RLP decoding of peers that temporarily used the
// extended 5-field format. It is not the canonical wire format.
type ExtWitness struct {
	Context *types.Header
	Headers []*types.Header `json:"headers"`
	Codes   []hexutil.Bytes `json:"codes"`
	State   []hexutil.Bytes `json:"state"`
	Keys    []hexutil.Bytes `json:"keys"`
}

// ToExtWitness converts our internal witness representation to the ExtWitness
// form used by the JSON debug API.
func (w *Witness) ToExtWitness() *ExtWitness {
	w.lock.RLock()
	defer w.lock.RUnlock()

	ext := &ExtWitness{
		Context: w.context,
		Headers: w.Headers,
	}
	ext.Codes = make([]hexutil.Bytes, 0, len(w.Codes))
	for code := range w.Codes {
		ext.Codes = append(ext.Codes, []byte(code))
	}
	ext.State = make([]hexutil.Bytes, 0, len(w.State))
	for node := range w.State {
		ext.State = append(ext.State, []byte(node))
	}
	return ext
}

// fromExtWitness converts the ExtWitness format into our internal representation.
func (w *Witness) fromExtWitness(ext *ExtWitness) error {
	w.context = ext.Context
	w.Headers = ext.Headers

	w.Codes = make(map[string]struct{}, len(ext.Codes))
	for _, code := range ext.Codes {
		w.Codes[string(code)] = struct{}{}
	}
	w.State = make(map[string]struct{}, len(ext.State))
	for _, node := range ext.State {
		w.State[string(node)] = struct{}{}
	}
	return nil
}

// EncodeRLP serializes a witness as RLP using the canonical BorWitness 3-field
// format. Only state trie nodes are encoded; contract bytecodes are not
// included in the wire format.
func (w *Witness) EncodeRLP(wr io.Writer) error {
	w.lock.RLock()
	defer w.lock.RUnlock()

	bw := &BorWitness{
		Context: w.context,
		Headers: w.Headers,
		State:   make([][]byte, 0, len(w.State)),
	}
	for node := range w.State {
		bw.State = append(bw.State, []byte(node))
	}
	return rlp.Encode(wr, bw)
}

// DecodeRLP decodes a witness from RLP. It first attempts the canonical
// BorWitness 3-field format. If that fails (e.g. the peer sent the legacy
// 5-field ExtWitness encoding), it falls back to ExtWitness for backward
// compatibility.
//
// When decoding BorWitness, State items are placed into w.State and w.Codes
// is left empty, since codes are not part of the BorWitness wire format.
func (w *Witness) DecodeRLP(s *rlp.Stream) error {
	raw, err := s.Raw()
	if err != nil {
		return err
	}

	// Try BorWitness first. A 5-field list will fail here with "too many
	// elements", routing cleanly to the ExtWitness fallback.
	var bw BorWitness
	if err := rlp.DecodeBytes(raw, &bw); err == nil {
		w.context = bw.Context
		w.Headers = bw.Headers
		w.Codes = make(map[string]struct{})
		w.State = make(map[string]struct{}, len(bw.State))
		for _, node := range bw.State {
			w.State[string(node)] = struct{}{}
		}
		return nil
	}

	// Fall back to the legacy 5-field ExtWitness format.
	var ext ExtWitness
	if err := rlp.DecodeBytes(raw, &ext); err != nil {
		return err
	}
	return w.fromExtWitness(&ext)
}
