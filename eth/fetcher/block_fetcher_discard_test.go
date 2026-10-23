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

package fetcher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBlockFetchDiscardReleasesAlternatives(t *testing.T) {
	for _, old := range []bool{false, true} {
		name := "known"
		if old {
			name = "too old"
		}
		t.Run(name, func(t *testing.T) {
			tester := newTester(false)
			f := tester.fetcher
			defer f.Stop()
			hashes, blocks := makeChain(maxUncleDist+4, 83, genesis)
			block := blocks[hashes[len(hashes)-3]]
			resume := pauseBlockTimeoutLoop(t, f)
			f.enqueue("owner", nil, block, nil)
			hash := block.Hash()
			f.completing[hash] = &blockAnnounce{origin: "owner", time: time.Now()}
			f.announced[hash] = []*blockAnnounce{{origin: "alternate", time: time.Now()}}
			f.announces["alternate"] = 1
			tester.lock.Lock()
			head := block
			if old {
				head = blocks[hashes[0]]
			} else {
				tester.blocks[hash] = block
			}
			tester.headers[head.Hash()] = head.Header()
			tester.blocks[head.Hash()] = head
			tester.hashes = append(tester.hashes, head.Hash())
			tester.lock.Unlock()
			resume()
			resume = pauseBlockTimeoutLoop(t, f)
			defer resume()
			require.Nil(t, f.queued[hash])
			require.Nil(t, f.completing[hash])
			require.Nil(t, f.announced[hash])
			require.Empty(t, f.announces)
			require.Empty(t, f.queues)
		})
	}
}
