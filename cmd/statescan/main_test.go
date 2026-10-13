package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/parquet-go/parquet-go"
)

const (
	testAccountIndexSize = common.AddressLength + 13
	testSlotIndexSize    = common.HashLength + 5
	testHistoryMetaSize  = 9 + 2*common.HashLength
)

// encodeMeta mirrors triedb/pathdb meta.encode.
func encodeMeta(version uint8, parent, root common.Hash, block uint64) []byte {
	buf := make([]byte, testHistoryMetaSize)
	buf[0] = version
	copy(buf[1:], parent.Bytes())
	copy(buf[1+common.HashLength:], root.Bytes())
	binary.BigEndian.PutUint64(buf[1+2*common.HashLength:], block)
	return buf
}

// encodeHistory mirrors triedb/pathdb stateHistory.encode for a hand-made
// change set.
func encodeHistory(accounts []accountEntry) (aIdx, sIdx, aData, sData []byte) {
	var slotNumber uint32
	for _, acc := range accounts {
		accOff := uint32(len(aData))
		accLen := uint8(len(acc.data))
		aData = append(aData, acc.data...)

		idx := make([]byte, testAccountIndexSize)
		copy(idx, acc.address.Bytes())
		idx[common.AddressLength] = accLen
		binary.BigEndian.PutUint32(idx[common.AddressLength+1:], accOff)
		binary.BigEndian.PutUint32(idx[common.AddressLength+5:], slotNumber)
		binary.BigEndian.PutUint32(idx[common.AddressLength+9:], uint32(len(acc.slots)))
		aIdx = append(aIdx, idx...)

		for _, s := range acc.slots {
			sOff := uint32(len(sData))
			sData = append(sData, s.value...)
			si := make([]byte, testSlotIndexSize)
			copy(si, s.key.Bytes())
			si[common.HashLength] = uint8(len(s.value))
			binary.BigEndian.PutUint32(si[common.HashLength+1:], sOff)
			sIdx = append(sIdx, si...)
			slotNumber++
		}
	}
	return aIdx, sIdx, aData, sData
}

// buildFreezer writes two state history records through the real rawdb state
// freezer so the reader is tested against genuine freezer file layout
// (snappy compression, index files, item numbering).
func buildFreezer(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	f, err := rawdb.NewStateFreezer(base, false, false)
	if err != nil {
		t.Fatalf("NewStateFreezer: %v", err)
	}

	addrA := common.BytesToAddress([]byte{0xaa})
	addrB := common.BytesToAddress([]byte{0xbb})
	slot1 := common.BytesToHash([]byte{0x01})
	slot2 := common.BytesToHash([]byte{0x02})

	accBlob := types.SlimAccountRLP(types.StateAccount{Nonce: 7, Balance: new(uint256.Int)})

	// Record 1, block 100: account A exists with two slots (one with a value,
	// one previously zero); account B is created by this block.
	rec1 := []accountEntry{
		{address: addrA, data: accBlob, slots: []slotEntry{
			{key: slot1, value: []byte{1, 2, 3}},
			{key: slot2, value: nil}, // previously zero
		}},
		{address: addrB, data: nil, slots: nil}, // created by this block
	}
	// Record 2, block 101: account A touched again.
	rec2 := []accountEntry{
		{address: addrA, data: accBlob, slots: []slotEntry{
			{key: slot1, value: []byte{9}},
		}},
	}

	for i, rec := range [][]accountEntry{rec1, rec2} {
		id := uint64(i + 1)
		block := uint64(100 + i)
		aIdx, sIdx, aData, sData := encodeHistory(rec)
		meta := encodeMeta(stateHistoryV1, common.Hash{byte(0x10 + i)}, common.Hash{byte(0x20 + i)}, block)
		if err := rawdb.WriteStateHistory(f, id, meta, aIdx, sIdx, aData, sData); err != nil {
			t.Fatalf("WriteStateHistory %d: %v", id, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close freezer: %v", err)
	}
	return filepath.Join(base, "state")
}

func TestScanRecords(t *testing.T) {
	dir := buildFreezer(t)
	out := filepath.Join(t.TempDir(), "s")
	o := options{ancient: dir, out: out, format: "tsv", summary: true, quiet: true, workers: 2, chunk: 1}
	if err := run(o, os.Stderr); err != nil {
		t.Fatalf("run: %v", err)
	}

	recs, err := os.ReadFile(out + ".records.tsv")
	if err != nil {
		t.Fatal(err)
	}
	accs, err := os.ReadFile(out + ".accounts.tsv")
	if err != nil {
		t.Fatal(err)
	}
	slots, err := os.ReadFile(out + ".slots.tsv")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(recs, []byte("1\t100\t")) || !bytes.Contains(recs, []byte("2\t101\t")) {
		t.Errorf("records.tsv missing expected rows:\n%s", recs)
	}
	if !bytes.Contains(recs, []byte("\t2\t2\t")) {
		t.Errorf("record 1 should have n_accounts=2 n_slots=2:\n%s", recs)
	}
	if !bytes.Contains(accs, []byte("\t0\t")) {
		t.Errorf("expected a present=0 (created) account row:\n%s", accs)
	}
	if !bytes.Contains(accs, []byte("\t7\t0\t")) {
		t.Errorf("expected nonce=7 balance=0 for account A:\n%s", accs)
	}
	if !bytes.Contains(slots, []byte("	0x0000000000000000000000000000000000000000000000000000000000000001	0x010203	3\n")) {
		t.Errorf("slots.tsv missing slot1 row:\n%s", slots)
	}
	if !bytes.Contains(slots, []byte("	0x0000000000000000000000000000000000000000000000000000000000000002		0\n")) {
		t.Errorf("slots.tsv missing previously-zero slot2 row:\n%s", slots)
	}
	if got := bytes.Count(slots, []byte("\n")) - 1; got != 3 {
		t.Errorf("slots.tsv has %d data rows, want 3:\n%s", got, slots)
	}
}

func TestScanParquetAndJson(t *testing.T) {
	dir := buildFreezer(t)
	out := filepath.Join(t.TempDir(), "s")
	o := options{ancient: dir, out: out, format: "parquet", quiet: true, workers: 2, chunk: 2}
	if err := run(o, os.Stderr); err != nil {
		t.Fatalf("run(parquet): %v", err)
	}

	// Read the parquet files back through the parquet reader to prove they
	// are well-formed for external consumers.
	f, err := os.Open(out + ".slots.parquet")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := parquet.NewGenericReader[slotRow](f)
	var rows []slotRow
	for {
		buf := make([]slotRow, 4)
		n, err := r.Read(buf)
		rows = append(rows, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parquet readback: %v", err)
		}
	}
	r.Close()
	if len(rows) != 3 {
		t.Errorf("parquet readback got %d slot rows, want 3", len(rows))
	}

	outJ := filepath.Join(t.TempDir(), "s")
	o.format = "json"
	o.out = outJ
	if err := run(o, os.Stderr); err != nil {
		t.Fatalf("run(json): %v", err)
	}
	for _, kind := range []string{"records", "accounts", "slots"} {
		st, err := os.Stat(outJ + "." + kind + ".jsonl")
		if err != nil || st.Size() == 0 {
			t.Errorf("json %s output missing or empty: %v", kind, err)
		}
	}
}

func TestDecodeRecordRejectsTruncated(t *testing.T) {
	if _, err := decodeRecord(1, []byte{1}, nil, nil, nil, nil); err == nil {
		t.Error("expected error for empty account index")
	}
}
