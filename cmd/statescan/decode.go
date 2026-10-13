// State history record decoding. Mirrors the on-disk layout of
// triedb/pathdb/history_state.go, which is the authoritative encoder.
//
// Layout notes:
//   - Freezer item position p holds state history ID p+1 (IDs start at 1).
//   - meta: version byte, parent root (32B), post root (32B), block number
//     (big-endian 8B). history.meta is the only uncompressed table.
//   - account index: fixed 33B entries (address, data length, data offset,
//     storage-offset, slot-count), sorted by address.
//   - storage index: fixed 37B entries (slot key, value length, value offset).
//   - account data: concatenated slim-RLP account blobs; an empty blob means
//     the account did not exist before the block (i.e. it was created by it).
//   - storage data: concatenated slot values; an empty value means the slot
//     was zero/nonexistent before the block.
//
// The history object is a REVERSE DIFF: every value stored is the pre-state
// (parent) value of a key the block changed. The meta root is the post-state
// root.
package main

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

const (
	accountIndexSize = common.AddressLength + 13
	slotIndexSize    = common.HashLength + 5
	historyMetaSize  = 9 + 2*common.HashLength

	stateHistoryV0 = uint8(0) // slot key is the keccak hash of the raw key
	stateHistoryV1 = uint8(1) // slot key is the raw storage key
)

type historyMeta struct {
	version uint8
	parent  common.Hash
	root    common.Hash
	block   uint64
}

func decodeHistoryMeta(blob []byte) (historyMeta, error) {
	var m historyMeta
	if len(blob) < 1 {
		return m, fmt.Errorf("no version tag")
	}
	switch blob[0] {
	case stateHistoryV0, stateHistoryV1:
		if len(blob) != historyMetaSize {
			return m, fmt.Errorf("invalid meta length %d", len(blob))
		}
		m.version = blob[0]
		m.parent = common.BytesToHash(blob[1 : 1+common.HashLength])
		m.root = common.BytesToHash(blob[1+common.HashLength : 1+2*common.HashLength])
		m.block = binary.BigEndian.Uint64(blob[1+2*common.HashLength : historyMetaSize])
		return m, nil
	default:
		return m, fmt.Errorf("unknown history version %d", blob[0])
	}
}

type slotEntry struct {
	key   common.Hash // raw key (v1) or keccak of raw key (v0)
	value []byte      // pre-state value; empty means the slot was previously zero
}

type accountEntry struct {
	address common.Address
	data    []byte // pre-state slim RLP; empty means the account was created by this block
	slots   []slotEntry
}

// chunk is a batch of decoded records handed from a producer worker to the
// emitter goroutine.
type chunk struct{ recs []*record }

// job identifies one chunk to produce: its index in the ordered chunk list
// and the state history ID it starts at.
type job struct {
	idx     int
	startID uint64
}

type record struct {
	id        uint64 // state history ID (freezer position + 1)
	meta      historyMeta
	accounts  []accountEntry
	dataBytes int // raw byte size of the account and storage data streams
}

// decodeRecord parses the five streams of one state history record. The
// account index is the outer structure; each account entry owns a contiguous
// range of storage index entries.
func decodeRecord(id uint64, mData, aIdx, sIdx, aData, sData []byte) (*record, error) {
	meta, err := decodeHistoryMeta(mData)
	if err != nil {
		return nil, fmt.Errorf("id %d: %w", id, err)
	}
	if len(aIdx)%accountIndexSize != 0 || len(aIdx) == 0 {
		return nil, fmt.Errorf("id %d: invalid account index length %d", id, len(aIdx))
	}
	if len(sIdx)%slotIndexSize != 0 {
		return nil, fmt.Errorf("id %d: invalid storage index length %d", id, len(sIdx))
	}
	rec := &record{id: id, meta: meta}
	n := len(aIdx) / accountIndexSize
	rec.accounts = make([]accountEntry, 0, n)
	for i := 0; i < n; i++ {
		e := accountIndexSize * i
		entry := accountEntry{
			address: common.BytesToAddress(aIdx[e : e+common.AddressLength]),
		}
		dataLen := int(aIdx[e+common.AddressLength])
		dataOff := int(binary.BigEndian.Uint32(aIdx[e+common.AddressLength+1:]))
		slotOff := int(binary.BigEndian.Uint32(aIdx[e+common.AddressLength+5:]))
		slotCnt := int(binary.BigEndian.Uint32(aIdx[e+common.AddressLength+9:]))

		end := dataOff + dataLen
		if end > len(aData) {
			return nil, fmt.Errorf("id %d: account %x data out of range", id, entry.address)
		}
		entry.data = aData[dataOff:end]

		if slotOff+slotCnt > len(sIdx)/slotIndexSize {
			return nil, fmt.Errorf("id %d: account %x slot index out of range", id, entry.address)
		}
		if slotCnt > 0 {
			entry.slots = make([]slotEntry, 0, slotCnt)
			for j := 0; j < slotCnt; j++ {
				s := (slotOff + j) * slotIndexSize
				se := slotEntry{key: common.BytesToHash(sIdx[s : s+common.HashLength])}
				vLen := int(sIdx[s+common.HashLength])
				vOff := int(binary.BigEndian.Uint32(sIdx[s+common.HashLength+1:]))
				vEnd := vOff + vLen
				if vEnd > len(sData) {
					return nil, fmt.Errorf("id %d: account %x slot %x value out of range", id, entry.address, se.key)
				}
				se.value = sData[vOff:vEnd]
				entry.slots = append(entry.slots, se)
			}
		}
		rec.accounts = append(rec.accounts, entry)
	}
	rec.dataBytes = len(aData) + len(sData)
	return rec, nil
}
