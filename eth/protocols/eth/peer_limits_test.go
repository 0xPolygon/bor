package eth

import (
	"bytes"
	"errors"
	"fmt"
	"math"
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
		TransactionsMsg, NewPooledTransactionHashesMsg, PooledTransactionsMsg, BlockRangeUpdateMsg,
	} {
		if err := peer.checkMessageRate(code, 1, now); err != nil {
			t.Fatalf("request allowance affected message %#x: %v", code, err)
		}
	}
	if err := limitedTestPeer().checkMessageRate(GetBlockHeadersMsg, 1, now); err != nil {
		t.Fatalf("request allowance affected another peer: %v", err)
	}
}

type limitTestRW struct {
	msg p2p.Msg
}

func (rw *limitTestRW) ReadMsg() (p2p.Msg, error) { return rw.msg, nil }
func (rw *limitTestRW) WriteMsg(msg p2p.Msg) error {
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

func TestPeerConfiguredLimits(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static", "static inbound", "added inbound"} {
		t.Run(mode, func(t *testing.T) {
			p := configuredLimitsTestPeer(t, mode)
			peer, now := &Peer{Peer: p, limits: newPeerLimits()}, time.Now()
			if peer.IsStatic() != p.Static() {
				t.Fatal("broadcast static classification differs from configured membership")
			}
			peer.limits.requests = rate.NewLimiter(0, 0)
			peer.limits.gossip = rate.NewLimiter(0, 0)
			peer.limits.hashes = rate.NewLimiter(0, 0)
			for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, NewBlockMsg, NewBlockHashesMsg} {
				err := peer.checkMessageRate(code, maxMessageSize, now)
				if (err == nil) != (mode != "ordinary") {
					t.Fatalf("message %#x exemption: %v", code, err)
				}
			}
			err := peer.checkAnnouncementRate(peerHashBurst+1, now)
			if (err == nil) != (mode != "ordinary") {
				t.Fatalf("announcement exemption: %v", err)
			}
		})
	}
}

func configuredLimitsTestPeer(t *testing.T, mode string) *p2p.Peer {
	t.Helper()
	stop := make(chan struct{})
	remote := limitsTestServer(t, nil, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	var trusted []*enode.Node
	if mode == "trusted" {
		trusted = append(trusted, remote.Self())
	}
	var static []*enode.Node
	if mode == "static inbound" {
		static = append(static, remote.Self())
	}
	connected := make(chan *p2p.Peer, 1)
	local := limitsTestServer(t, trusted, static, func(p *p2p.Peer, _ p2p.MsgReadWriter) error {
		connected <- p
		<-stop
		return nil
	})
	t.Cleanup(func() { close(stop) })
	if mode == "static" {
		local.AddPeer(remote.Self())
	} else {
		remote.AddPeer(local.Self())
	}
	select {
	case p := <-connected:
		if mode == "added inbound" {
			local.AddPeer(remote.Self())
		}
		if p.Static() != (mode == "static" || mode == "static inbound" || mode == "added inbound") {
			t.Fatal("unexpected static membership")
		}
		if p.Trusted() != (mode == "trusted") || p.StaticDialed() != (mode == "static") {
			t.Fatal("unexpected connection flags")
		}
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not connect")
		return nil
	}
}

func limitsTestServer(t *testing.T, trusted, static []*enode.Node, run func(*p2p.Peer, p2p.MsgReadWriter) error) *p2p.Server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := &p2p.Server{Config: p2p.Config{
		PrivateKey: key, MaxPeers: 4, ListenAddr: "127.0.0.1:0", NoDiscovery: true,
		TrustedNodes: trusted, StaticNodes: static, NoDial: len(static) > 0,
		Protocols: []p2p.Protocol{{Name: "limits", Version: 1, Length: 1, Run: run}},
	}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func TestPeerPooledTransactionRequestBounds(t *testing.T) {
	for _, version := range ProtocolVersions {
		for _, tc := range []struct {
			name  string
			count int
			id    uint64
		}{
			{"empty", 0, 0},
			{"single", 1, 1},
			{"full batch", maxPooledTxsServe, 1},
			{"full batch maximum ID", maxPooledTxsServe, math.MaxUint64},
		} {
			t.Run(fmt.Sprintf("%d/%s", version, tc.name), func(t *testing.T) {
				data, err := rlp.EncodeToBytes(GetPooledTransactionsPacket{tc.id, make(GetPooledTransactionsRequest, tc.count)})
				if err != nil {
					t.Fatal(err)
				}
				rw := &replyTestRW{sent: make(chan p2p.Msg, 1)}
				rw.msg = p2p.Msg{Code: GetPooledTransactionsMsg, Size: uint32(len(data)), Payload: bytes.NewReader(data)}
				peer := NewPeer(version, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
				defer peer.Close()
				peer.limits.requests = rate.NewLimiter(0, 0)
				pool := new(replyLookupPool)
				if err := handleMessage(&replyLookupBackend{pool: pool}, peer); err != nil {
					t.Fatal(err)
				}
				if pool.lookups != tc.count {
					t.Fatalf("got %d lookups, want %d", pool.lookups, tc.count)
				}
				select {
				case reply := <-rw.sent:
					var response PooledTransactionsPacket
					if err := reply.Decode(&response); err != nil || response.RequestId != tc.id {
						t.Fatalf("incorrect reply: %v, %v", response, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request-compliant peer did not receive a reply")
				}
			})
		}
	}
}

func TestPeerPooledTransactionRequestRejectedBeforeDecode(t *testing.T) {
	data, err := rlp.EncodeToBytes(GetPooledTransactionsPacket{0, make(GetPooledTransactionsRequest, maxPooledTxsServe+1)})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range ProtocolVersions {
		for _, size := range []uint32{uint32(len(data)), maxMessageSize} {
			t.Run(fmt.Sprintf("%d/%d", version, size), func(t *testing.T) {
				payload := bytes.NewReader(data)
				rw := &limitTestRW{msg: p2p.Msg{Code: GetPooledTransactionsMsg, Size: size, Payload: payload}}
				peer := NewPeer(version, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
				defer peer.Close()
				pool := new(replyLookupPool)
				err := handleMessage(&replyLookupBackend{pool: pool}, peer)
				if !errors.Is(err, errMsgTooLarge) || errors.Is(err, ErrPeerRateLimit) {
					t.Fatalf("oversized request should disconnect without jail: %v", err)
				}
				if payload.Len() != len(data) || pool.lookups != 0 {
					t.Fatalf("rejected request consumed %d bytes and performed %d lookups", len(data)-payload.Len(), pool.lookups)
				}
			})
		}
	}
}
