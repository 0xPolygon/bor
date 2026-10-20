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
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestFetchSlotBodyReleasedOnEveryExit(t *testing.T) {
	for _, exit := range []string{"nil fetch", "dispatch error", "nil response", "wrong response type", "typed nil response", "shutdown"} {
		t.Run(exit, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			first := addBlockReleaseRequest(f, false, 1, "peer")
			second := addBlockReleaseRequest(f, false, 2, "peer")
			other := addBlockReleaseRequest(f, false, 3, "peer")
			header := addBlockReleaseRequest(f, true, 1, "peer")
			owned := []*blockAnnounce{first, second}
			request := &eth.Request{Cancel: make(chan struct{})}
			response := bodyExitResponse(exit)
			fetch := bodyExitFetcher(f, exit, request, response)
			if exit == "shutdown" {
				f.Stop()
			}
			done := startBodyRequestTest(t, f, bodyRequest{hashes: []common.Hash{first.hash, second.hash}, announces: owned, fetch: fetch})
			if exit != "shutdown" {
				select {
				case release := <-f.fetchRelease:
					if release.headers || !slices.Equal(release.announces, owned) {
						t.Fatalf("body worker returned the wrong ownership: %+v", release)
					}
					f.releaseFetchOwnership(release)
				case <-time.After(time.Second):
					t.Fatal("body worker did not return its batch ownership")
				}
			}
			finishBodyRequestTest(t, f, done)
			if exit != "shutdown" && (f.completing[first.hash] != nil || f.completing[second.hash] != nil) {
				t.Fatal("completed body request retained its missing bodies")
			}
			if f.completing[other.hash] != other || f.fetching[header.hash] != header {
				t.Fatal("body exit removed another request's ownership")
			}
			assertBodyRequestCompletion(t, exit, request, response)
		})
	}
}

func bodyExitResponse(exit string) *eth.Response {
	if exit == "nil response" {
		return nil
	}
	var payload interface{} = new(eth.BlockHeadersRequest)
	if exit == "typed nil response" {
		payload = (*eth.BlockBodiesResponse)(nil)
	}
	return &eth.Response{Res: payload, Done: make(chan error, 1)}
}

func bodyExitFetcher(f *BlockFetcher, exit string, request *eth.Request, response *eth.Response) bodyRequesterFn {
	if exit == "nil fetch" {
		return nil
	}
	return func(_ []common.Hash, chanRes chan *eth.Response) (*eth.Request, error) {
		if exit == "dispatch error" {
			return nil, errors.New("body dispatch failed")
		}
		if exit != "shutdown" {
			go func() {
				select {
				case chanRes <- response:
				case <-f.quit:
				}
			}()
		}
		return request, nil
	}
}

func startBodyRequestTest(t *testing.T, f *BlockFetcher, req bodyRequest) <-chan struct{} {
	t.Helper()
	if !f.acquireFetchSlot("peer") {
		t.Fatal("could not reserve body request capacity")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.runBodyRequest("peer", req)
	}()
	return done
}

func finishBodyRequestTest(t *testing.T, f *BlockFetcher, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("body worker did not finish")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.fetchSlots) != 0 || f.fetchSlotsTotal != 0 {
		t.Fatalf("body worker leaked capacity: peers=%v total=%d", f.fetchSlots, f.fetchSlotsTotal)
	}
}

func assertBodyRequestCompletion(t *testing.T, exit string, request *eth.Request, response *eth.Response) {
	t.Helper()
	if exit == "nil fetch" || exit == "dispatch error" {
		return
	}
	select {
	case <-request.Cancel:
	default:
		t.Error("body worker did not close its dispatched request")
	}
	if response != nil && exit != "shutdown" {
		select {
		case err := <-response.Done:
			if err != nil {
				t.Errorf("body response acknowledgement: %v", err)
			}
		default:
			t.Error("body response was not acknowledged")
		}
	}
}

func TestFetchSlotBodyResponseReleasesMissing(t *testing.T) {
	hashes, blocks := makeChain(2, 91, genesis)
	for _, partial := range []bool{false, true} {
		name := "empty"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			f.getBlock = func(common.Hash) *types.Block { return nil }
			f.chainHeight = func() uint64 { return 0 }
			var owned []*blockAnnounce
			for _, hash := range hashes[:2] {
				announce := &blockAnnounce{hash: hash, header: blocks[hash].Header(), origin: "peer", time: time.Now()}
				f.completing[hash] = announce
				owned = append(owned, announce)
			}
			var bodies eth.BlockBodiesResponse
			if partial {
				block := blocks[hashes[0]]
				bodies = append(bodies, &eth.BlockBody{Transactions: block.Transactions(), Uncles: block.Uncles()})
			}
			request := &eth.Request{Cancel: make(chan struct{})}
			response := &eth.Response{Res: &bodies, Done: make(chan error, 1)}
			fetch := bodyExitFetcher(f, name, request, response)
			f.Start()
			done := startBodyRequestTest(t, f, bodyRequest{hashes: hashes[:2], announces: owned, fetch: fetch})
			finishBodyRequestTest(t, f, done)
			assertBodyRequestCompletion(t, name, request, response)
			resume := pauseBlockTimeoutLoop(t, f)
			defer resume()
			if f.completing[hashes[1]] != nil {
				t.Error("missing body remained owned after the request ended")
			}
			if partial {
				if f.queued[hashes[0]] == nil || f.completing[hashes[0]] != owned[0] {
					t.Error("partial response lost its delivered body before import")
				}
			} else if len(f.completing) != 0 || len(f.queued) != 0 {
				t.Error("empty response retained body ownership or queued a block")
			}
		})
	}
}
