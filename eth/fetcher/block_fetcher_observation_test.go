// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package fetcher

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

type observedImport struct {
	peer string
	err  error
}

func TestBlockContributionRequiresNewImport(t *testing.T) {
	for _, tc := range []struct {
		name                string
		known, stored, want bool
	}{
		{"imported", false, true, true},
		{"already known", true, true, false},
		{"skipped", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := invalidHeaderFetcher(func(*types.Header) error { return nil }, nil)
			peer := eth.NewPeer(eth.ETH69, p2p.NewPeer(enode.ID{1}, "", nil), nil, nil)
			t.Cleanup(peer.Close)
			var useful bool
			peer.SetObserver(func(event peerpolicy.Evidence) { useful = event.Activity.UsefulBlock })
			block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2), ParentHash: common.Hash{1}})
			block.ReceivedFrom = peer
			hasBlock := tc.known
			f.getBlock = func(hash common.Hash) *types.Block {
				if hash == block.Hash() && !hasBlock {
					return nil
				}
				return block
			}
			f.insertChain = func(types.Blocks, []*stateless.Witness) (int, error) {
				hasBlock = tc.stored
				return 0, nil
			}
			f.runBlockImport(&blockOrHeaderInject{origin: peer.ID(), block: block})
			if useful != tc.want {
				t.Fatalf("useful = %v, want %v", useful, tc.want)
			}
		})
	}
}

func TestFetchedBlockCreditsSupplier(t *testing.T) {
	hashes, blocks := makeChain(2, 0, genesis)
	tester := newTester(false)
	t.Cleanup(tester.fetcher.Stop)
	local, remote := p2p.MsgPipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	peer := eth.NewPeer(eth.ETH69, p2p.NewPeer(enode.ID{1}, "", nil), local, nil)
	t.Cleanup(peer.Close)
	credited := make(chan struct{}, 2)
	peer.SetObserver(func(event peerpolicy.Evidence) {
		if event.Activity.UsefulBlock {
			credited <- struct{}{}
		}
	})
	done := make(chan error, 1)
	go func() { done <- eth.Handle(nil, peer) }()
	serve := func() error {
		msg, err := remote.ReadMsg()
		if err != nil {
			return err
		}
		switch msg.Code {
		case eth.GetBlockHeadersMsg:
			var req eth.GetBlockHeadersPacket
			if err := msg.Decode(&req); err != nil {
				return err
			}
			return p2p.Send(remote, eth.BlockHeadersMsg, &eth.BlockHeadersPacket{
				RequestId: req.RequestId, BlockHeadersRequest: eth.BlockHeadersRequest{blocks[req.Origin.Hash].Header()},
			})
		case eth.GetBlockBodiesMsg:
			var req eth.GetBlockBodiesPacket
			if err := msg.Decode(&req); err != nil {
				return err
			}
			body := blocks[req.GetBlockBodiesRequest[0]].Body()
			return p2p.Send(remote, eth.BlockBodiesMsg, &eth.BlockBodiesPacket{
				RequestId: req.RequestId, BlockBodiesResponse: eth.BlockBodiesResponse{{Transactions: body.Transactions, Uncles: body.Uncles}},
			})
		default:
			return fmt.Errorf("unexpected request code %d", msg.Code)
		}
	}
	serveDone := make(chan error, 1)
	go func() {
		for range 3 {
			if err := serve(); err != nil {
				serveDone <- err
				return
			}
		}
		serveDone <- nil
	}()
	for _, hash := range []common.Hash{hashes[1], hashes[0]} {
		block := blocks[hash]
		if err := tester.fetcher.Notify(peer.ID(), hash, block.NumberU64(), time.Now().Add(-arriveTimeout), peer.RequestOneHeader, peer.RequestBodies, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-credited:
			if tester.getBlock(hash) == nil {
				t.Fatal("credited before block import")
			}
		case err := <-done:
			t.Fatalf("ETH handler stopped: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("no contribution credit for fetched block %d", block.NumberU64())
		}
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func invalidHeaderFetcher(verify func(*types.Header) error, insert error) (*BlockFetcher, *[]observedImport) {
	var observed []observedImport

	parent := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	f := &BlockFetcher{
		done:           make(chan common.Hash, 1),
		getHeader:      func(common.Hash) *types.Header { return parent.Header() },
		getBlock:       func(common.Hash) *types.Block { return parent },
		verifyHeader:   verify,
		broadcastBlock: func(*types.Block, *stateless.Witness, bool) {},
		insertHeaders:  func([]*types.Header) (int, error) { return 0, insert },
		insertChain:    func(types.Blocks, []*stateless.Witness) (int, error) { return 0, insert },
		dropPeer:       func(string) {},
	}
	f.SetInvalidHeaderObserver(func(peer string, err error) {
		observed = append(observed, observedImport{peer: peer, err: err})
	})

	return f, &observed
}

func TestBlockFetcherInvalidHeaderObservation(t *testing.T) {
	sentinel := consensus.ErrInvalidNumber
	wrapped := fmt.Errorf("verify header: %w", sentinel)

	for _, tc := range []struct {
		name          string
		verify        error
		insert        error
		wantErrs      []error
		missingParent bool
	}{
		{name: "valid header imports silently"},
		{name: "future block is not the supplier's doing", verify: consensus.ErrFutureBlock},
		{name: "unknown ancestor is not the supplier's doing", verify: consensus.ErrUnknownAncestor, wantErrs: []error{consensus.ErrUnknownAncestor}},
		{name: "refused header reaches the observer", verify: wrapped, wantErrs: []error{wrapped}},
		{name: "insertion failure is not a verification verdict", insert: errors.New("database closed")},
		{name: "missing parent prevents verification", verify: sentinel, missingParent: true},
	} {
		for _, mode := range []string{"header", "block"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				f, observed := invalidHeaderFetcher(func(*types.Header) error { return tc.verify }, tc.insert)
				if tc.missingParent {
					f.getHeader = func(common.Hash) *types.Header { return nil }
					f.getBlock = func(common.Hash) *types.Block { return nil }
				}
				header := &types.Header{Number: big.NewInt(2), ParentHash: common.Hash{1}}

				if mode == "header" {
					f.importHeaders("supplier", header)
					<-f.done
				} else {
					f.runBlockImport(&blockOrHeaderInject{origin: "supplier", block: types.NewBlockWithHeader(header)})
				}

				if len(*observed) != len(tc.wantErrs) {
					t.Fatalf("observed %v, want %d reports", *observed, len(tc.wantErrs))
				}
				for i, want := range tc.wantErrs {
					if (*observed)[i].peer != "supplier" || !errors.Is((*observed)[i].err, want) {
						t.Fatalf("observed %+v, want supplier/%v", (*observed)[i], want)
					}
				}
			})
		}
	}
}

// Header faults belong to the header supplier, not a separate witness server.
func TestBlockFetcherInvalidHeaderBlamesHeaderSupplier(t *testing.T) {
	f, observed := invalidHeaderFetcher(func(*types.Header) error { return errors.New("non empty uncle hash") }, nil)
	f.runBlockImport(&blockOrHeaderInject{
		origin:      "supplier",
		block:       types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2), ParentHash: common.Hash{1}}),
		witnessPeer: "witness-server",
	})

	if len(*observed) != 1 || (*observed)[0].peer != "supplier" {
		t.Fatalf("observed %v, want a single report against the header supplier", *observed)
	}
}

func TestBlockFetcherInvalidHeaderObserverOptional(t *testing.T) {
	dropped := ""
	f, _ := invalidHeaderFetcher(func(*types.Header) error { return errors.New("non empty uncle hash") }, nil)
	f.SetInvalidHeaderObserver(nil)
	f.dropPeer = func(peer string) { dropped = peer }

	f.runBlockImport(&blockOrHeaderInject{origin: "supplier", block: types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2), ParentHash: common.Hash{1}})})

	if dropped != "supplier" {
		t.Fatalf("existing drop changed: %q", dropped)
	}
}
