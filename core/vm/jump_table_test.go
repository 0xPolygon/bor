// Copyright 2016 The go-ethereum Authors
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

package vm

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJumpTableCopy tests that deep copy is necessary to prevent modify shared jump table
func TestJumpTableCopy(t *testing.T) {
	t.Parallel()

	tbl := newMergeInstructionSet()
	require.Equal(t, uint64(0), tbl[SLOAD].constantGas)

	// a deep copy won't modify the shared jump table
	deepCopy := copyJumpTable(&tbl)
	deepCopy[SLOAD].constantGas = 100
	require.Equal(t, uint64(100), deepCopy[SLOAD].constantGas)
	require.Equal(t, uint64(0), tbl[SLOAD].constantGas)
}

// TestLisovoProMatchesLisovo verifies that lisovoPro instruction set is identical to lisovo
func TestLisovoProMatchesLisovo(t *testing.T) {
	t.Parallel()

	lisovo := newLisovoInstructionSet()
	lisovoPro := newLisovoProInstructionSet()

	// Compare all 256 operations in the jump table
	for i := 0; i < 256; i++ {
		opLisovo := lisovo[i]
		opLisovoPro := lisovoPro[i]

		// Both should be non-nil
		require.NotNil(t, opLisovo, "lisovo operation at index %d is nil", i)
		require.NotNil(t, opLisovoPro, "lisovoPro operation at index %d is nil", i)

		// Compare all fields
		require.Equal(t, opLisovo.constantGas, opLisovoPro.constantGas,
			"constantGas mismatch at opcode %#x", i)
		require.Equal(t, opLisovo.minStack, opLisovoPro.minStack,
			"minStack mismatch at opcode %#x", i)
		require.Equal(t, opLisovo.maxStack, opLisovoPro.maxStack,
			"maxStack mismatch at opcode %#x", i)
		require.Equal(t, opLisovo.undefined, opLisovoPro.undefined,
			"undefined mismatch at opcode %#x", i)

		// Compare function pointers using reflection
		require.Equal(t, reflect.ValueOf(opLisovo.execute).Pointer(),
			reflect.ValueOf(opLisovoPro.execute).Pointer(),
			"execute function mismatch at opcode %#x", i)

		// Compare dynamicGas (can be nil)
		if opLisovo.dynamicGas == nil && opLisovoPro.dynamicGas == nil {
			// Both nil, ok
		} else if opLisovo.dynamicGas == nil || opLisovoPro.dynamicGas == nil {
			t.Errorf("dynamicGas nil mismatch at opcode %#x: lisovo=%v, lisovoPro=%v",
				i, opLisovo.dynamicGas == nil, opLisovoPro.dynamicGas == nil)
		} else {
			require.Equal(t, reflect.ValueOf(opLisovo.dynamicGas).Pointer(),
				reflect.ValueOf(opLisovoPro.dynamicGas).Pointer(),
				"dynamicGas function mismatch at opcode %#x", i)
		}

		// Compare memorySize (can be nil)
		if opLisovo.memorySize == nil && opLisovoPro.memorySize == nil {
			// Both nil, ok
		} else if opLisovo.memorySize == nil || opLisovoPro.memorySize == nil {
			t.Errorf("memorySize nil mismatch at opcode %#x: lisovo=%v, lisovoPro=%v",
				i, opLisovo.memorySize == nil, opLisovoPro.memorySize == nil)
		} else {
			require.Equal(t, reflect.ValueOf(opLisovo.memorySize).Pointer(),
				reflect.ValueOf(opLisovoPro.memorySize).Pointer(),
				"memorySize function mismatch at opcode %#x", i)
		}
	}
}
