package eth

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
)

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
		p.blockReplies.limiter.SetLimit(0)
		p.txReplies.limiter.SetLimit(0)
		codes := queueTestReplies(t, p)
		synctest.Wait()
		seen := make(map[uint64]bool)
		for range codes {
			select {
			case msg := <-rw.sent:
				want := []byte{0xc4, 0x81, 0x80, 0xc1, 0x80}
				data, err := io.ReadAll(msg.Payload)
				if err != nil || seen[msg.Code] || !bytes.Equal(data, want) || int(msg.Size) != len(want) {
					t.Fatalf("reply %#x: %x, size %d, err %v", msg.Code, data, msg.Size, err)
				}
				seen[msg.Code] = true
			default:
				t.Fatal("reply within burst was not sent")
			}
		}
		if got := p.blockReplies.limiter.Tokens(); got != float64(peerByteBurst-15) {
			t.Fatalf("block reply allowance: %v", got)
		}
		if p.txReplies.limiter.Tokens() != peerByteBurst-5 {
			t.Fatal("pooled transactions did not use their own allowance")
		}
		if got := p.limits.gossipBytes.Tokens(); got != peerByteBurst {
			t.Fatalf("reply consumed gossip allowance: %v", got)
		}
		if p.blockReplies.bytes != 0 {
			t.Fatalf("sent replies retained %d bytes", p.blockReplies.bytes)
		}
	})
}

func TestPeerReplyThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		p.blockReplies.limiter.SetLimit(5)
		if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust reply allowance")
		}
		start := time.Now()
		codes := queueTestReplies(t, p)
		if time.Since(start) != 0 {
			t.Fatal("queuing blocked the protocol reader")
		}
		synctest.Wait()
		if len(rw.sent) != 1 || (<-rw.sent).Code != PooledTransactionsMsg {
			t.Fatal("block reply throttling blocked transaction replies")
		}
		for _, code := range codes[:3] {
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
		p.blockReplies.limiter.SetLimit(1)
		if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
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
				p.blockReplies.limiter.SetLimit(1)
				if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
					t.Fatal("could not exhaust reply allowance")
				}
				data, count := []rlp.RawValue(nil), peerRequestBurst+1
				if limit == "bytes" {
					encoded, err := rlp.EncodeToBytes(make([]byte, 8*1024*1024-13))
					if err != nil {
						t.Fatal(err)
					}
					data = []rlp.RawValue{encoded}
					count = (peerByteBurst / 2) / (8 * 1024 * 1024)
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
				if len(rw.sent) != 0 || p.blockReplies.bytes > peerByteBurst/2 {
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
		p.blockReplies.limiter.SetLimit(1)
		if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust reply allowance")
		}
		p.txReplies.limiter.SetLimit(1)
		if !p.txReplies.limiter.AllowN(time.Now(), peerByteBurst) {
			t.Fatal("could not exhaust transaction reply allowance")
		}
		queueTestReplies(t, p)
		synctest.Wait()
		p.Close()
		synctest.Wait()
		if p.blockReplies.queue != nil || p.blockReplies.bytes != 0 || p.txReplies.queue != nil || p.txReplies.bytes != 0 || len(rw.sent) != 0 {
			t.Fatal("close did not release pending replies")
		}
		if got := p.blockReplies.limiter.Tokens(); got != 0 {
			t.Fatalf("close did not refund reserved bytes: %v", got)
		}
		p.blockReplies.limiter.SetLimit(rate.Inf)
		if err := p.waitReplyAllowance(p.blockReplies.limiter, 1); !errors.Is(err, ErrDisconnected) {
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
		if p.blockReplies.queue != nil || p.blockReplies.bytes != 0 {
			t.Fatal("write failure did not release pending replies")
		}
		if err := p.ReplyReceiptsRLP(1, nil); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("reply queued after writer stopped: %v", err)
		}
	})
}

func TestPeerReplyConfiguredExemptions(t *testing.T) {
	for _, mode := range []string{"trusted", "static", "static inbound", "added inbound"} {
		t.Run(mode, func(t *testing.T) {
			rw := &replyTestRW{sent: make(chan p2p.Msg, 4)}
			p := NewPeer(ETH68, configuredLimitsTestPeer(t, mode), rw, nil)
			defer p.Close()
			p.blockReplies.limiter = rate.NewLimiter(0, 0)
			p.txReplies.limiter = rate.NewLimiter(0, 0)
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
			rw.err = io.ErrClosedPipe
			hash := common.Hash{1}
			if err := p.ReplyPooledTransactionsRLP(1, []common.Hash{hash}, nil); !errors.Is(err, io.ErrClosedPipe) || p.KnownTransaction(hash) {
				t.Fatal("failed direct reply marked a transaction known")
			}
		})
	}
}

func TestPeerPooledReplyKnownHashes(t *testing.T) {
	for _, mode := range []string{"accepted", "full", "oversized", "closed", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			p := limitedTestPeer()
			p.knownTxs, p.txReplies = newKnownCache(maxKnownTxs), newPeerReplies()
			var want error
			var data []rlp.RawValue
			switch mode {
			case "full":
				for range peerRequestBurst {
					if err := p.ReplyPooledTransactionsRLP(1, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				want = errReplyQueueFull
			case "oversized":
				data, want = []rlp.RawValue{make([]byte, maxMessageSize)}, errMsgTooLarge
			case "closed":
				p.Close()
				want = ErrDisconnected
			case "stopped":
				p.txReplies.queue, want = nil, ErrDisconnected
			}
			hash := common.Hash{1}
			if err := p.ReplyPooledTransactionsRLP(1, []common.Hash{hash}, data); !errors.Is(err, want) {
				t.Fatalf("reply error: got %v, want %v", err, want)
			}
			if p.KnownTransaction(hash) != (want == nil) {
				t.Fatal("known transaction tracking does not match reply acceptance")
			}
		})
	}
}

func TestPeerReplyValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		if err := p.waitReplyAllowance(p.blockReplies.limiter, peerByteBurst+1); !errors.Is(err, errMsgTooLarge) {
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

func TestPeerRepliesUnderConcurrentLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, rw := newReplyTestPeer(t)
		data, err := rlp.EncodeToBytes(make([]byte, 128*1024-13))
		if err != nil {
			t.Fatal(err)
		}
		body, err := rlp.EncodeToBytes(make([]byte, 256*1024-13))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 80*30; i++ {
			if err := p.checkMessageRate(GetPooledTransactionsMsg, 1, time.Now()); err != nil {
				t.Fatalf("honest fetcher rejected at request %d: %v", i, err)
			}
			if err := p.ReplyPooledTransactionsRLP(1, nil, []rlp.RawValue{data}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if i%2 == 0 {
				if err := p.checkMessageRate(GetBlockBodiesMsg, 1, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := p.ReplyBlockBodiesRLP(1, []rlp.RawValue{body}); err != nil {
					t.Fatal(err)
				}
				want++
			}
			synctest.Wait()
			if len(rw.sent) != want {
				t.Fatalf("transaction and block replies interfered at request %d", i)
			}
			for range want {
				<-rw.sent
			}
			time.Sleep(time.Second / 80)
		}
	})
}

type replyLookupPool struct {
	TxPool
	lookups int
	encoded []byte
}

func (p *replyLookupPool) GetRLP(common.Hash) []byte {
	p.lookups++
	return p.encoded
}

type replyLookupBackend struct {
	Backend
	pool TxPool
}

func (b *replyLookupBackend) TxPool() TxPool { return b.pool }

func TestPooledTransactionReplyBoundsLookups(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		size, lookups, replies int
	}{
		{"unknown", 0, maxPooledTxsServe, 0},
		{"small", 1, maxPooledTxsServe, maxPooledTxsServe},
		{"byte boundary", softResponseLimit, 1, 1},
		{"below byte boundary", softResponseLimit - 1, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &replyLookupPool{encoded: make([]byte, tc.size)}
			backend := &replyLookupBackend{pool: pool}
			hashes, txs := answerGetPooledTransactions(backend, make(GetPooledTransactionsRequest, maxPooledTxsServe+1))
			if pool.lookups != tc.lookups || len(hashes) != tc.replies || len(txs) != tc.replies {
				t.Fatalf("got %d lookups, %d hashes, %d transactions; want %d lookups, %d replies", pool.lookups, len(hashes), len(txs), tc.lookups, tc.replies)
			}
		})
	}
}
