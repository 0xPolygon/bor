// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/log"
)

func TestPeerPolicyRiskTransitionBandsAndExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	steps := []struct {
		advance time.Duration
		reason  Reason
		risk    int
	}{
		{0, RateLimited, 20},
		{time.Second, RateLimited, -1},
		{9 * time.Second, InvalidBlock, -1},
		{20 * time.Second, None, 80},
		{10 * time.Second, InvalidBlock, -1},
		{20 * time.Second, None, 100},
		{30 * time.Second, None, 60},
		{30 * time.Second, None, 0},
	}
	var previous uint64
	for i, step := range steps {
		clock.Run(step.advance)
		if step.reason != None {
			tracker.Observe("peer", Evidence{Reason: step.reason})
		}
		entries := tracker.drainTransitions()
		if step.risk < 0 {
			if len(entries) != 0 {
				t.Fatalf("step %d: unexpected entries: %+v", i, entries)
			}
			continue
		}
		entry := singleTransition(t, entries)
		if entry.peerID != "peer" || entry.current.risk != uint64(step.risk) || entry.previous.risk != previous || entry.changes != 1 {
			t.Fatalf("step %d: incorrect transition: %+v", i, entry)
		}
		if entry.current.reasons == 0 {
			t.Fatalf("step %d: transition lost reasons", i)
		}
		previous = entry.current.risk
	}
	if got := tracker.drainTransitions(); len(got) != 0 {
		t.Fatalf("recovery repeated: %+v", got)
	}
}

func TestPeerPolicyRiskTransitionCoalescesPeakAndRecovery(t *testing.T) {
	for _, initialDrain := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_log_%t", initialDrain), func(t *testing.T) {
			clock := new(mclock.Simulated)
			tracker := New(clock)
			tracker.Observe("peer", Evidence{Reason: RateLimited})
			var previous uint64
			wantChanges := uint64(4)
			if initialDrain {
				previous = singleTransition(t, tracker.drainTransitions()).current.risk
				wantChanges--
			}
			clock.Run(10 * time.Second)
			tracker.Observe("peer", Evidence{Reason: InvalidBlock})
			clock.Run(10 * time.Second)
			tracker.Observe("peer", Evidence{Reason: InvalidBlock})
			clock.Run(60 * time.Second)
			entry := singleTransition(t, tracker.drainTransitions())
			if entry.current.risk != 0 || entry.previous.risk != previous || entry.peak.risk != 100 || entry.changes != wantChanges {
				t.Fatalf("coalesced transition = %+v", entry)
			}
			if entry.current.reasons != entry.peak.reasons || transitionReasons(entry.current.reasons) != "invalid-block,rate-limited" {
				t.Fatalf("recovery lost reasons: %+v", entry)
			}
		})
	}
}

func TestPeerPolicyRiskTransitionExcludesExpiredReasons(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	singleTransition(t, tracker.drainTransitions())
	for range 2 {
		clock.Run(windowWidth)
		tracker.Observe("peer", Evidence{Reason: RateLimited})
	}
	clock.Run(windowWidth)
	if entry := singleTransition(t, tracker.drainTransitions()); entry.current.risk != 100 {
		t.Fatalf("escalation = %+v", entry)
	}
	clock.Run(30 * time.Second)
	entry := singleTransition(t, tracker.drainTransitions())
	if entry.current.risk != 40 || transitionReasons(entry.current.reasons) != "rate-limited" {
		t.Fatalf("expired evidence remained in current reasons: %+v", entry)
	}
}

func TestPeerPolicyRiskTransitionIncludesAllReasonsAtPeak(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	tracker.Observe("peer", Evidence{Reason: InvalidEncoding})
	entry := singleTransition(t, tracker.drainTransitions())
	if entry.current.risk != 60 || entry.peak.risk != 60 || entry.changes != 1 {
		t.Fatalf("same-band evidence changed the transition: %+v", entry)
	}
	if transitionReasons(entry.peak.reasons) != "invalid-encoding,invalid-block" || entry.current.reasons != entry.peak.reasons {
		t.Fatalf("equal peak lost current reasons: %+v", entry)
	}
}

func TestPeerPolicyRiskTransitionRenewedEvidenceAfterExpiry(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.Observe("peer", Evidence{Reason: RateLimited})
	singleTransition(t, tracker.drainTransitions())
	clock.Run(windowCount * windowWidth)
	tracker.Observe("peer", Evidence{Reason: AnnouncementVolume})
	entry := singleTransition(t, tracker.drainTransitions())
	if entry.previous.risk != 20 || entry.current.risk != 20 || entry.changes != 2 {
		t.Fatalf("expiry and renewed evidence were lost: %+v", entry)
	}
	if transitionReasons(entry.current.reasons) != "announcement-volume" {
		t.Fatalf("renewed evidence retained expired reasons: %+v", entry)
	}
}

func TestPeerPolicyRiskTransitionProtectedPeers(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.ObserveWithClass("protected", true, Evidence{Reason: InvalidBlock})
	if entries := tracker.drainTransitions(); len(entries) != 0 {
		t.Fatalf("protected peer produced transitions: %+v", entries)
	}
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	singleTransition(t, tracker.drainTransitions())
	tracker.SetClassResolver(func(string) (bool, bool) { return true, true })
	clock.Run(peerTransitionInterval)
	entry := singleTransition(t, tracker.drainTransitions())
	if !entry.protected || entry.current.risk != 0 || entry.previous.risk != 60 || transitionReasons(entry.current.reasons) != "invalid-block" {
		t.Fatalf("protection recovery = %+v", entry)
	}
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	clock.Run(peerTransitionInterval)
	if entries := tracker.drainTransitions(); len(entries) != 0 {
		t.Fatalf("protected evidence produced transitions: %+v", entries)
	}
}

func TestPeerPolicyRiskTransitionGlobalBudget(t *testing.T) {
	clock := new(mclock.Simulated)
	clock.Run(2 * time.Hour)
	tracker := New(clock)
	for i := range 2 * maxPeers {
		tracker.Observe(fmt.Sprint(i), Evidence{Reason: InvalidBlock})
	}
	if tracker.peers.Len() != maxPeers {
		t.Fatalf("peer state grew beyond limit: %d", tracker.peers.Len())
	}
	count := 0
	for range transitionLogLimit {
		entries := tracker.drainTransitions()
		if len(entries) > transitionLogBatch {
			t.Fatalf("batch exceeded limit: %d", len(entries))
		}
		count += len(entries)
	}
	if count != transitionLogLimit || len(tracker.transitions.recent) > 2*transitionLogLimit {
		t.Fatalf("log count %d, recent identities %d", count, len(tracker.transitions.recent))
	}
	for i := range maxPeers {
		tracker.Observe(fmt.Sprintf("new-%d", i), Evidence{Reason: InvalidBlock})
	}
	if entries := tracker.drainTransitions(); len(entries) != 0 {
		t.Fatalf("identity churn reset global budget: %+v", entries)
	}
	clock.Run(transitionLogWindow - time.Nanosecond)
	if entries := tracker.drainTransitions(); len(entries) != 0 {
		t.Fatalf("global budget reset early: %+v", entries)
	}
	clock.Run(time.Nanosecond)
	if entries := tracker.drainTransitions(); len(entries) != transitionLogBatch {
		t.Fatalf("budget did not reset: %d entries", len(entries))
	}
}

func TestPeerPolicyRiskTransitionPeerBudgetSurvivesEviction(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.Observe("peer", Evidence{Reason: RateLimited})
	singleTransition(t, tracker.drainTransitions())
	for i := range maxPeers {
		tracker.Observe(fmt.Sprint(i), Evidence{})
	}
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	clock.Run(peerTransitionInterval - time.Nanosecond)
	if entries := tracker.drainTransitions(); len(entries) != 0 {
		t.Fatalf("record eviction reset peer budget: %+v", entries)
	}
	clock.Run(time.Nanosecond)
	if entry := singleTransition(t, tracker.drainTransitions()); entry.current.risk != 60 {
		t.Fatalf("pending transition lost: %+v", entry)
	}
}

func TestPeerPolicyRiskTransitionLoggerExpiryAndShutdown(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	entries := make(transitionLogWriter, 16)
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.JSONHandler(entries)))
	t.Cleanup(func() { log.SetDefault(previousLogger) })
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tracker.RunTransitionLogger(done)
	}()
	stop := sync.OnceFunc(func() { close(done); waitTransitionWorker(t, stopped) })
	t.Cleanup(stop)
	tracker.Observe("peer", Evidence{Reason: RateLimited})
	waitTransitionTimer(t, clock)
	clock.Run(transitionPollInterval)
	if entry := receiveTransitionLog(t, entries); entry["risk"] != float64(20) || entry["peer_id"] != "peer" {
		t.Fatalf("onset = %+v", entry)
	}
	waitTransitionTimer(t, clock)
	clock.Run(time.Minute)
	if entry := receiveTransitionLog(t, entries); entry["risk"] != float64(0) || entry["previous_risk"] != float64(20) {
		t.Fatalf("idle recovery = %+v", entry)
	}
	stop()
	if clock.ActiveTimers() != 0 {
		t.Fatal("logger left a scheduled timer after shutdown")
	}
}

func TestPeerPolicyRiskTransitionLoggerDoesNotBlockObservation(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	entered, release := make(chan struct{}), make(chan struct{})
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tracker.runTransitionLogger(done, func(riskTransition) { close(entered); <-release })
	}()
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	waitTransitionTimer(t, clock)
	clock.Run(transitionPollInterval)
	waitTransitionWorker(t, entered)
	observed := make(chan struct{})
	go func() {
		tracker.Observe("peer", Evidence{Reason: InvalidBlock})
		tracker.Snapshot("peer")
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		close(release)
		close(done)
		t.Fatal("blocked log sink stalled tracker")
	}
	close(done)
	close(release)
	waitTransitionWorker(t, stopped)
}

func TestPeerPolicyRiskTransitionConcurrentObservation(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for i := range 100 {
				id := fmt.Sprint(worker, "-", i)
				tracker.Observe(id, Evidence{Reason: InvalidBlock})
				tracker.Snapshot(id)
				tracker.SetExempt(id, i%2 == 0)
			}
		})
	}
	wg.Go(func() {
		for range 100 {
			tracker.drainTransitions()
			clock.Run(time.Second)
		}
	})
	wg.Wait()
	if tracker.peers.Len() != 400 || tracker.transitions.emitted > transitionLogLimit {
		t.Fatalf("concurrent observation lost peers or exceeded logging budget: peers=%d logs=%d", tracker.peers.Len(), tracker.transitions.emitted)
	}
}

func TestPeerPolicyRiskTransitionLogFields(t *testing.T) {
	var output bytes.Buffer
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.JSONHandler(&output)))
	t.Cleanup(func() { log.SetDefault(previousLogger) })
	writeRiskTransition(riskTransition{
		peerID: "peer", current: riskSample{0, 1 << InvalidBlock},
		previous: riskSample{20, 1 << RateLimited}, peak: riskSample{100, 1 << InvalidBlock},
		changes: 3, protected: true,
	})
	var fields map[string]any
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatalf("invalid structured log: %v", err)
	}
	for name, want := range map[string]any{
		"peer_id": "peer", "mode": "observe", "score_type": "abuse", "risk": float64(0),
		"previous_risk": float64(20), "from_band": "positive", "to_band": "zero", "would_action": "none",
		"reasons": "invalid-block", "peak_risk": float64(100), "peak_reasons": "invalid-block",
		"transition_count": float64(3), "protected": true,
	} {
		if got := fields[name]; got != want {
			t.Errorf("field %q = %v, want %v", name, got, want)
		}
	}
}

func singleTransition(t *testing.T, entries []riskTransition) riskTransition {
	t.Helper()
	if len(entries) != 1 {
		t.Fatalf("got %d transitions, want one: %+v", len(entries), entries)
	}
	return entries[0]
}

type transitionLogWriter chan []byte

func (w transitionLogWriter) Write(data []byte) (int, error) {
	w <- append([]byte(nil), data...)
	return len(data), nil
}

func receiveTransitionLog(t *testing.T, entries <-chan []byte) map[string]any {
	t.Helper()
	select {
	case entry := <-entries:
		var fields map[string]any
		if err := json.Unmarshal(entry, &fields); err != nil {
			t.Fatalf("invalid transition log: %v", err)
		}
		return fields
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for transition")
		return nil
	}
}

func waitTransitionWorker(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transition worker did not complete")
	}
}

func waitTransitionTimer(t *testing.T, clock *mclock.Simulated) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for clock.ActiveTimers() == 0 {
		select {
		case <-timeout.C:
			t.Fatal("transition worker did not schedule a timer")
		case <-poll.C:
		}
	}
}
