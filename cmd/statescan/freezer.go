// Lock-free reader for bor/geth freezer tables.
//
// rawdb.NewStateFreezer takes a shared flock, which fails while a live node
// holds the lock (a second process cannot open the freezer at all, and opening
// it in-process blocks on the lock). Frozen items are immutable once indexed,
// so reading the index and data files directly is safe next to a running node
// as long as only the item count observed at open time is trusted.
//
// Copied from cmd/receiptscan, which established this pattern for the chain
// freezer.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang/snappy"
)

const indexEntrySize = 6

type indexEntry struct {
	filenum uint32
	offset  uint32
}

func (e *indexEntry) unmarshal(b []byte) {
	e.filenum = uint32(binary.BigEndian.Uint16(b[:2]))
	e.offset = binary.BigEndian.Uint32(b[2:6])
}

// bounds mirrors rawdb.indexEntry.bounds: an item whose start and end entries
// name different files lives wholly in the second file, starting at offset 0.
func (e *indexEntry) bounds(end *indexEntry) (start, stop, file uint32) {
	if e.filenum != end.filenum {
		return 0, end.offset, end.filenum
	}
	return e.offset, end.offset, e.filenum
}

// table is a read-only view of one freezer table.
type table struct {
	dir        string
	name       string
	compressed bool
	index      *os.File
	itemOffset uint64 // items physically removed from the head of the table
	hidden     uint64 // items logically deleted (virtual tail)
	items      uint64 // first item number NOT present

	mu    sync.Mutex
	files map[uint32]*os.File
}

// openTable opens <dir>/<name>.{cidx|ridx} plus its metadata file.
func openTable(dir, name string, compressed bool) (*table, error) {
	idxName := name + ".ridx"
	if compressed {
		idxName = name + ".cidx"
	}
	idx, err := os.Open(filepath.Join(dir, idxName))
	if err != nil {
		return nil, err
	}
	st, err := idx.Stat()
	if err != nil {
		idx.Close()
		return nil, err
	}
	size := st.Size()

	tail, flushOffset := readMeta(filepath.Join(dir, name+".meta"))
	if flushOffset > 0 && flushOffset < size {
		size = flushOffset
	}
	size -= size % indexEntrySize
	if size < 2*indexEntrySize {
		idx.Close()
		return nil, fmt.Errorf("table %s: index too short (%d bytes)", name, size)
	}

	var first indexEntry
	buf := make([]byte, indexEntrySize)
	if _, err := idx.ReadAt(buf, 0); err != nil {
		idx.Close()
		return nil, err
	}
	first.unmarshal(buf)

	t := &table{
		dir:        dir,
		name:       name,
		compressed: compressed,
		index:      idx,
		itemOffset: uint64(first.offset),
		hidden:     tail,
		files:      make(map[uint32]*os.File),
	}
	// Entry 0 is the deletion marker; entries 1..n mark the end of items 0..n-1.
	t.items = t.itemOffset + uint64(size/indexEntrySize) - 1
	if t.hidden < t.itemOffset {
		t.hidden = t.itemOffset
	}
	return t, nil
}

// readMeta returns (virtualTail, flushOffset). Missing or unreadable metadata
// yields zeros, meaning "trust the whole index".
func readMeta(path string) (uint64, int64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var o struct {
		Version uint16
		Tail    uint64
		Offset  uint64
	}
	if err := rlp.Decode(f, &o); err != nil || o.Version != 2 {
		return 0, 0
	}
	return o.Tail, int64(o.Offset)
}

func (t *table) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, f := range t.files {
		f.Close()
	}
	return t.index.Close()
}

func (t *table) file(num uint32) (*os.File, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f, ok := t.files[num]; ok {
		return f, nil
	}
	ext := ".rdat"
	if t.compressed {
		ext = ".cdat"
	}
	f, err := os.Open(filepath.Join(t.dir, fmt.Sprintf("%s.%04d%s", t.name, num, ext)))
	if err != nil {
		return nil, err
	}
	t.files[num] = f
	return f, nil
}

// Retrieve returns items [from, from+count). Empty items are zero-length
// slices. Safe for concurrent use: os.File.ReadAt is a positional pread.
func (t *table) Retrieve(from, count uint64) ([][]byte, error) {
	if from < t.hidden || from+count > t.items {
		return nil, fmt.Errorf("table %s: range [%d,%d) outside [%d,%d)", t.name, from, from+count, t.hidden, t.items)
	}
	rel := from - t.itemOffset
	buf := make([]byte, (count+1)*indexEntrySize)
	if _, err := t.index.ReadAt(buf, int64(rel*indexEntrySize)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	entries := make([]indexEntry, count+1)
	for i := range entries {
		entries[i].unmarshal(buf[i*indexEntrySize:])
	}
	if rel == 0 {
		entries[0].offset = 0
		entries[0].filenum = entries[1].filenum
	}

	out := make([][]byte, count)
	for i := uint64(0); i < count; i++ {
		start, stop, num := entries[i].bounds(&entries[i+1])
		if stop < start {
			return nil, fmt.Errorf("table %s: item %d has negative length", t.name, from+i)
		}
		if stop == start {
			out[i] = []byte{}
			continue
		}
		f, err := t.file(num)
		if err != nil {
			return nil, err
		}
		raw := make([]byte, stop-start)
		if _, err := f.ReadAt(raw, int64(start)); err != nil {
			return nil, fmt.Errorf("table %s: item %d: %w", t.name, from+i, err)
		}
		if t.compressed {
			dec, err := snappy.Decode(nil, raw)
			if err != nil {
				return nil, fmt.Errorf("table %s: item %d: snappy: %w", t.name, from+i, err)
			}
			raw = dec
		}
		out[i] = raw
	}
	return out, nil
}
