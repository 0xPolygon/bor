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
	"math/big"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

type partialBodyRequest struct {
	count int
	at    time.Time
}

func TestBlockFetchPartialSingleSupplier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tester := newTester(false)
		f := tester.fetcher
		defer f.Stop()
		defer f.wm.witnessCache.Stop()
		blocks := make(map[common.Hash]*types.Block)
		parent := genesis
		for i := 1; i <= 3; i++ {
			tx := types.NewTx(&types.LegacyTx{Nonce: uint64(i), Data: make([]byte, 1024*1024)})
			block := types.NewBlock(&types.Header{Number: big.NewInt(int64(i)), ParentHash: parent.Hash()}, &types.Body{Transactions: []*types.Transaction{tx}}, nil, trie.NewStackTrie(nil))
			blocks[block.Hash()] = block
			parent = block
		}
		requests := make(chan partialBodyRequest, 4)
		imported := make(chan common.Hash, 3)
		f.importedHook = func(_ *types.Header, b *types.Block) { imported <- b.Hash() }
		fetch := limitedBodyFetcher(t, blocks, requests)
		for hash, block := range blocks {
			require.NoError(t, f.Notify("only-peer", hash, block.NumberU64(), time.Now(), tester.makeHeaderFetcher("only-peer", blocks, 0), fetch, nil))
		}
		for range 3 {
			select {
			case <-imported:
			case <-time.After(3 * time.Second):
				t.Fatal("partial response stranded a block without another announcement")
			}
		}
		first, retry := <-requests, <-requests
		require.Equal(t, 3, first.count)
		require.Equal(t, 1, retry.count)
		require.GreaterOrEqual(t, retry.at.Sub(first.at), fetchRetryInterval)
		require.Empty(t, requests)
		resume := pauseBlockTimeoutLoop(t, f)
		defer resume()
		require.Empty(t, f.fetched)
		require.Empty(t, f.announced)
		require.Empty(t, f.announces)
		require.Zero(t, f.fetchSlotsTotal)
	})
}

func limitedBodyFetcher(t *testing.T, blocks map[common.Hash]*types.Block, requests chan<- partialBodyRequest) bodyRequesterFn {
	t.Helper()
	return func(hashes []common.Hash, sink chan *ethproto.Response) (*ethproto.Request, error) {
		requests <- partialBodyRequest{count: len(hashes), at: time.Now()}
		var bodies ethproto.BlockBodiesResponse
		size := 0
		for _, hash := range hashes {
			// Match ServiceGetBlockBodiesQuery's soft response limit.
			if size >= 2*1024*1024 {
				break
			}
			block := blocks[hash]
			body := &ethproto.BlockBody{Transactions: block.Transactions(), Uncles: block.Uncles()}
			encoded, err := rlp.EncodeToBytes(body)
			require.NoError(t, err)
			size += len(encoded)
			bodies = append(bodies, body)
		}
		req := &ethproto.Request{Cancel: make(chan struct{})}
		go func() { sink <- &ethproto.Response{Req: req, Res: &bodies, Done: make(chan error, 1)} }()
		return req, nil
	}
}

func TestBlockFetchPartialReleaseOwnership(t *testing.T) {
	for _, mode := range []string{"retry", "empty", "headers", "new owner", "queued"} {
		t.Run(mode, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			a := addBlockReleaseRequest(f, mode == "headers", 1, "peer")
			release := blockFetchRelease{announces: []*blockAnnounce{a}, retryBodies: mode != "empty", headers: mode == "headers"}
			if mode == "new owner" {
				copy := *a
				f.completing[a.hash] = &copy
			}
			if mode == "queued" {
				f.queued[a.hash] = &blockOrHeaderInject{}
			}
			require.Equal(t, mode == "retry", f.releaseFetchOwnership(release))
			if mode != "retry" {
				require.Empty(t, f.fetched)
				return
			}
			retry := f.fetched[a.hash][0]
			require.NotSame(t, a, retry)
			require.True(t, retry.bodyDeferred)
			require.Equal(t, a.origin, retry.origin)
			delete(f.fetched, a.hash)
			f.completing[a.hash] = retry
			require.False(t, f.releaseFetchOwnership(release))
			require.Same(t, retry, f.completing[a.hash])
		})
	}
}
