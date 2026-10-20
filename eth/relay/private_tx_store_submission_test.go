package relay

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/stretchr/testify/require"
)

func TestPrivateTxStoreSubmissionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		committed, first, second bool
	}{
		{name: "all rejected"},
		{name: "first accepted", first: true},
		{name: "second accepted", second: true},
		{name: "both accepted", first: true, second: true},
		{name: "existing private", committed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewPrivateTxStore()
			defer s.Close()
			hash := common.Hash{1}
			if tc.committed {
				s.Add(hash)
			}
			first, second := s.Begin(hash), s.Begin(hash)
			require.True(t, s.IsTxPrivate(hash))
			first(tc.first)
			require.True(t, s.IsTxPrivate(hash), "remaining admission must keep the hash private")
			second(tc.second)
			require.Equal(t, tc.committed || tc.first || tc.second, s.IsTxPrivate(hash))
			require.Empty(t, s.pending)
		})
	}
}

func TestPrivateTxStoreSubmissionConcurrentCompletion(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "all rejected", true: "one accepted"}[accepted], func(t *testing.T) {
			s := NewPrivateTxStore()
			defer s.Close()
			hash := common.Hash{1}
			finishes := make([]func(bool), 32)
			for i := range finishes {
				finishes[i] = s.Begin(hash)
			}
			var wg sync.WaitGroup
			for i, finish := range finishes {
				wg.Go(func() { finish(accepted && i == 0) })
			}
			wg.Wait()
			require.Equal(t, accepted, s.IsTxPrivate(hash))
			require.Empty(t, s.pending)
		})
	}
}

func TestPrivateTxStoreSubmissionFinishesOnce(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	first, second := s.Begin(hash), s.Begin(hash)
	first(false)
	first(true)
	require.True(t, s.IsTxPrivate(hash))
	require.Empty(t, s.txs, "repeated completion cannot commit a rejected submission")
	second(false)
	require.False(t, s.IsTxPrivate(hash))
	require.Empty(t, s.pending)
}

func TestPrivateTxStoreSubmissionPreservesExplicitCommit(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	finish := s.Begin(hash)
	s.Add(hash)
	finish(false)
	require.True(t, s.IsTxPrivate(hash))
	require.Empty(t, s.pending)
}

func TestPrivateTxStoreSubmissionPurgeInvalidates(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	old := s.Begin(hash)
	s.Purge(hash)
	current := s.Begin(hash)
	old(true)
	require.Empty(t, s.txs, "completion from before purge cannot commit a later reservation")
	current(false)
	require.False(t, s.IsTxPrivate(hash))
	require.Empty(t, s.pending)
}

func TestPrivateTxStoreSubmissionInclusionInvalidates(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	tx := types.NewTx(&types.LegacyTx{Nonce: 1})
	finish := s.Begin(tx.Hash())
	var feed event.Feed
	subscribed := make(chan struct{})
	s.SetchainEventSubFn(func(ch chan<- core.ChainEvent) event.Subscription {
		sub := feed.Subscribe(ch)
		close(subscribed)
		return sub
	})
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not subscribe")
	}
	feed.Send(core.ChainEvent{Transactions: types.Transactions{tx}})
	require.Eventually(t, func() bool { return !s.IsTxPrivate(tx.Hash()) }, time.Second, time.Millisecond)
	finish(true)
	require.False(t, s.IsTxPrivate(tx.Hash()), "included submission was recreated by its callback")
}

func TestPrivateTxStoreSubmissionProtectsSweep(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	s.Add(hash)
	s.mu.Lock()
	s.txs[hash] = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	finish := s.Begin(hash)
	s.SetTxPoolChecker(func(common.Hash) bool { return false })
	s.sweepOnce()
	require.True(t, s.IsTxPrivate(hash))
	require.Contains(t, s.txs, hash, "long-running admission lost its prior commitment")
	finish(false)
	require.True(t, s.IsTxPrivate(hash))
	s.sweepOnce()
	require.False(t, s.IsTxPrivate(hash), "completed failed reservation prevented absent-marker expiry")
}

func TestPrivateTxStoreSubmissionFailureDoesNotRefresh(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	addedAt := time.Now().Add(-time.Hour)
	s.mu.Lock()
	s.txs[hash] = addedAt
	s.mu.Unlock()
	s.SetTxPoolChecker(func(common.Hash) bool { return false })
	for range 32 {
		s.Begin(hash)(false)
	}
	s.mu.RLock()
	current := s.txs[hash]
	s.mu.RUnlock()
	require.Equal(t, addedAt, current, "rejected submissions renewed the absent commitment")
	s.sweepOnce()
	require.False(t, s.IsTxPrivate(hash), "rejected submissions postponed absent-marker cleanup")
}

func TestPrivateTxStoreSubmissionCommitProtectsStaleSweep(t *testing.T) {
	s := NewPrivateTxStore()
	defer s.Close()
	hash := common.Hash{1}
	s.mu.Lock()
	s.txs[hash] = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	checked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	s.SetTxPoolChecker(func(common.Hash) bool {
		close(checked)
		<-release
		return false
	})
	go func() {
		s.sweepOnce()
		close(done)
	}()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("sweep did not check the pool")
	}
	s.Begin(hash)(true)
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweep did not finish")
	}
	require.True(t, s.IsTxPrivate(hash), "stale pool absence removed a successful admission")
	require.Zero(t, s.txsExpired.Load())
}
