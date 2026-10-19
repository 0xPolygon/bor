package eth

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

type transactionObservationBackend struct {
	Backend
	accept     bool
	deliveries int
}

func (b *transactionObservationBackend) AcceptTxs() bool { return b.accept }
func (b *transactionObservationBackend) Handle(*Peer, Packet) error {
	b.deliveries++
	return nil
}

func TestPeerPolicyIgnoredTransactions(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for code, handle := range map[uint64]msgHandler{TransactionsMsg: handleTransactions, PooledTransactionsMsg: handlePooledTransactions} {
			t.Run(fmt.Sprintf("%d/observer=%t", code, enabled), func(t *testing.T) {
				tracker := peerpolicy.New(new(mclock.Simulated))
				backend := new(transactionObservationBackend)
				peer := &Peer{}
				observations := 0
				if enabled {
					peer.SetObserver(func(e peerpolicy.Evidence) {
						observations++
						if e.Reason == peerpolicy.UnknownTransactionDelivery {
							if code != PooledTransactionsMsg || e.Bytes != 0 || e.Items != 0 {
								t.Fatalf("incorrect diagnostic: %+v", e)
							}
						} else if e.Family != peerpolicy.Transactions || e.Reason != peerpolicy.None || e.Items != 0 || e.Bytes != 8<<20 {
							t.Fatalf("unexpected ignored delivery: %+v", e)
						}
						tracker.Observe("sync-peer", e)
					})
				}
				for delivery := 1; delivery <= 9; delivery++ {
					payload := bytes.NewReader([]byte{0xff})
					msg := p2p.Msg{Code: code, Size: 8 << 20, Payload: payload}
					if err := peer.handleObserved(backend, msg, handle); err != nil {
						t.Fatal(err)
					}
					if payload.Len() != 1 || backend.deliveries != 0 {
						t.Fatal("ignored packet was decoded or forwarded")
					}
					want := uint64(0)
					if enabled && delivery == 9 {
						want = 20
					}
					if got := tracker.Snapshot("sync-peer"); got.Risk != want || got.Windows[peerpolicy.TransactionVolume.String()] != want/20 {
						t.Fatalf("delivery %d: %+v, want risk %d", delivery, got, want)
					}
				}
				wantObservations := 9
				if code == PooledTransactionsMsg {
					wantObservations = 18
				}
				if enabled && observations != wantObservations {
					t.Fatalf("observations=%d want=%d", observations, wantObservations)
				}
				if enabled && code == PooledTransactionsMsg && tracker.Snapshot("sync-peer").Windows[peerpolicy.UnknownTransactionDelivery.String()] != 1 {
					t.Fatal("missing provenance diagnostic")
				}
			})
		}
	}
}

func TestPeerPolicyAcceptedTransactionsDeferCorrelation(t *testing.T) {
	for _, tc := range []struct {
		code   uint64
		packet Packet
		handle msgHandler
	}{
		{TransactionsMsg, &TransactionsPacket{}, handleTransactions},
		{PooledTransactionsMsg, &PooledTransactionsPacket{RequestId: 42}, handlePooledTransactions},
	} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			encoded, err := rlp.EncodeToBytes(tc.packet)
			if err != nil {
				t.Fatal(err)
			}
			backend := &transactionObservationBackend{accept: true}
			var events []peerpolicy.Evidence
			peer := &Peer{observer: func(e peerpolicy.Evidence) { events = append(events, e) }}
			msg := p2p.Msg{Code: tc.code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
			if err := peer.handleObserved(backend, msg, tc.handle); err != nil {
				t.Fatal(err)
			}
			if backend.deliveries != 1 || len(events) != 1 {
				t.Fatalf("accepted delivery bypassed fetcher correlation: deliveries=%d events=%+v", backend.deliveries, events)
			}
			event := events[0]
			if event.Items != 0 || event.Bytes != 0 || event.Reason != peerpolicy.None || event.Activity != (peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedBytes: uint64(len(encoded))}) {
				t.Fatalf("forwarded delivery counted risk traffic or lost wire activity: %+v", event)
			}
		})
	}
}
