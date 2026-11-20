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

package eth

import (
	"bytes"
	"fmt"
	"io"
	"iter"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// This is just a sanity limit for the size of a single receipt.
const maxReceiptSize = 16 * 1024 * 1024

// Receipt is the representation of receipts for networking purposes.
type Receipt struct {
	TxType            byte
	PostStateOrStatus []byte
	GasUsed           uint64
	Logs              rlp.RawValue
}

func newReceipt(tr *types.Receipt) Receipt {
	r := Receipt{TxType: tr.Type, GasUsed: tr.CumulativeGasUsed}
	if tr.PostState != nil {
		r.PostStateOrStatus = tr.PostState
	} else {
		r.PostStateOrStatus = new(big.Int).SetUint64(tr.Status).Bytes()
	}
	r.Logs, _ = rlp.EncodeToBytes(tr.Logs)
	return r
}

// decode68 parses a receipt in the eth/68 network encoding.
func (r *Receipt) decode68(b []byte) error {
	k, content, _, err := rlp.Split(b)
	if err != nil {
		return err
	}

	*r = Receipt{}
	if k == rlp.List {
		// Legacy receipt.
		return r.decodeInnerList(b, false, true)
	}
	// Typed receipt.
	if len(content) < 2 || len(content) > maxReceiptSize {
		return fmt.Errorf("invalid receipt size %d", len(content))
	}
	r.TxType = content[0]
	return r.decodeInnerList(content[1:], false, true)
}

// decode69 parses a receipt in the eth/69 network encoding.
func (r *Receipt) decode69(b []byte) error {
	*r = Receipt{}
	return r.decodeInnerList(b, true, false)
}

// decodeDatabase parses a receipt in the basic database encoding.
func (r *Receipt) decodeDatabase(txType byte, b []byte) error {
	*r = Receipt{TxType: txType}
	return r.decodeInnerList(b, false, false)
}

func (r *Receipt) decodeInnerList(input []byte, readTxType, readBloom bool) error {
	input, _, err := rlp.SplitList(input)
	if err != nil {
		return fmt.Errorf("inner list: %v", err)
	}

	// txType
	if readTxType {
		var txType uint64
		txType, input, err = rlp.SplitUint64(input)
		if err != nil {
			return fmt.Errorf("invalid txType: %w", err)
		}
		if txType > 0x7f {
			return fmt.Errorf("invalid txType: too large")
		}
		r.TxType = byte(txType)
	}

	// status
	r.PostStateOrStatus, input, err = rlp.SplitString(input)
	if err != nil {
		return fmt.Errorf("invalid postStateOrStatus: %w", err)
	}
	if len(r.PostStateOrStatus) > 1 && len(r.PostStateOrStatus) != 32 {
		return fmt.Errorf("invalid postStateOrStatus length %d", len(r.PostStateOrStatus))
	}

	// gas
	r.GasUsed, input, err = rlp.SplitUint64(input)
	if err != nil {
		return fmt.Errorf("invalid gasUsed: %w", err)
	}

	// bloom
	if readBloom {
		var bloomBytes []byte
		bloomBytes, input, err = rlp.SplitString(input)
		if err != nil {
			return fmt.Errorf("invalid bloom: %v", err)
		}
		if len(bloomBytes) != types.BloomByteLength {
			return fmt.Errorf("invalid bloom length %d", len(bloomBytes))
		}
	}

	// logs
	_, rest, err := rlp.SplitList(input)
	if err != nil {
		return fmt.Errorf("invalid logs: %w", err)
	}
	if len(rest) != 0 {
		return fmt.Errorf("junk at end of receipt")
	}
	r.Logs = input
	return nil
}

// encodeForStorage produces the storage encoding, i.e. the result matches
// the RLP encoding of types.ReceiptForStorage.
func (r *Receipt) encodeForStorage(w *rlp.EncoderBuffer) {
	list := w.List()
	w.WriteBytes(r.PostStateOrStatus)
	w.WriteUint64(r.GasUsed)
	w.Write(r.Logs)
	w.ListEnd(list)
}

// encodeForNetwork68 produces the eth/68 network protocol encoding of a receipt.
// Note this recomputes the bloom filter of the receipt.
func (r *Receipt) encodeForNetwork68(buf *receiptListBuffers, w *rlp.EncoderBuffer) {
	writeInner := func(w *rlp.EncoderBuffer) {
		list := w.List()
		w.WriteBytes(r.PostStateOrStatus)
		w.WriteUint64(r.GasUsed)
		bloom := r.bloom(&buf.bloom)
		w.WriteBytes(bloom[:])
		w.Write(r.Logs)
		w.ListEnd(list)
	}

	if r.TxType == 0 {
		writeInner(w)
	} else {
		buf.tmp.Reset()
		buf.tmp.WriteByte(r.TxType)
		buf.enc.Reset(&buf.tmp)
		writeInner(&buf.enc)
		buf.enc.Flush()
		w.WriteBytes(buf.tmp.Bytes())
	}
}

// encodeForNetwork69 produces the eth/69 network protocol encoding of a receipt.
func (r *Receipt) encodeForNetwork69(w *rlp.EncoderBuffer) {
	list := w.List()
	w.WriteUint64(uint64(r.TxType))
	w.WriteBytes(r.PostStateOrStatus)
	w.WriteUint64(r.GasUsed)
	w.Write(r.Logs)
	w.ListEnd(list)
}

// encodeForHash encodes a receipt for the block receiptsRoot derivation.
func (r *Receipt) encodeForHash(buf *receiptListBuffers, out *bytes.Buffer) {
	// For typed receipts, add the tx type.
	if r.TxType != 0 {
		out.WriteByte(r.TxType)
	}
	// Encode list = [postStateOrStatus, gasUsed, bloom, logs].
	w := &buf.enc
	w.Reset(out)
	l := w.List()
	w.WriteBytes(r.PostStateOrStatus)
	w.WriteUint64(r.GasUsed)
	bloom := r.bloom(&buf.bloom)
	w.WriteBytes(bloom[:])
	w.Write(r.Logs)
	w.ListEnd(l)
	w.Flush()
}

// bloom computes the bloom filter of the receipt.
// Note this doesn't check the validity of encoding, and will produce an invalid filter
// for invalid input. This is acceptable for the purpose of this function, which is
// recomputing the receipt hash.
func (r *Receipt) bloom(buffer *[6]byte) types.Bloom {
	var b types.Bloom
	logsIter, err := rlp.NewListIterator(r.Logs)
	if err != nil {
		return b
	}
	for logsIter.Next() {
		log, _, _ := rlp.SplitList(logsIter.Value())
		address, log, _ := rlp.SplitString(log)
		b.AddWithBuffer(address, buffer)
		topicsIter, err := rlp.NewListIterator(log)
		if err != nil {
			return b
		}
		for topicsIter.Next() {
			topic, _, _ := rlp.SplitString(topicsIter.Value())
			b.AddWithBuffer(topic, buffer)
		}
	}
	return b
}

type receiptListBuffers struct {
	enc   rlp.EncoderBuffer
	bloom [6]byte
	tmp   bytes.Buffer
}

func initBuffers(buf **receiptListBuffers) {
	if *buf == nil {
		*buf = new(receiptListBuffers)
	}
}

// encodeForStorage encodes a list of receipts for the database.
func (buf *receiptListBuffers) encodeForStorage(rs rlp.RawList[Receipt], decode func([]byte, *Receipt) error) (rlp.RawValue, error) {
	var out bytes.Buffer
	w := &buf.enc
	w.Reset(&out)
	outer := w.List()
	it := rs.ContentIterator()
	for it.Next() {
		var receipt Receipt
		if err := decode(it.Value(), &receipt); err != nil {
			return nil, err
		}
		receipt.encodeForStorage(w)
	}
	if it.Err() != nil {
		return nil, fmt.Errorf("bad list: %v", it.Err())
	}
	w.ListEnd(outer)
	w.Flush()
	return out.Bytes(), nil
}

// excludeStateSyncReceipt drops the state sync receipt from the end of the list
// if present. A receipt that fails to decode leaves the list unchanged, so the
// receipt root check rejects it later.
func excludeStateSyncReceipt(items *rlp.RawList[Receipt], decode func([]byte, *Receipt) error) {
	n := items.Len()
	if n == 0 {
		return
	}
	raw := make([][]byte, 0, n)
	it := items.ContentIterator()
	for it.Next() {
		raw = append(raw, it.Value())
	}
	if len(raw) != n {
		return
	}
	var last Receipt
	if decode(raw[n-1], &last) != nil {
		return
	}
	// The state-sync receipt can either have a 0 cumulative gas used (this depends on the remote peer) or
	// have the same cumulative gas used as the previous receipt as state-sync transactions uses 0 gas and
	// hence they don't contribute to the cumulative gas used value.
	drop := last.GasUsed == 0
	if !drop && n >= 2 {
		var prev Receipt
		drop = decode(raw[n-2], &prev) == nil && prev.GasUsed == last.GasUsed
	}
	if !drop {
		return
	}
	var trimmed rlp.RawList[Receipt]
	for _, r := range raw[:n-1] {
		if err := trimmed.AppendRaw(r); err != nil {
			return
		}
	}
	*items = trimmed
}

// ReceiptList68 is a block receipt list as downloaded by eth/68.
// This also implements types.DerivableList for validation purposes.
type ReceiptList68 struct {
	buf   *receiptListBuffers
	items rlp.RawList[Receipt]
}

// NewReceiptList68 creates a receipt list.
// This is slow, and exists for testing purposes.
func NewReceiptList68(trs []*types.Receipt) *ReceiptList68 {
	rl := new(ReceiptList68)
	initBuffers(&rl.buf)
	enc := rlp.NewEncoderBuffer(nil)
	for _, tr := range trs {
		r := newReceipt(tr)
		r.encodeForNetwork68(rl.buf, &enc)
		rl.items.AppendRaw(enc.ToBytes())
		enc.Reset(nil)
	}
	return rl
}

func blockReceiptsToNetwork68(blockReceipts, blockBody rlp.RawValue) ([]byte, error) {
	txTypesIter, err := txTypesInBody(blockBody)
	if err != nil {
		return nil, fmt.Errorf("invalid block body: %v", err)
	}
	nextTxType, stopTxTypes := iter.Pull(txTypesIter)
	defer stopTxTypes()

	var (
		out bytes.Buffer
		buf receiptListBuffers
	)
	blockReceiptIter, err := rlp.NewListIterator(blockReceipts)
	if err != nil {
		if len(blockReceipts) == 0 {
			blockReceiptIter, err = rlp.NewListIterator(rlp.EmptyList)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid block receipts: %w", err)
		}
	}
	w := rlp.NewEncoderBuffer(&out)
	outer := w.List()
	for i := 0; blockReceiptIter.Next(); i++ {
		txType, _ := nextTxType()
		var r Receipt
		if err := r.decodeDatabase(txType, blockReceiptIter.Value()); err != nil {
			return nil, fmt.Errorf("invalid database receipt %d: %v", i, err)
		}
		r.encodeForNetwork68(&buf, &w)
	}
	w.ListEnd(outer)
	w.Flush()
	return out.Bytes(), nil
}

// setBuffers implements ReceiptsList.
func (rl *ReceiptList68) setBuffers(buf *receiptListBuffers) {
	rl.buf = buf
}

// rawItems implements ReceiptsList.
func (rl *ReceiptList68) rawItems() *rlp.RawList[Receipt] {
	return &rl.items
}

// EncodeForStorage encodes the receipts for storage into the database.
func (rl *ReceiptList68) EncodeForStorage() (rlp.RawValue, error) {
	initBuffers(&rl.buf)
	return rl.buf.encodeForStorage(rl.items, func(data []byte, r *Receipt) error {
		return r.decode68(data)
	})
}

// Derivable turns the receipts into a list that can derive the root hash.
func (rl *ReceiptList68) Derivable() types.DerivableList {
	initBuffers(&rl.buf)
	return newDerivableRawList(&rl.items, func(data []byte, outbuf *bytes.Buffer) {
		var r Receipt
		if r.decode68(data) != nil {
			// Write invalid data to corrupt the receipt root
			outbuf.WriteByte(0x00)
			return
		}
		r.encodeForHash(rl.buf, outbuf)
	})
}

// DecodeRLP decodes a list of receipts from the network format.
func (rl *ReceiptList68) DecodeRLP(s *rlp.Stream) error {
	return rl.items.DecodeRLP(s)
}

// EncodeRLP encodes the list into the network format of eth/68.
func (rl *ReceiptList68) EncodeRLP(w io.Writer) error {
	return rl.items.EncodeRLP(w)
}

// ExcludeStateSync removes the state sync transaction receipt from the list.
func (rl *ReceiptList68) ExcludeStateSyncReceipt() {
	excludeStateSyncReceipt(&rl.items, func(data []byte, r *Receipt) error {
		return r.decode68(data)
	})
}

// ReceiptList69 is the block receipt list as downloaded by eth/69.
// This implements types.DerivableList for validation purposes.
type ReceiptList69 struct {
	buf   *receiptListBuffers
	items rlp.RawList[Receipt]
}

// NewReceiptList69 creates a receipt list.
// This is slow, and exists for testing purposes.
func NewReceiptList69(trs []*types.Receipt) *ReceiptList69 {
	rl := new(ReceiptList69)
	enc := rlp.NewEncoderBuffer(nil)
	for _, tr := range trs {
		r := newReceipt(tr)
		r.encodeForNetwork69(&enc)
		rl.items.AppendRaw(enc.ToBytes())
		enc.Reset(nil)
	}
	return rl
}

// setBuffers implements ReceiptsList.
func (rl *ReceiptList69) setBuffers(buf *receiptListBuffers) {
	rl.buf = buf
}

// rawItems implements ReceiptsList.
func (rl *ReceiptList69) rawItems() *rlp.RawList[Receipt] {
	return &rl.items
}

// EncodeForStorage encodes the receipts for storage into the database.
func (rl *ReceiptList69) EncodeForStorage() (rlp.RawValue, error) {
	initBuffers(&rl.buf)
	return rl.buf.encodeForStorage(rl.items, func(data []byte, r *Receipt) error {
		return r.decode69(data)
	})
}

// Derivable turns the receipts into a list that can derive the root hash.
func (rl *ReceiptList69) Derivable() types.DerivableList {
	initBuffers(&rl.buf)
	return newDerivableRawList(&rl.items, func(data []byte, outbuf *bytes.Buffer) {
		var r Receipt
		if r.decode69(data) != nil {
			// Write invalid data to corrupt the receipt root
			outbuf.WriteByte(0x00)
			return
		}
		r.encodeForHash(rl.buf, outbuf)
	})
}

// DecodeRLP decodes a list receipts from the network format.
func (rl *ReceiptList69) DecodeRLP(s *rlp.Stream) error {
	return rl.items.DecodeRLP(s)
}

// EncodeRLP encodes the list into the network format of eth/69.
func (rl *ReceiptList69) EncodeRLP(w io.Writer) error {
	return rl.items.EncodeRLP(w)
}

// ExcludeStateSync removes the state sync transaction receipt from the list.
func (rl *ReceiptList69) ExcludeStateSyncReceipt() {
	excludeStateSyncReceipt(&rl.items, func(data []byte, r *Receipt) error {
		return r.decode69(data)
	})
}

// Append appends all items from another receipt list to this list. It is used by
// eth/70 to reassemble a block whose receipts were split across several packets.
func (rl *ReceiptList69) Append(other *ReceiptList69) {
	rl.items.AppendList(&other.items)
}

// LogsSize returns the total size of log data across all receipts of the list.
func (rl *ReceiptList69) LogsSize() (uint64, error) {
	var size uint64
	it := rl.items.ContentIterator()
	for it.Next() {
		// The encoded receipts are of the form:
		//
		//   [txType, status, cumulativeGasUsed, [logs...]]
		//
		// Skip the first three fields and count the inner size of the logs list.
		content, _, err := rlp.SplitList(it.Value())
		if err != nil {
			return 0, fmt.Errorf("invalid receipt structure: %v", err)
		}
		rest := content
		for range 3 {
			_, _, rest, err = rlp.Split(rest)
			if err != nil {
				return 0, fmt.Errorf("invalid receipt structure: %v", err)
			}
		}
		logsContent, _, err := rlp.SplitList(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid receipt logs: %v", err)
		}
		size += uint64(len(logsContent))
	}
	if it.Err() != nil {
		return 0, fmt.Errorf("bad list: %v", it.Err())
	}
	return size, nil
}

// blockReceiptsToNetwork69 takes a slice of rlp-encoded receipts, and transactions,
// and applies the type-encoding on the receipts (for non-legacy receipts).
// e.g. for non-legacy receipts: receipt-data -> {tx-type || receipt-data}. It also
// handles state-sync transaction receipts and encodes them in the same format.
//
// q bounds the output for eth/70, which may answer with part of a block: receipts before
// q.firstIndex are omitted and encoding stops before q.sizeLimit is exceeded, in which
// case incomplete is set. Its zero value produces the whole block, as eth/68 and eth/69
// always require.
//
// The loop index stays absolute even when receipts are skipped, so isStateSyncReceipt
// keeps identifying the state-sync receipt by its position within the whole block rather
// than within the chunk being encoded. The tx-type iterator is advanced in lockstep for
// the same reason.
func blockReceiptsToNetwork69(blockReceipts, blockBody rlp.RawValue, isStateSyncReceipt func(index int) bool, q receiptQueryParams) (output []byte, incomplete bool, err error) {
	txTypesIter, err := txTypesInBody(blockBody)
	if err != nil {
		return nil, false, fmt.Errorf("invalid block body: %v", err)
	}
	nextTxType, stopTxTypes := iter.Pull(txTypesIter)
	defer stopTxTypes()

	var (
		out bytes.Buffer
		enc = rlp.NewEncoderBuffer(&out)
	)
	it, err := rlp.NewListIterator(blockReceipts)
	if err != nil {
		if len(blockReceipts) == 0 {
			it, err = rlp.NewListIterator(rlp.EmptyList)
		}
		if err != nil {
			return nil, false, fmt.Errorf("invalid block receipts: %w", err)
		}
	}
	outer := enc.List()
	for i := 0; it.Next(); i++ {
		// TxType is always 0 for state-sync transactions before Madhugiri hardfork.
		// Post Madhugiri HF, they will be part of normal block receipts and body so no special
		// handling needed.
		var txType uint64
		if !isStateSyncReceipt(i) {
			t, ok := nextTxType()
			if !ok {
				return nil, false, fmt.Errorf("block has less txs than receipts (%d)", i)
			}
			txType = uint64(t)
		}
		if uint64(i) < q.firstIndex {
			continue
		}
		content, _, _ := rlp.SplitList(it.Value())
		// The txType is encoded as a single byte, which EIP-2718 guarantees by
		// disallowing tx types above 0x7f.
		size := rlp.ListSize(1 + uint64(len(content)))
		if q.sizeLimit > 0 && uint64(enc.Size())+size > q.sizeLimit {
			if uint64(i) == q.firstIndex {
				// Not even the first requested receipt fits, so there is no progress
				// to be made on this block.
				return nil, false, nil
			}
			incomplete = true
			break
		}
		receiptList := enc.List()
		enc.WriteUint64(txType)
		enc.Write(content)
		enc.ListEnd(receiptList)
	}
	enc.ListEnd(outer)
	enc.Flush()
	return out.Bytes(), incomplete, nil
}

// receiptQueryParams bounds an eth/70 receipt response. Receipts positioned before
// firstIndex are omitted, and encoding stops before the response would grow past
// sizeLimit. A zero sizeLimit means unbounded.
type receiptQueryParams struct {
	firstIndex uint64
	sizeLimit  uint64
}

// txTypesInBody parses the transactions list of an encoded block body, returning just the types.
func txTypesInBody(body rlp.RawValue) (iter.Seq[byte], error) {
	bodyFields, _, err := rlp.SplitList(body)
	if err != nil {
		return nil, err
	}
	txsIter, err := rlp.NewListIterator(bodyFields)
	if err != nil {
		return nil, err
	}
	return func(yield func(byte) bool) {
		for txsIter.Next() {
			var txType byte
			switch k, content, _, _ := rlp.Split(txsIter.Value()); k {
			case rlp.List:
				txType = 0
			case rlp.String:
				if len(content) > 0 {
					txType = content[0]
				}
			}
			if !yield(txType) {
				return
			}
		}
	}, nil
}
