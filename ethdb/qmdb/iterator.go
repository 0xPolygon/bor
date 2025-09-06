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

package qmdb

import (
	"errors"

	"github.com/ethereum/go-ethereum/ethdb"
)

// Iterator represents a minimal iterator that doesn't support iteration
// This is due to QMDB's lack of native iteration support
type Iterator struct {
	err error
}

// NewIterator creates a new iterator over a subset of database content
func (db *Database) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	return &Iterator{
		err: errors.New("iteration not supported by QMDB"),
	}
}

// Next moves the iterator to the next key/value pair
func (iter *Iterator) Next() bool {
	return false
}

// Error returns any accumulated error
func (iter *Iterator) Error() error {
	return iter.err
}

// Key returns the key of the current key/value pair
func (iter *Iterator) Key() []byte {
	return nil
}

// Value returns the value of the current key/value pair
func (iter *Iterator) Value() []byte {
	return nil
}

// Release releases associated resources
func (iter *Iterator) Release() {
	// Nothing to release
}