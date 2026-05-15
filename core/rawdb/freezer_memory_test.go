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

package rawdb

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb/ancienttest"
	"github.com/ethereum/go-ethereum/ethdb"
)

func TestMemoryFreezer(t *testing.T) {
	ancienttest.TestAncientSuite(t, func(kinds []string) ethdb.AncientStore {
		tables := make(map[string]freezerTableConfig)
		for _, kind := range kinds {
			tables[kind] = freezerTableConfig{
				noSnappy: true,
				prunable: true,
			}
		}
		return NewMemoryFreezer(false, 0, tables)
	})
	ancienttest.TestResettableAncientSuite(t, func(kinds []string) ethdb.ResettableAncientStore {
		tables := make(map[string]freezerTableConfig)
		for _, kind := range kinds {
			tables[kind] = freezerTableConfig{
				noSnappy: true,
				prunable: true,
			}
		}
		return NewMemoryFreezer(false, 0, tables)
	})
}

func TestMemoryFreezerOffset(t *testing.T) {
	freezer := NewMemoryFreezer(false, 10, map[string]freezerTableConfig{
		"test": {noSnappy: true, prunable: true},
	})
	if got := freezer.AncientOffSet(); got != 10 {
		t.Fatalf("AncientOffSet() = %d, want 10", got)
	}
	if got, err := freezer.Ancients(); err != nil {
		t.Fatalf("Ancients() returned error: %v", err)
	} else if got != 10 {
		t.Fatalf("Ancients() = %d, want 10", got)
	}
	if got, err := freezer.ItemAmountInAncient(); err != nil {
		t.Fatalf("ItemAmountInAncient() returned error: %v", err)
	} else if got != 0 {
		t.Fatalf("ItemAmountInAncient() = %d, want 0", got)
	}

	if _, err := freezer.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		return op.AppendRaw("test", 0, []byte("zero"))
	}); !errors.Is(err, errOutOrderInsertion) {
		t.Fatalf("AppendRaw before offset error = %v, want %v", err, errOutOrderInsertion)
	}

	if _, err := freezer.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		if err := op.AppendRaw("test", 10, []byte("ten")); err != nil {
			return err
		}
		if err := op.AppendRaw("test", 11, []byte("eleven")); err != nil {
			return err
		}
		return op.AppendRaw("test", 12, []byte("twelve"))
	}); err != nil {
		t.Fatalf("ModifyAncients() returned error: %v", err)
	}

	if got, err := freezer.Ancients(); err != nil {
		t.Fatalf("Ancients() returned error: %v", err)
	} else if got != 13 {
		t.Fatalf("Ancients() = %d, want 13", got)
	}
	if got, err := freezer.ItemAmountInAncient(); err != nil {
		t.Fatalf("ItemAmountInAncient() returned error: %v", err)
	} else if got != 3 {
		t.Fatalf("ItemAmountInAncient() = %d, want 3", got)
	}

	if blob, err := freezer.Ancient("test", 10); err != nil {
		t.Fatalf("Ancient(10) returned error: %v", err)
	} else if !bytes.Equal(blob, []byte("ten")) {
		t.Fatalf("Ancient(10) = %q, want %q", blob, []byte("ten"))
	}
	if _, err := freezer.Ancient("test", 9); !errors.Is(err, errOutOfBounds) {
		t.Fatalf("Ancient(9) error = %v, want %v", err, errOutOfBounds)
	}

	if batch, err := freezer.AncientRange("test", 10, 2, 0); err != nil {
		t.Fatalf("AncientRange(10, 2, 0) returned error: %v", err)
	} else if len(batch) != 2 || !bytes.Equal(batch[0], []byte("ten")) || !bytes.Equal(batch[1], []byte("eleven")) {
		t.Fatalf("AncientRange(10, 2, 0) = %q, want %q", batch, [][]byte{[]byte("ten"), []byte("eleven")})
	}
	if _, err := freezer.AncientRange("test", 9, 1, 0); !errors.Is(err, errOutOfBounds) {
		t.Fatalf("AncientRange(9, 1, 0) error = %v, want %v", err, errOutOfBounds)
	}

	if blob, err := freezer.AncientBytes("test", 12, 0, uint64(len("twelve"))); err != nil {
		t.Fatalf("AncientBytes(12) returned error: %v", err)
	} else if !bytes.Equal(blob, []byte("twelve")) {
		t.Fatalf("AncientBytes(12) = %q, want %q", blob, []byte("twelve"))
	}
	if _, err := freezer.AncientBytes("test", 2, 0, 1); !errors.Is(err, errOutOfBounds) {
		t.Fatalf("AncientBytes(2) error = %v, want %v", err, errOutOfBounds)
	}

	if _, err := freezer.TruncateHead(9); !errors.Is(err, errTruncateBelowOffset) {
		t.Fatalf("TruncateHead(9) error = %v, want %v", err, errTruncateBelowOffset)
	}
	if _, err := freezer.TruncateTail(9); !errors.Is(err, errTruncateBelowOffset) {
		t.Fatalf("TruncateTail(9) error = %v, want %v", err, errTruncateBelowOffset)
	}

	if old, err := freezer.TruncateTail(11); err != nil {
		t.Fatalf("TruncateTail(11) returned error: %v", err)
	} else if old != 0 {
		t.Fatalf("TruncateTail(11) old tail = %d, want 0", old)
	}
	if blob, err := freezer.Ancient("test", 11); err != nil {
		t.Fatalf("Ancient(11) returned error: %v", err)
	} else if !bytes.Equal(blob, []byte("eleven")) {
		t.Fatalf("Ancient(11) = %q, want %q", blob, []byte("eleven"))
	}
	if _, err := freezer.Ancient("test", 10); !errors.Is(err, errOutOfBounds) {
		t.Fatalf("Ancient(10) after tail truncation error = %v, want %v", err, errOutOfBounds)
	}

	if old, err := freezer.TruncateHead(12); err != nil {
		t.Fatalf("TruncateHead(12) returned error: %v", err)
	} else if old != 13 {
		t.Fatalf("TruncateHead(12) old head = %d, want 13", old)
	}
	if got, err := freezer.Ancients(); err != nil {
		t.Fatalf("Ancients() after head truncation returned error: %v", err)
	} else if got != 12 {
		t.Fatalf("Ancients() after head truncation = %d, want 12", got)
	}
	if _, err := freezer.Ancient("test", 12); !errors.Is(err, errOutOfBounds) {
		t.Fatalf("Ancient(12) after head truncation error = %v, want %v", err, errOutOfBounds)
	}
}

func TestMemoryFreezerResetKeepsOffset(t *testing.T) {
	freezer := NewMemoryFreezer(false, 7, map[string]freezerTableConfig{
		"test": {noSnappy: true, prunable: true},
	})
	if err := freezer.Reset(); err != nil {
		t.Fatalf("Reset() returned error: %v", err)
	}
	if got, err := freezer.Ancients(); err != nil {
		t.Fatalf("Ancients() returned error: %v", err)
	} else if got != 7 {
		t.Fatalf("Ancients() = %d, want 7", got)
	}
	if got, err := freezer.ItemAmountInAncient(); err != nil {
		t.Fatalf("ItemAmountInAncient() returned error: %v", err)
	} else if got != 0 {
		t.Fatalf("ItemAmountInAncient() = %d, want 0", got)
	}
	if got, err := freezer.Tail(); err != nil {
		t.Fatalf("Tail() returned error: %v", err)
	} else if got != 7 {
		t.Fatalf("Tail() = %d, want 7", got)
	}
	if _, err := freezer.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		return op.AppendRaw("test", 7, []byte("seven"))
	}); err != nil {
		t.Fatalf("ModifyAncients() after reset returned error: %v", err)
	}
}
