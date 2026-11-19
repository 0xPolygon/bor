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
	"github.com/ethereum/go-ethereum/rlp"
)

type observedImport struct {
	peer string
	err  error
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

func TestBlockFetcherUndecodableBodyObservation(t *testing.T) {
	hashes, blocks := makeChain(1, 0, genesis)
	header := blocks[hashes[0]].Header()
	if header.TxHash == types.EmptyTxsHash {
		t.Fatal("test block needs a body to fetch")
	}
	tester := newTester(false)
	defer tester.fetcher.Stop()
	observed := make(chan string, 1)
	tester.fetcher.SetInvalidBodyObserver(func(peer string) { observed <- peer })

	undecodableBodies := func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		var body eth.BlockBody
		if err := rlp.DecodeBytes([]byte{0xc3, 0xc1, 0x80, 0xc0}, &body); err != nil {
			return nil, err
		}
		bodies := eth.BlockBodiesResponse{body}
		req := &eth.Request{Peer: "bad"}
		// The roots match the requested header, so the body is decoded.
		meta := eth.BlockBodyHashes{TransactionRoots: []common.Hash{header.TxHash}, UncleHashes: []common.Hash{header.UncleHash}, WithdrawalRoots: []common.Hash{{}}}
		go func() { sink <- &eth.Response{Req: req, Res: &bodies, Meta: meta, Done: make(chan error, 1)} }()
		return req, nil
	}
	tester.fetcher.Notify("bad", hashes[0], 1, time.Now().Add(-arriveTimeout), tester.makeHeaderFetcher("bad", blocks, -gatherSlack), undecodableBodies, tester.makeWitnessFetcher("bad", blocks, 0))

	select {
	case peer := <-observed:
		if peer != "bad" {
			t.Fatalf("observed %q, want the body supplier", peer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("undecodable body was not observed")
	}
}

func TestRequestedBodies(t *testing.T) {
	requested := &types.Header{TxHash: common.Hash{1}, UncleHash: common.Hash{2}}
	bodies := eth.BlockBodiesResponse{{}, {}, {}}
	hashes := eth.BlockBodyHashes{
		TransactionRoots: []common.Hash{{1}, {1}, {3}},
		UncleHashes:      []common.Hash{{2}, {3}, {2}},
	}
	got := requestedBodies(bodies, hashes, []*types.Header{requested})
	if len(got) != 1 {
		t.Fatalf("kept %d bodies, want only the one matching both roots", len(got))
	}
	if got := requestedBodies(bodies, eth.BlockBodyHashes{}, []*types.Header{requested}); len(got) != 0 {
		t.Fatalf("kept %d bodies without hashes", len(got))
	}
}
