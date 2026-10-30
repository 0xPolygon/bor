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
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

func TestPeerPolicyDownloaderObservation(t *testing.T) {
	d := &Downloader{peers: newPeerSet()}
	calls := 0
	d.SetFailureObserver(func(id string) {
		if id != "peer" {
			t.Fatalf("wrong peer: %s", id)
		}
		calls++
	})
	peer := newPeerConnection("peer", 69, nil, log.New())
	d.respondToPeer(peer, peerFailureNoRemote, nil)
	if calls != 1 {
		t.Fatal("missing classified failure")
	}
	if len(d.peers.jailed) != 0 {
		t.Fatal("observation changed jail state")
	}
	d.observeFailure("peer", peerFailureReason("arbitrary"))
	if calls != 1 {
		t.Fatal("unknown reason accepted")
	}
	d.SetFailureObserver(nil)
	d.respondToPeer(peer, peerFailureNoRemote, nil)
	if calls != 1 {
		t.Fatal("disabled observer called")
	}
}

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

func (p *scriptedHeaderPeer) RequestReceipts([]common.Hash, []uint64, []uint64, chan *eth.Response) (*eth.Request, error) {
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

// Peers before the stop-at-first-gap serving rule omit unavailable hashes from
// the middle of a reply; positional rejection alone is not evidence.
func TestPeerPolicyOmittedBodyAndReceiptsStayNeutral(t *testing.T) {
	tester := newTester(t)
	defer tester.terminate()
	missing := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1), TxHash: common.Hash{1}, ReceiptHash: common.Hash{2}, UncleHash: types.EmptyUncleHash}
	available := &types.Header{Number: big.NewInt(2), Difficulty: big.NewInt(1), ParentHash: missing.Hash(), TxHash: types.EmptyTxsHash, ReceiptHash: types.EmptyReceiptsHash, UncleHash: types.EmptyUncleHash}
	rawdb.WriteBlock(tester.chain.DB(), types.NewBlockWithHeader(available))
	hashes := []common.Hash{missing.Hash(), available.Hash()}
	for _, kind := range []string{"bodies", "receipts"} {
		t.Run(kind, func(t *testing.T) {
			tracker := peerpolicy.New(new(mclock.Simulated))
			d := &Downloader{queue: newQueue(10, 10, nil)}
			d.SetInvalidResponseObserver(func(id string) { tracker.Observe(id, peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock}) })
			peer := newPeerConnection("honest", eth.ETH69, nil, log.New("case", kind))
			request := &fetchRequest{Peer: peer, Headers: []*types.Header{missing, available}}
			var err, want error
			if kind == "bodies" {
				packet := omittedBodyReply(t, tester, hashes, available)
				d.queue.blockPendPool[peer.id] = request
				_, err = (*bodyQueue)(d).deliver(peer, packet)
				want = errInvalidBody
			} else {
				packet := omittedReceiptReply(t, tester, hashes, available)
				d.queue.receiptPendPool[peer.id] = request
				_, err = (*receiptQueue)(d).deliver(peer, packet)
				want = errInvalidReceipt
			}
			if !errors.Is(err, want) {
				t.Fatalf("expected positional rejection %v, got %v", want, err)
			}
			if got := tracker.Snapshot("honest"); got.Risk != 0 {
				t.Fatalf("honest partial response was scored: %+v", got)
			}
		})
	}
}

func omittedBodyReply(t *testing.T, tester *downloadTester, hashes []common.Hash, available *types.Header) *eth.Response {
	t.Helper()
	if served := eth.ServiceGetBlockBodiesQuery(tester.chain, hashes); len(served) != 0 {
		t.Fatalf("native server returned %d bodies past the missing one, want 0", len(served))
	}
	encoded := eth.ServiceGetBlockBodiesQuery(tester.chain, hashes[1:])
	if len(encoded) != 1 {
		t.Fatalf("native server returned %d bodies, want 1", len(encoded))
	}
	body := new(eth.BlockBody)
	if err := rlp.DecodeBytes(encoded[0], body); err != nil {
		t.Fatal(err)
	}
	txRoot := types.DeriveSha(types.Transactions(body.Transactions), trie.NewStackTrie(nil))
	if txRoot != available.TxHash {
		t.Fatal("native server did not return the available body")
	}
	bodies := eth.BlockBodiesResponse{body}
	return &eth.Response{Req: &eth.Request{Peer: "honest"}, Res: &bodies, Meta: [][]common.Hash{{txRoot}, {types.CalcUncleHash(body.Uncles)}, {{}}}}
}

func omittedReceiptReply(t *testing.T, tester *downloadTester, hashes []common.Hash, available *types.Header) *eth.Response {
	t.Helper()
	if served := eth.ServiceGetReceiptsQuery69(tester.chain, hashes); len(served) != 0 {
		t.Fatalf("native server returned %d receipt lists past the missing one, want 0", len(served))
	}
	encoded := eth.ServiceGetReceiptsQuery69(tester.chain, hashes[1:])
	if len(encoded) != 1 {
		t.Fatalf("native server returned %d receipt lists, want 1", len(encoded))
	}
	receipts := new(eth.ReceiptList69)
	if err := rlp.DecodeBytes(encoded[0], receipts); err != nil {
		t.Fatal(err)
	}
	if root := types.DeriveSha(receipts, trie.NewStackTrie(nil)); root != available.ReceiptHash {
		t.Fatal("native server did not return the available receipts")
	}
	return &eth.Response{Req: &eth.Request{Peer: "honest"}, Res: []*eth.ReceiptList69{receipts}}
}
