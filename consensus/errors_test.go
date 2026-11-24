// Copyright 2017 The go-ethereum Authors
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

package consensus

import "testing"

func TestEngineStoppedErrorMessageWithoutCause(t *testing.T) {
	tests := []struct {
		name string
		err  *EngineStoppedError
	}{
		{name: "nil receiver"},
		{name: "nil cause", err: &EngineStoppedError{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != ErrEngineStopped.Error() {
				t.Fatalf("error message = %q, want %q", got, ErrEngineStopped)
			}
		})
	}
}
