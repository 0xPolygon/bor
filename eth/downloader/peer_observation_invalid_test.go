// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package downloader

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func observedDownloader(t *testing.T) (*Downloader, *peerpolicy.Tracker) {
	t.Helper()

	d := &Downloader{peers: newPeerSet()}
	tracker := peerpolicy.New(new(mclock.Simulated))
	// eth/peer_policy.go maps this observer to peerpolicy.InvalidBlock.
	d.SetInvalidResponseObserver(func(id string) {
		tracker.Observe(id, peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
	})

	return d, tracker
}

func pivotReply(next, confirmer uint64) []*types.Header {
	return []*types.Header{
		{Number: new(big.Int).SetUint64(next)},
		{Number: new(big.Int).SetUint64(confirmer)},
	}
}

func TestPeerPolicyInvalidResponseObservation(t *testing.T) {
	d, tracker := observedDownloader(t)
	d.observeInvalidResponse("peer", "header_ordering")

	if got := tracker.Snapshot("peer"); got.Risk != 60 {
		t.Fatalf("provably wrong response was not scored: %+v", got)
	}
	if len(d.peers.jailed) != 0 {
		t.Fatal("observation changed jail state")
	}
	// An unset observer must stay harmless on the sync path.
	(&Downloader{}).observeInvalidResponse("peer", "header_ordering")
}

// A local pivot move makes the existing rejection ambiguous for peer scoring.
func TestPeerPolicyNextPivotEvidenceUsesRequestTimePivot(t *testing.T) {
	const requestPivot = uint64(1000)

	deep := uint64(fsMinFullBlocks)

	for _, tc := range []struct {
		name         string
		currentPivot uint64
		headers      []*types.Header
		wantRisk     uint64
	}{
		{
			name:         "unchanged pivot and correct reply",
			currentPivot: requestPivot,
			headers:      pivotReply(requestPivot+deep, requestPivot+2*deep-8),
			wantRisk:     0,
		},
		{
			name:         "pivot advanced while the correct reply was in flight",
			currentPivot: requestPivot + 2*deep,
			headers:      pivotReply(requestPivot+deep, requestPivot+2*deep-8),
			wantRisk:     0,
		},
		{
			name:         "pivot advanced and the confirmer answers neither request",
			currentPivot: requestPivot + 2*deep,
			headers:      pivotReply(requestPivot+3*deep, 7),
			wantRisk:     0,
		},
		{
			name:         "unchanged pivot and a number no request asked for",
			currentPivot: requestPivot,
			headers:      pivotReply(requestPivot+9, requestPivot+2*deep-8),
			wantRisk:     60,
		},
		{
			name:         "unchanged pivot and a confirmer no request asked for",
			currentPivot: requestPivot,
			headers:      pivotReply(requestPivot+deep, requestPivot+9),
			wantRisk:     60,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, tracker := observedDownloader(t)
			peer := newPeerConnection("peer", 69, nil, log.New())

			err := d.verifyNextPivot(peer, tc.headers, requestPivot, tc.currentPivot)
			// Rejection is unchanged: the reply must match the pivot held now.
			correctForCurrent := tc.headers[0].Number.Uint64() == tc.currentPivot+deep &&
				tc.headers[1].Number.Uint64() == tc.currentPivot+2*deep-8
			if gotErr := err != nil; gotErr == correctForCurrent {
				t.Fatalf("sync rejection changed: err=%v", err)
			}
			if err != nil && !errors.Is(err, errInvalidChain) {
				t.Fatalf("unexpected rejection error: %v", err)
			}
			if got := tracker.Snapshot("peer"); got.Risk != tc.wantRisk {
				t.Fatalf("risk %d, want %d: %+v", got.Risk, tc.wantRisk, got)
			}
		})
	}
}

type scriptedHeaderPeer struct {
	headers []*types.Header
	hashes  []common.Hash
}

func (p *scriptedHeaderPeer) Head() (common.Hash, *big.Int) { return common.Hash{}, new(big.Int) }

func (p *scriptedHeaderPeer) RequestHeadersByNumber(_ uint64, _ int, _ int, _ bool, sink chan *eth.Response) (*eth.Request, error) {
	req := new(eth.Request)
	headers := eth.BlockHeadersRequest(p.headers)

	go func() {
		sink <- &eth.Response{Req: req, Res: &headers, Meta: p.hashes, Done: make(chan error, 1)}
	}()

	return req, nil
}

func (p *scriptedHeaderPeer) RequestHeadersByHash(common.Hash, int, int, bool, chan *eth.Response) (*eth.Request, error) {
	return nil, errors.New("not used")
}

func (p *scriptedHeaderPeer) RequestBodies([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	return nil, errors.New("not used")
}

func (p *scriptedHeaderPeer) RequestReceipts([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	return nil, errors.New("not used")
}

func (p *scriptedHeaderPeer) RequestWitnesses([]common.Hash, chan *eth.Response) (*eth.Request, error) {
	return nil, errors.New("not used")
}

func (p *scriptedHeaderPeer) SupportsWitness() bool { return false }

// Empty replies are neutral; excess or known mismatching headers are attributable.
func TestPeerPolicyBinarySearchInvalidResponses(t *testing.T) {
	tester := newTester(t)
	defer tester.terminate()

	genesis := tester.chain.Genesis().Header()
	someHeader := &types.Header{Number: big.NewInt(50)}

	for _, tc := range []struct {
		name     string
		headers  []*types.Header
		hashes   []common.Hash
		wantRisk uint64
	}{
		{
			name:     "two headers for a single header request",
			headers:  []*types.Header{someHeader, someHeader},
			hashes:   []common.Hash{someHeader.Hash(), someHeader.Hash()},
			wantRisk: 60,
		},
		{
			name:     "a hash for a number nobody asked for",
			headers:  []*types.Header{genesis},
			hashes:   []common.Hash{genesis.Hash()},
			wantRisk: 60,
		},
		{
			name:     "empty reply is how an honest peer says it has nothing",
			headers:  nil,
			hashes:   nil,
			wantRisk: 0,
		},
		{
			name:     "an unknown header only narrows the search",
			headers:  []*types.Header{someHeader},
			hashes:   []common.Hash{someHeader.Hash()},
			wantRisk: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := peerpolicy.New(new(mclock.Simulated))
			tester.downloader.SetInvalidResponseObserver(func(id string) {
				tracker.Observe(id, peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
			})
			defer tester.downloader.SetInvalidResponseObserver(nil)

			peer := newPeerConnection("peer", eth.ETH69, &scriptedHeaderPeer{headers: tc.headers, hashes: tc.hashes}, log.New())
			//nolint:errcheck // The search outcome is asserted through the evidence.
			tester.downloader.findAncestorBinarySearch(peer, FullSync, 100, 0)

			if got := tracker.Snapshot("peer"); got.Risk != tc.wantRisk {
				t.Fatalf("risk %d, want %d: %+v", got.Risk, tc.wantRisk, got)
			}
		})
	}
}
