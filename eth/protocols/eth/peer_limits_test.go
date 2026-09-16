package eth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/synctest"
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
		{"requests", []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg}, peerRequestBurst, peerRequestRate},
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
		for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg, NewBlockMsg, NewBlockHashesMsg} {
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
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			p := configuredLimitsTestPeer(t, mode)
			peer, now := &Peer{Peer: p, limits: newPeerLimits()}, time.Now()
			peer.limits.requests = rate.NewLimiter(0, 0)
			peer.limits.gossip = rate.NewLimiter(0, 0)
			peer.limits.hashes = rate.NewLimiter(0, 0)
			for _, code := range []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg, NewBlockMsg, NewBlockHashesMsg} {
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
	remote := limitsTestServer(t, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	var trusted []*enode.Node
	if mode == "trusted" {
		trusted = append(trusted, remote.Self())
	}
	connected := make(chan *p2p.Peer, 1)
	local := limitsTestServer(t, trusted, func(p *p2p.Peer, _ p2p.MsgReadWriter) error {
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
		if p.Trusted() != (mode == "trusted") || p.StaticDialed() != (mode == "static") {
			t.Fatal("unexpected connection flags")
		}
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not connect")
		return nil
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

type replyTestRW struct {
	limitTestRW
	sent chan p2p.Msg
	err  error
}

func (rw *replyTestRW) WriteMsg(msg p2p.Msg) error {
	if rw.err != nil {
		return rw.err
	}
	data, err := io.ReadAll(msg.Payload)
	if err != nil {
		return err
	}
	msg.Payload = bytes.NewReader(data)
	rw.sent <- msg
	return nil
}

func newReplyTestPeer(t *testing.T) (*Peer, *replyTestRW) {
	t.Helper()
	rw := &replyTestRW{sent: make(chan p2p.Msg, peerRequestBurst)}
	p := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
	t.Cleanup(p.Close)
	return p, rw
}

func queueTestReplies(t *testing.T, p *Peer) []uint64 {
	t.Helper()
	data := []rlp.RawValue{{0x80}}
	for _, reply := range []func(uint64, []rlp.RawValue) error{
		p.ReplyBlockHeadersRLP, p.ReplyBlockBodiesRLP, p.ReplyReceiptsRLP,
		func(id uint64, data []rlp.RawValue) error { return p.ReplyPooledTransactionsRLP(id, nil, data) },
	} {
		if err := reply(128, data); err != nil {
			t.Fatal(err)
		}
	}
	return []uint64{BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg, PooledTransactionsMsg}
}

func TestPeerReplyByteLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		p.limits.replyBytes.SetLimit(0)
		codes := queueTestReplies(t, p)
		synctest.Wait()
		var total int
		for _, code := range codes {
			select {
			case msg := <-rw.sent:
				want := []byte{0xc4, 0x81, 0x80, 0xc1, 0x80}
				data, err := io.ReadAll(msg.Payload)
				if err != nil || msg.Code != code || !bytes.Equal(data, want) || int(msg.Size) != len(want) {
					t.Fatalf("reply %#x: %x, size %d, err %v", msg.Code, data, msg.Size, err)
				}
				total += len(want)
			default:
				t.Fatal("reply within burst was not sent")
			}
		}
		if got := p.limits.replyBytes.Tokens(); got != float64(peerByteBurst-total) {
			t.Fatalf("shared reply allowance: %v", got)
		}
		if got := p.limits.gossipBytes.Tokens(); got != peerByteBurst {
			t.Fatalf("reply consumed gossip allowance: %v", got)
		}
		if p.replyBytes != 0 {
			t.Fatalf("sent replies retained %d bytes", p.replyBytes)
		}
	})
}

func TestPeerReplyThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		p.limits.replyBytes.SetLimit(5)
		if !p.limits.replyBytes.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust reply allowance")
		}
		start := time.Now()
		codes := queueTestReplies(t, p)
		if time.Since(start) != 0 {
			t.Fatal("queuing blocked the protocol reader")
		}
		for _, code := range codes {
			synctest.Wait()
			if len(rw.sent) != 0 {
				t.Fatal("reply sent before allowance refilled")
			}
			time.Sleep(time.Second)
			synctest.Wait()
			if len(rw.sent) != 1 || (<-rw.sent).Code != code {
				t.Fatal("refilled allowance did not release the next reply")
			}
		}
		if err := p.checkMessageRate(GetBlockHeadersMsg, 1, time.Now()); err != nil {
			t.Fatalf("reply throttling affected request allowance: %v", err)
		}
	})
}

func TestPeerReplyThrottleKeepsReaderRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		p.limits.replyBytes.SetLimit(1)
		if !p.limits.replyBytes.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust reply allowance")
		}
		backend := &packetCapturingBackend{testBackend: new(testBackend)}
		rw.msg = p2p.Msg{Code: GetBlockBodiesMsg, Size: 3, Payload: bytes.NewReader([]byte{0xc2, 0x01, 0xc0})}
		start := time.Now()
		if err := handleMessage(backend, p); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		rw.msg = p2p.Msg{Code: NewBlockHashesMsg, Size: 1, Payload: bytes.NewReader([]byte{0xc0})}
		if err := handleMessage(backend, p); err != nil {
			t.Fatal(err)
		}
		if backend.packet == nil || time.Since(start) != 0 || len(rw.sent) != 0 {
			t.Fatal("pending reply blocked inbound gossip")
		}
	})
}

func TestPeerReplyQueueBounds(t *testing.T) {
	for _, limit := range []string{"count", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, rw := newReplyTestPeer(t)
				p.limits.replyBytes.SetLimit(1)
				if !p.limits.replyBytes.AllowN(time.Now(), peerByteBurst) {
					t.Fatal("could not exhaust reply allowance")
				}
				data, count := []rlp.RawValue(nil), peerRequestBurst+1
				if limit == "bytes" {
					encoded, err := rlp.EncodeToBytes(make([]byte, 8*1024*1024-13))
					if err != nil {
						t.Fatal(err)
					}
					data = []rlp.RawValue{encoded}
					count = peerByteBurst / (8 * 1024 * 1024)
				}
				for i := 0; i < count; i++ {
					if err := p.ReplyBlockBodiesRLP(1, data); err != nil {
						t.Fatalf("reply %d within queue bound: %v", i, err)
					}
					synctest.Wait()
				}
				err := p.ReplyBlockBodiesRLP(1, nil)
				if !errors.Is(err, errReplyQueueFull) || errors.Is(err, ErrPeerRateLimit) {
					t.Fatalf("full queue should disconnect without jail: %v", err)
				}
				if len(rw.sent) != 0 || p.replyBytes > peerByteBurst {
					t.Fatal("reply queue exceeded its allowance")
				}
			})
		})
	}
}

func TestPeerReplyQueueShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rw := &replyTestRW{sent: make(chan p2p.Msg, 4)}
		p := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
		p.limits.replyBytes.SetLimit(1)
		if !p.limits.replyBytes.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust reply allowance")
		}
		queueTestReplies(t, p)
		synctest.Wait()
		p.Close()
		synctest.Wait()
		if p.replyQueue != nil || p.replyBytes != 0 || len(rw.sent) != 0 {
			t.Fatal("close did not release pending replies")
		}
		if got := p.limits.replyBytes.Tokens(); got != 0 {
			t.Fatalf("close did not refund reserved bytes: %v", got)
		}
		p.limits.replyBytes.SetLimit(rate.Inf)
		if err := p.waitReplyAllowance(1); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("closed peer received immediate allowance: %v", err)
		}
		if err := p.ReplyBlockHeadersRLP(1, nil); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("reply queued after close: %v", err)
		}
	})
}

func TestPeerReplyWriteFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		rw.err = io.ErrClosedPipe
		if err := p.ReplyReceiptsRLP(1, nil); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if p.replyQueue != nil || p.replyBytes != 0 {
			t.Fatal("write failure did not release pending replies")
		}
		if err := p.ReplyReceiptsRLP(1, nil); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("reply queued after writer stopped: %v", err)
		}
	})
}

func TestPeerReplyConfiguredExemptions(t *testing.T) {
	for _, mode := range []string{"trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			rw := &replyTestRW{sent: make(chan p2p.Msg, 4)}
			p := NewPeer(ETH68, configuredLimitsTestPeer(t, mode), rw, nil)
			defer p.Close()
			p.limits.replyBytes = rate.NewLimiter(0, 0)
			for _, code := range queueTestReplies(t, p) {
				select {
				case msg := <-rw.sent:
					if msg.Code != code {
						t.Fatalf("reply code: got %d, want %d", msg.Code, code)
					}
				default:
					t.Fatal("configured peer reply was throttled")
				}
			}
		})
	}
}

func TestPeerReplyValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		if err := p.waitReplyAllowance(peerByteBurst + 1); !errors.Is(err, errMsgTooLarge) {
			t.Fatalf("reply larger than allowance burst: %v", err)
		}
		if err := p.queueReply(BlockHeadersMsg, make(chan int)); err == nil {
			t.Fatal("unencodable response accepted")
		}
		if err := p.ReplyBlockBodiesRLP(1, []rlp.RawValue{make([]byte, maxMessageSize)}); !errors.Is(err, errMsgTooLarge) {
			t.Fatalf("oversized reply: %v", err)
		}
		data, err := rlp.EncodeToBytes(make([]byte, maxMessageSize-13))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.ReplyBlockBodiesRLP(1, []rlp.RawValue{data}); err != nil {
			t.Fatalf("reply at message size limit: %v", err)
		}
		hash := common.Hash{1}
		if err := p.ReplyPooledTransactionsRLP(1, []common.Hash{hash}, nil); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !p.KnownTransaction(hash) || len(rw.sent) != 2 {
			t.Fatal("pooled transaction reply not tracked and sent")
		}
	})
}
