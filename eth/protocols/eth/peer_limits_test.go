package eth

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
)

func limitedTestPeer() *Peer {
	return &Peer{Peer: p2p.NewPeer(enode.ID{1}, "", nil), limits: newPeerLimits(), term: make(chan struct{})}
}

func TestPeerMessageLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codes []uint64
		burst int
		rate  int
	}{
		{"requests", []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg}, peerRequestBurst, peerRequestRate},
		{"gossip", []uint64{NewBlockMsg, NewBlockHashesMsg}, peerGossipBurst, peerGossipRate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, now := limitedTestPeer(), time.Now()
			for i := 0; i < tc.burst; i++ {
				if err := peer.checkMessageRate(tc.codes[i%len(tc.codes)], 0, now); err != nil {
					t.Fatalf("message %d within burst: %v", i, err)
				}
			}
			for _, code := range tc.codes {
				if err := peer.checkMessageRate(code, 0, now); !errors.Is(err, ErrPeerRateLimit) {
					t.Fatalf("message %#x beyond shared burst: %v", code, err)
				}
			}
			now = now.Add(time.Second)
			for i := 0; i < tc.rate; i++ {
				if err := peer.checkMessageRate(tc.codes[0], 0, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := peer.checkMessageRate(tc.codes[0], 0, now); !errors.Is(err, ErrPeerRateLimit) {
				t.Fatalf("message beyond refill: %v", err)
			}
		})
	}
}

func TestPeerGossipByteLimit(t *testing.T) {
	peer, now := limitedTestPeer(), time.Now()
	for i := 0; i < peerByteBurst/(8<<20); i++ {
		if err := peer.checkMessageRate(NewBlockMsg, 8<<20, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := peer.checkMessageRate(NewBlockMsg, 1, now); !errors.Is(err, ErrPeerRateLimit) {
		t.Fatalf("gossip beyond byte allowance: %v", err)
	}
	if err := peer.checkMessageRate(GetBlockBodiesMsg, 1, now); err != nil {
		t.Fatalf("gossip consumed download allowance: %v", err)
	}
	if err := peer.checkMessageRate(NewBlockMsg, peerByteRate, now.Add(time.Second)); err != nil {
		t.Fatalf("refilled byte allowance: %v", err)
	}
}

func TestPeerAnnouncementLimit(t *testing.T) {
	peer, now := limitedTestPeer(), time.Now()
	if err := peer.checkAnnouncementRate(peerHashBurst, now); err != nil {
		t.Fatal(err)
	}
	if err := peer.checkAnnouncementRate(1, now); !errors.Is(err, ErrPeerRateLimit) {
		t.Fatalf("hash beyond allowance: %v", err)
	}
	if err := peer.checkAnnouncementRate(peerHashRate, now.Add(time.Second)); err != nil {
		t.Fatalf("refilled allowance: %v", err)
	}
	if err := peer.checkAnnouncementRate(1, now.Add(time.Second)); !errors.Is(err, ErrPeerRateLimit) {
		t.Fatalf("hash beyond refill: %v", err)
	}
	if err := limitedTestPeer().checkAnnouncementRate(peerHashBurst+1, now); !errors.Is(err, ErrPeerRateLimit) {
		t.Fatalf("oversized batch: %v", err)
	}
}

func TestPeerLimitsIsolation(t *testing.T) {
	peer, now := limitedTestPeer(), time.Now()
	if !peer.limits.requests.AllowN(now, peerRequestBurst) {
		t.Fatal("could not consume request allowance")
	}
	for _, code := range []uint64{
		NewBlockMsg, NewBlockHashesMsg, BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg,
		TransactionsMsg, NewPooledTransactionHashesMsg, GetPooledTransactionsMsg, PooledTransactionsMsg, BlockRangeUpdateMsg,
	} {
		if err := peer.checkMessageRate(code, 1, now); err != nil {
			t.Fatalf("request allowance affected message %#x: %v", code, err)
		}
	}
	if err := limitedTestPeer().checkMessageRate(GetBlockHeadersMsg, 1, now); err != nil {
		t.Fatalf("request allowance affected another peer: %v", err)
	}
}

func TestPeerReplyAccounting(t *testing.T) {
	for _, id := range []uint64{0, 127, 128, ^uint64(0)} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			data := []rlp.RawValue{{0xc0}, {0x82, 0x01, 0x02}}
			encoded, err := rlp.EncodeToBytes(&BlockBodiesRLPPacket{id, data})
			if err != nil {
				t.Fatal(err)
			}
			peer, now := limitedTestPeer(), time.Now()
			peer.limits.replyBytes = rate.NewLimiter(0, len(encoded))
			if err := peer.checkReplyRate(id, data, now); err != nil {
				t.Fatalf("exact allowance: %v", err)
			}
			if tokens := peer.limits.replyBytes.TokensAt(now); tokens != 0 {
				t.Fatalf("uncharged encoded bytes: %v", tokens)
			}
			if err := peer.checkReplyRate(id, nil, now); !errors.Is(err, errPeerResponseScheduling) {
				t.Fatalf("empty response beyond allowance: %v", err)
			}
			if tokens := peer.limits.replyBytes.TokensAt(now); tokens != 0 {
				t.Fatalf("failed reservation consumed tokens: %v", tokens)
			}
		})
	}
}

type limitTestRW struct {
	msg    p2p.Msg
	writes int
}

func (rw *limitTestRW) ReadMsg() (p2p.Msg, error) { return rw.msg, nil }
func (rw *limitTestRW) WriteMsg(msg p2p.Msg) error {
	rw.writes++
	return msg.Discard()
}

func TestPeerMessageLimitBeforeDecode(t *testing.T) {
	for _, version := range ProtocolVersions {
		for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, NewBlockMsg, NewBlockHashesMsg} {
			t.Run(fmt.Sprintf("%d/%d", version, code), func(t *testing.T) {
				rw := &limitTestRW{msg: p2p.Msg{Code: code, Size: 1, Payload: bytes.NewReader([]byte{0xff})}}
				peer := NewPeer(version, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
				defer peer.Close()
				peer.limits.requests = rate.NewLimiter(0, 0)
				peer.limits.gossip = rate.NewLimiter(0, 0)
				if err := handleMessage(nil, peer); !errors.Is(err, ErrPeerRateLimit) {
					t.Fatalf("expected rate check before malformed packet decoding: %v", err)
				}
			})
		}
	}
}

func TestPeerReplyLimitBeforeSend(t *testing.T) {
	peer := limitedTestPeer()
	rw := new(limitTestRW)
	peer.rw = rw
	peer.limits.replyBytes = rate.NewLimiter(0, 0)
	for _, reply := range []func(uint64, []rlp.RawValue) error{
		peer.ReplyBlockHeadersRLP, peer.ReplyBlockBodiesRLP, peer.ReplyReceiptsRLP,
	} {
		if err := reply(1, []rlp.RawValue{{0xc0}}); !errors.Is(err, errPeerResponseScheduling) {
			t.Fatalf("expected limited response: %v", err)
		}
	}
	if rw.writes != 0 {
		t.Fatalf("sent %d responses beyond allowance", rw.writes)
	}
}

func TestPeerReplyLimitWaits(t *testing.T) {
	data := []rlp.RawValue{{0xc0}}
	encoded, err := rlp.EncodeToBytes(&BlockBodiesRLPPacket{1, data})
	if err != nil {
		t.Fatal(err)
	}
	peer := limitedTestPeer()
	rw := new(limitTestRW)
	peer.rw = rw
	peer.limits.replyBytes = rate.NewLimiter(rate.Limit(len(encoded)*20), len(encoded))
	if err := peer.ReplyBlockBodiesRLP(1, data); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := peer.ReplyBlockBodiesRLP(2, data); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("response was not throttled: %v", elapsed)
	}
	if rw.writes != 2 {
		t.Fatalf("sent %d responses, want 2", rw.writes)
	}
}

func TestPeerReplyLimitStopsAfterDisconnect(t *testing.T) {
	data := []rlp.RawValue{{0xc0}}
	encoded, err := rlp.EncodeToBytes(&BlockBodiesRLPPacket{1, data})
	if err != nil {
		t.Fatal(err)
	}
	peer := limitedTestPeer()
	rw := new(limitTestRW)
	peer.rw = rw
	peer.limits.replyBytes = rate.NewLimiter(rate.Limit(1), len(encoded))
	if err := peer.ReplyBlockBodiesRLP(1, data); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- peer.ReplyBlockBodiesRLP(2, data) }()
	close(peer.term)
	select {
	case err := <-result:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("reply error: got %v, want %v", err, ErrDisconnected)
		}
	case <-time.After(time.Second):
		t.Fatal("reply did not stop after disconnect")
	}
	if rw.writes != 1 {
		t.Fatalf("sent %d responses, want 1", rw.writes)
	}
}

func TestPeerAnnouncementLimitBeforeBackend(t *testing.T) {
	ann := make(NewBlockHashesPacket, peerHashBurst+1)
	for i := range ann {
		ann[i].Hash, ann[i].Number = common.Hash{1}, 1
	}
	encoded, err := rlp.EncodeToBytes(ann)
	if err != nil {
		t.Fatal(err)
	}
	rw := &limitTestRW{msg: p2p.Msg{Code: NewBlockHashesMsg, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}}
	peer := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
	defer peer.Close()
	if err := handleMessage(nil, peer); !errors.Is(err, ErrPeerRateLimit) {
		t.Fatalf("expected batch rejection before calling backend: %v", err)
	}
	if peer.KnownBlock(common.Hash{1}) {
		t.Fatal("rejected batch updated known blocks")
	}
}

func TestPeerServingWithinAllowance(t *testing.T) {
	peer, now := limitedTestPeer(), time.Now()
	data := []rlp.RawValue{bytes.Repeat([]byte{0xc0}, 256<<10)}
	for i := 0; i < 320; i++ {
		at := now.Add(time.Duration(i) * time.Second / 32)
		for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg} {
			if err := peer.checkMessageRate(code, 1, at); err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
		}
		if err := peer.checkReplyRate(uint64(i), data, at); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
}

func TestPeerTrustedLimits(t *testing.T) {
	stop := make(chan struct{})
	remote := limitsTestServer(t, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	connected := make(chan *p2p.Peer, 1)
	local := limitsTestServer(t, []*enode.Node{remote.Self()}, func(p *p2p.Peer, _ p2p.MsgReadWriter) error {
		connected <- p
		<-stop
		return nil
	})
	t.Cleanup(func() { close(stop) })
	remote.AddPeer(local.Self())
	select {
	case p := <-connected:
		peer, now := &Peer{Peer: p}, time.Now()
		if !p.Inbound() || !p.Trusted() {
			t.Fatal("expected authenticated inbound trusted peer")
		}
		for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, NewBlockMsg, NewBlockHashesMsg} {
			if err := peer.checkMessageRate(code, maxMessageSize, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := peer.checkAnnouncementRate(peerHashBurst+1, now); err != nil {
			t.Fatal(err)
		}
		if err := peer.checkReplyRate(1, nil, now); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trusted peer did not connect")
	}
}

func limitsTestServer(t *testing.T, trusted []*enode.Node, run func(*p2p.Peer, p2p.MsgReadWriter) error) *p2p.Server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := &p2p.Server{Config: p2p.Config{
		PrivateKey: key, MaxPeers: 4, ListenAddr: "127.0.0.1:0", NoDiscovery: true,
		TrustedNodes: trusted,
		Protocols:    []p2p.Protocol{{Name: "limits", Version: 1, Length: 1, Run: run}},
	}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}
