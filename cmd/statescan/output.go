// Row writers for the three output formats. All writers are single-threaded;
// the emitter goroutine is their only caller.
//
// Three row kinds are emitted, one file each ("<out>.records", "<out>.accounts",
// "<out>.slots" plus the format extension):
//   - records:  one row per state history record
//   - accounts: one row per (record, modified account)
//   - slots:    one row per (record, account, modified storage slot)
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/parquet-go/parquet-go"
)

// recordRow is the flat representation of one history record.
type recordRow struct {
	StateID    uint64 `json:"state_id" parquet:"state_id"`
	Block      uint64 `json:"block" parquet:"block"`
	ParentRoot string `json:"parent_root" parquet:"parent_root"`
	Root       string `json:"root" parquet:"root"`
	NumAcc     int    `json:"n_accounts" parquet:"n_accounts"`
	NumSlots   int    `json:"n_slots" parquet:"n_slots"`
	DataBytes  int    `json:"data_bytes" parquet:"data_bytes"`
}

// accountRow is one modified account. present=false means the account did not
// exist before the block. balance/storage_root/code_hash are the PRE-state
// values (the reverse diff), not the post-state ones.
type accountRow struct {
	StateID     uint64 `json:"state_id" parquet:"state_id"`
	Block       uint64 `json:"block" parquet:"block"`
	Address     string `json:"address" parquet:"address"`
	Present     bool   `json:"present" parquet:"present"`
	Nonce       uint64 `json:"nonce" parquet:"nonce"`
	Balance     string `json:"balance" parquet:"balance"`
	StorageRoot string `json:"storage_root" parquet:"storage_root"`
	CodeHash    string `json:"code_hash" parquet:"code_hash"`
	NumSlots    int    `json:"n_slots" parquet:"n_slots"`
	// DecodeFailed is true when the pre-state account RLP could not be
	// decoded; the touch is kept but the decoded fields are zero.
	DecodeFailed bool `json:"decode_failed" parquet:"decode_failed"`
}

// slotRow is one modified storage slot. value is the PRE-state value; an empty
// value means the slot was zero/nonexistent before the block. slot is the raw
// storage key (v1 history) or its keccak hash (v0 history).
type slotRow struct {
	StateID  uint64 `json:"state_id" parquet:"state_id"`
	Block    uint64 `json:"block" parquet:"block"`
	Address  string `json:"address" parquet:"address"`
	Slot     string `json:"slot" parquet:"slot"`
	Value    string `json:"value" parquet:"value"`
	ValueLen int    `json:"value_len" parquet:"value_len"`
}

func addrHex(a common.Address) string {
	b := make([]byte, 42)
	b[0], b[1] = '0', 'x'
	hex.Encode(b[2:], a[:])
	return string(b)
}

func hashHex(h common.Hash) string {
	b := make([]byte, 66)
	b[0], b[1] = '0', 'x'
	hex.Encode(b[2:], h[:])
	return string(b)
}

func bytesHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return "0x" + hex.EncodeToString(b)
}

// toAccountRow decodes the pre-state slim RLP of an account entry.
func toAccountRow(rec *record, e *accountEntry) accountRow {
	row := accountRow{
		StateID:  rec.id,
		Block:    rec.meta.block,
		Address:  addrHex(e.address),
		NumSlots: len(e.slots),
	}
	if len(e.data) == 0 {
		return row // created by this block: nonce/balance/etc. stay zero
	}
	acc, err := types.FullAccount(e.data)
	if err != nil {
		// Keep the row with present=true so the touch is not lost; fields
		// stay zero. Decode failures are counted in the summary.
		row.Present = true
		row.DecodeFailed = true
		return row
	}
	row.Present = true
	row.Nonce = acc.Nonce
	if acc.Balance != nil {
		row.Balance = acc.Balance.ToBig().String()
	} else {
		row.Balance = "0"
	}
	if acc.Root != types.EmptyRootHash {
		row.StorageRoot = hashHex(acc.Root)
	}
	if string(acc.CodeHash) != string(types.EmptyCodeHash[:]) {
		row.CodeHash = bytesHex(acc.CodeHash)
	}
	return row
}

// ---------------------------------------------------------------------------
// writers

type writerSet interface {
	writeRecord(recordRow)
	writeAccount(accountRow)
	writeSlot(slotRow)
	close() error
}

func openWriters(out, format string) (writerSet, error) {
	switch format {
	case "tsv":
		return newTsvWriters(out)
	case "json":
		return newJsonWriters(out)
	case "parquet":
		return newParquetWriters(out)
	default:
		return nil, fmt.Errorf("unknown format %q (want tsv, json or parquet)", format)
	}
}

func outFile(out, kind, ext string) string { return out + "." + kind + ext }

// ---------------------------------------------------------------------------
// TSV

type tsvFile struct {
	f *os.File
	w *bufio.Writer
}

func newTsvFile(path string, header string) (*tsvFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.WriteString(header + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return &tsvFile{f: f, w: w}, nil
}

func (t *tsvFile) close() error {
	if err := t.w.Flush(); err != nil {
		t.f.Close()
		return err
	}
	return t.f.Close()
}

type tsvWriters struct {
	records, accounts, slots *tsvFile
}

func newTsvWriters(out string) (*tsvWriters, error) {
	records, err := newTsvFile(outFile(out, "records", ".tsv"),
		"state_id\tblock\tparent_root\troot\tn_accounts\tn_slots\tdata_bytes")
	if err != nil {
		return nil, err
	}
	accounts, err := newTsvFile(outFile(out, "accounts", ".tsv"),
		"state_id	block	address	present	nonce	balance	storage_root	code_hash	n_slots	decode_failed")
	if err != nil {
		records.close()
		return nil, err
	}
	slots, err := newTsvFile(outFile(out, "slots", ".tsv"),
		"state_id\tblock\taddress\tslot\tvalue\tvalue_len")
	if err != nil {
		records.close()
		accounts.close()
		return nil, err
	}
	return &tsvWriters{records: records, accounts: accounts, slots: slots}, nil
}

func (w *tsvWriters) close() error {
	for _, t := range []*tsvFile{w.records, w.accounts, w.slots} {
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

func (w *tsvWriters) writeRecord(r recordRow) {
	b := w.records.w
	b.WriteString(strconv.FormatUint(r.StateID, 10))
	b.WriteByte('\t')
	b.WriteString(strconv.FormatUint(r.Block, 10))
	b.WriteByte('\t')
	b.WriteString(r.ParentRoot)
	b.WriteByte('\t')
	b.WriteString(r.Root)
	b.WriteString("\t")
	b.WriteString(strconv.Itoa(r.NumAcc))
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(r.NumSlots))
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(r.DataBytes))
	b.WriteByte('\n')
}

func (w *tsvWriters) writeAccount(r accountRow) {
	b := w.accounts.w
	b.WriteString(strconv.FormatUint(r.StateID, 10))
	b.WriteByte('\t')
	b.WriteString(strconv.FormatUint(r.Block, 10))
	b.WriteByte('\t')
	b.WriteString(r.Address)
	b.WriteByte('\t')
	if r.Present {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('\t')
	b.WriteString(strconv.FormatUint(r.Nonce, 10))
	b.WriteByte('\t')
	b.WriteString(r.Balance)
	b.WriteByte('\t')
	b.WriteString(r.StorageRoot)
	b.WriteByte('\t')
	b.WriteString(r.CodeHash)
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(r.NumSlots))
	b.WriteByte('	')
	if r.DecodeFailed {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('\n')
}

func (w *tsvWriters) writeSlot(r slotRow) {
	b := w.slots.w
	b.WriteString(strconv.FormatUint(r.StateID, 10))
	b.WriteByte('\t')
	b.WriteString(strconv.FormatUint(r.Block, 10))
	b.WriteByte('\t')
	b.WriteString(r.Address)
	b.WriteByte('\t')
	b.WriteString(r.Slot)
	b.WriteByte('\t')
	b.WriteString(r.Value)
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(r.ValueLen))
	b.WriteByte('\n')
}

// ---------------------------------------------------------------------------
// JSON lines

type jsonFile struct {
	f *os.File
	w *bufio.Writer
}

func newJsonFile(path string) (*jsonFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &jsonFile{f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (t *jsonFile) write(v any) error {
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	_, err = t.w.Write(blob)
	return err
}

func (t *jsonFile) close() error {
	if err := t.w.Flush(); err != nil {
		t.f.Close()
		return err
	}
	return t.f.Close()
}

type jsonWriters struct {
	records, accounts, slots *jsonFile
}

func newJsonWriters(out string) (*jsonWriters, error) {
	records, err := newJsonFile(outFile(out, "records", ".jsonl"))
	if err != nil {
		return nil, err
	}
	accounts, err := newJsonFile(outFile(out, "accounts", ".jsonl"))
	if err != nil {
		records.close()
		return nil, err
	}
	slots, err := newJsonFile(outFile(out, "slots", ".jsonl"))
	if err != nil {
		records.close()
		accounts.close()
		return nil, err
	}
	return &jsonWriters{records: records, accounts: accounts, slots: slots}, nil
}

func (w *jsonWriters) close() error {
	for _, t := range []*jsonFile{w.records, w.accounts, w.slots} {
		if err := t.close(); err != nil {
			return err
		}
	}
	return nil
}

func (w *jsonWriters) writeRecord(r recordRow)   { w.records.write(r) }
func (w *jsonWriters) writeAccount(r accountRow) { w.accounts.write(r) }
func (w *jsonWriters) writeSlot(r slotRow)       { w.slots.write(r) }

// ---------------------------------------------------------------------------
// Parquet

type parquetFile[T any] struct {
	f *os.File
	w *parquet.GenericWriter[T]
}

func newParquetFile[T any](path string) (*parquetFile[T], error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := parquet.NewGenericWriter[T](f)
	return &parquetFile[T]{f: f, w: w}, nil
}

func (p *parquetFile[T]) write(row T) error {
	_, err := p.w.Write([]T{row})
	return err
}

func (p *parquetFile[T]) close() error {
	if err := p.w.Close(); err != nil {
		p.f.Close()
		return err
	}
	return p.f.Close()
}

type parquetWriters struct {
	records  *parquetFile[recordRow]
	accounts *parquetFile[accountRow]
	slots    *parquetFile[slotRow]
}

func newParquetWriters(out string) (*parquetWriters, error) {
	records, err := newParquetFile[recordRow](outFile(out, "records", ".parquet"))
	if err != nil {
		return nil, err
	}
	accounts, err := newParquetFile[accountRow](outFile(out, "accounts", ".parquet"))
	if err != nil {
		records.close()
		return nil, err
	}
	slots, err := newParquetFile[slotRow](outFile(out, "slots", ".parquet"))
	if err != nil {
		records.close()
		accounts.close()
		return nil, err
	}
	return &parquetWriters{records: records, accounts: accounts, slots: slots}, nil
}

func (w *parquetWriters) close() error {
	for _, p := range []interface{ close() error }{w.records, w.accounts, w.slots} {
		if err := p.close(); err != nil {
			return err
		}
	}
	return nil
}

func (w *parquetWriters) writeRecord(r recordRow)   { w.records.write(r) }
func (w *parquetWriters) writeAccount(r accountRow) { w.accounts.write(r) }
func (w *parquetWriters) writeSlot(r slotRow)       { w.slots.write(r) }
