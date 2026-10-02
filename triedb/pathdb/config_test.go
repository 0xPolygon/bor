// Copyright 2026 The go-ethereum Authors
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

package pathdb

import "testing"

// TestConfigFieldsAddressCachePersist checks that the startup log fields
// report address cache persistence only when it is enabled.
func TestConfigFieldsAddressCachePersist(t *testing.T) {
	lookup := func(list []interface{}) (interface{}, bool) {
		for i := 0; i+1 < len(list); i += 2 {
			if list[i] == "address-cache-persist" {
				return list[i+1], true
			}
		}
		return nil, false
	}

	if v, ok := lookup((&Config{}).fields()); ok {
		t.Fatalf("expected no address-cache-persist field when disabled, got %v", v)
	}
	v, ok := lookup((&Config{AddressCachePersist: true}).fields())
	if !ok || v != true {
		t.Fatalf("expected address-cache-persist=true when enabled, got %v (present=%v)", v, ok)
	}
}
