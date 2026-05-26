// Copyright 2025 The go-ethereum Authors
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
	"testing"

	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
)

// TestProtocolsSnapServing verifies that the snap/1 protocol is included or
// excluded from the advertised protocol list based on SnapshotCache and
// NoSnapServing config fields.
func TestProtocolsSnapServing(t *testing.T) {
	th := newTestHandlerWithBlocks(0)
	defer th.close()

	tests := []struct {
		name          string
		snapshotCache int
		noSnapServing bool
		wantSnap      bool
	}{
		{
			name:          "snap served when snapshots enabled",
			snapshotCache: 100,
			noSnapServing: false,
			wantSnap:      true,
		},
		{
			name:          "snap not served when SnapshotCache is zero",
			snapshotCache: 0,
			noSnapServing: false,
			wantSnap:      false,
		},
		{
			name:          "snap not served when NoSnapServing is set",
			snapshotCache: 100,
			noSnapServing: true,
			wantSnap:      false,
		},
		{
			name:          "snap not served when both SnapshotCache zero and NoSnapServing set",
			snapshotCache: 0,
			noSnapServing: true,
			wantSnap:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eth := &Ethereum{
				handler:   th.handler,
				networkID: 1,
				config: &ethconfig.Config{
					SnapshotCache: tt.snapshotCache,
					NoSnapServing: tt.noSnapServing,
				},
			}

			protos := eth.Protocols()

			var hasSnap bool
			for _, proto := range protos {
				if proto.Name == snap.ProtocolName {
					hasSnap = true
					break
				}
			}

			if hasSnap != tt.wantSnap {
				t.Errorf("snap protocol advertised = %v, want %v", hasSnap, tt.wantSnap)
			}
		})
	}
}
