// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package fetcher

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
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
