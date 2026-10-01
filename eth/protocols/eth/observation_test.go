package eth

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestPeerPolicyMessageObservation(t *testing.T) {
	var got []peerpolicy.Evidence
	peer := &Peer{}
	peer.SetObserver(func(e peerpolicy.Evidence) { got = append(got, e) })
	wantErr := errors.New("local failure")
	err := peer.handleObserved(nil, p2p.Msg{Code: NewBlockMsg, Size: 123}, func(_ Backend, msg Decoder, _ *Peer) error {
		observeInvalid(msg, peerpolicy.InvalidBlock)
		return wantErr
	})
	if !errors.Is(err, wantErr) || len(got) != 1 {
		t.Fatalf("changed handler result: %v %+v", err, got)
	}
	if got[0].Reason != peerpolicy.InvalidBlock || got[0].Family != peerpolicy.Blocks || got[0].Bytes != 123 {
		t.Fatalf("wrong evidence: %+v", got[0])
	}
}

func TestPeerPolicyDecodeAndLocalErrors(t *testing.T) {
	for _, decode := range []bool{false, true} {
		var got peerpolicy.Evidence
		p := &Peer{observer: func(e peerpolicy.Evidence) { got = e }}
		err := p.handleObserved(nil, p2p.Msg{Payload: bytes.NewReader(nil)}, func(_ Backend, msg Decoder, _ *Peer) error {
			if decode {
				return msg.Decode(new(GetBlockBodiesPacket))
			}
			return errors.New("database unavailable")
		})
		if err == nil {
			t.Fatal("expected error")
		}
		want := peerpolicy.None
		if decode {
			want = peerpolicy.InvalidEncoding
		}
		if got.Reason != want {
			t.Fatalf("reason = %v want %v", got.Reason, want)
		}
	}
}

func TestPeerPolicyDisabledPreservesDecoder(t *testing.T) {
	peer := &Peer{}
	called := false
	err := peer.handleObserved(nil, p2p.Msg{}, func(_ Backend, msg Decoder, _ *Peer) error {
		_, called = msg.(p2p.Msg)
		return nil
	})
	if err != nil || !called {
		t.Fatal("disabled observation changed decoder")
	}
}

func TestPeerPolicyAnnouncementCapture(t *testing.T) {
	d := &observedDecoder{}
	packet := make(NewBlockHashesPacket, 1024)
	packet[0].Hash = common.Hash{1}
	observeBlockAnnouncements(d, packet)
	if d.evidence.Items != 1024 || len(d.evidence.Hashes) != 128 || d.evidence.Hashes[0] != packet[0].Hash {
		t.Fatal("block announcement capture was not bounded")
	}
	hashes := []common.Hash{{2}, {3}}
	observeAnnouncements(d, hashes)
	if d.evidence.Items != 2 || d.evidence.Hashes[1] != hashes[1] {
		t.Fatal("missing hashes")
	}
}

func TestPeerPolicyMessageFamilies(t *testing.T) {
	for code, want := range map[uint64]peerpolicy.Family{
		NewBlockMsg: peerpolicy.Blocks, NewBlockHashesMsg: peerpolicy.BlockAnnouncements,
		NewPooledTransactionHashesMsg: peerpolicy.TransactionAnnouncements,
		GetBlockHeadersMsg:            peerpolicy.Requests, GetBlockBodiesMsg: peerpolicy.Requests,
		GetReceiptsMsg: peerpolicy.Requests, BlockBodiesMsg: peerpolicy.Other,
		TransactionsMsg: peerpolicy.Other, PooledTransactionsMsg: peerpolicy.Other,
	} {
		if got := messageFamily(code); got != want {
			t.Fatalf("%d: %v != %v", code, got, want)
		}
	}
}

type observationReader struct{ p2p.Msg }

func (r observationReader) ReadMsg() (p2p.Msg, error) { return r.Msg, nil }
func (r observationReader) WriteMsg(p2p.Msg) error    { return errors.New("not used") }

func TestPeerPolicyFraming(t *testing.T) {
	for _, msg := range []p2p.Msg{
		{Code: NewBlockMsg, Size: maxMessageSize + 1},
		{Code: 0xff, Payload: bytes.NewReader(nil)},
	} {
		var got peerpolicy.Evidence
		p := &Peer{rw: observationReader{msg}, version: ETH68}
		p.SetObserver(func(e peerpolicy.Evidence) { got = e })
		if err := handleMessage(nil, p); err == nil {
			t.Fatal("expected framing failure")
		}
		if got.Reason != peerpolicy.InvalidEncoding || got.Bytes != uint64(msg.Size) {
			t.Fatalf("wrong framing evidence: %+v", got)
		}
	}
}
