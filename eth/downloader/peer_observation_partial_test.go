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

// Native serving omits unavailable hashes; positional rejection alone is not evidence.
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
	encoded := eth.ServiceGetBlockBodiesQuery(tester.chain, hashes)
	if len(encoded) != 1 {
		t.Fatalf("native server returned %d bodies, want 1", len(encoded))
	}
	body := new(eth.BlockBody)
	if err := rlp.DecodeBytes(encoded[0], body); err != nil {
		t.Fatal(err)
	}
	txRoot := types.DeriveSha(types.Transactions(body.Transactions), trie.NewStackTrie(nil))
	if txRoot != available.TxHash {
		t.Fatal("native server did not return the second requested body")
	}
	bodies := eth.BlockBodiesResponse{body}
	return &eth.Response{Req: &eth.Request{Peer: "honest"}, Res: &bodies, Meta: [][]common.Hash{{txRoot}, {types.CalcUncleHash(body.Uncles)}, {{}}}}
}

func omittedReceiptReply(t *testing.T, tester *downloadTester, hashes []common.Hash, available *types.Header) *eth.Response {
	t.Helper()
	encoded := eth.ServiceGetReceiptsQuery69(tester.chain, hashes)
	if len(encoded) != 1 {
		t.Fatalf("native server returned %d receipt lists, want 1", len(encoded))
	}
	receipts := new(eth.ReceiptList69)
	if err := rlp.DecodeBytes(encoded[0], receipts); err != nil {
		t.Fatal(err)
	}
	if root := types.DeriveSha(receipts, trie.NewStackTrie(nil)); root != available.ReceiptHash {
		t.Fatal("native server did not return the second requested receipts")
	}
	return &eth.Response{Req: &eth.Request{Peer: "honest"}, Res: []*eth.ReceiptList69{receipts}}
}
