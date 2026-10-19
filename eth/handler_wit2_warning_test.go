package eth

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

type witStrikeLogCapture struct {
	slog.Handler
	records chan slog.Record
}

func (c *witStrikeLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *witStrikeLogCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		c.records <- record.Clone()
	}
	return nil
}

func TestPeerPolicyWitStrikeWarning(t *testing.T) {
	for _, reason := range []peerpolicy.Reason{peerpolicy.InvalidWitnessAnnouncement, peerpolicy.InvalidWitnessBody} {
		t.Run(reason.String(), func(t *testing.T) {
			capture := &witStrikeLogCapture{Handler: log.DiscardHandler(), records: make(chan slog.Record, 16)}
			previous := log.Root()
			log.SetDefault(log.NewLogger(capture))
			defer log.SetDefault(previous)
			h := &handler{peers: newPeerSet(), wit2PeerTracker: newPeerWit2Tracker(), peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			strike := h.strikeWit2PeerByID
			if reason == peerpolicy.InvalidWitnessAnnouncement {
				strike = h.strikeWit2AnnouncementByID
			}
			for range wit2MisbehaviorStrikeLimit - 1 {
				strike("peer")
			}
			if len(capture.records) != 0 || h.peerPolicy.Snapshot("peer").Risk != 0 {
				t.Fatal("below-threshold strikes must remain neutral and quiet")
			}
			strike("peer")
			if len(capture.records) != 1 || h.peerPolicy.Snapshot("peer").Risk != 60 {
				t.Fatal("qualified strike must emit exactly one warning and its evidence")
			}
			record := <-capture.records
			attrs := make(map[string]string)
			record.Attrs(func(attr slog.Attr) bool { attrs[attr.Key] = attr.Value.String(); return true })
			if record.Level != slog.LevelWarn || attrs["peer"] != "peer" || attrs["reason"] != reason.String() {
				t.Fatalf("warning lacks qualified peer reason: %v", attrs)
			}
			strike("peer")
			if len(capture.records) != 0 {
				t.Fatal("next below-threshold strike emitted an extra warning")
			}
		})
	}
}
