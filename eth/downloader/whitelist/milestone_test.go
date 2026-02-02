package whitelist

import (
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestUnlockSprintThreshold(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	svc := NewService(db, false, 0)

	m, ok := svc.milestoneService.(*milestone)
	if !ok {
		t.Fatalf("expected milestoneService to be *milestone, got %T", svc.milestoneService)
	}

	m.finality.Lock()
	m.Locked = true
	m.LockedMilestoneNumber = 10
	m.LockedMilestoneHash = common.Hash{0x42}
	m.LockedMilestoneIDs = map[string]struct{}{
		"id1": {},
		"id2": {},
	}
	if err := rawdb.WriteLockField(db, m.Locked, m.LockedMilestoneNumber, m.LockedMilestoneHash, m.LockedMilestoneIDs); err != nil {
		t.Fatalf("failed to write initial lock field: %v", err)
	}
	m.finality.Unlock()

	// endBlockNum < LockedMilestoneNumber → should remain locked
	m.UnlockSprint(9)

	m.finality.RLock()
	if !m.Locked {
		t.Fatalf("expected Locked to remain true when endBlockNum < LockedMilestoneNumber")
	}
	if len(m.LockedMilestoneIDs) != 2 {
		t.Fatalf("expected LockedMilestoneIDs to remain untouched for endBlockNum < LockedMilestoneNumber")
	}
	m.finality.RUnlock()

	// endBlockNum >= LockedMilestoneNumber → should unlock & clear IDs
	m.UnlockSprint(10)

	m.finality.RLock()
	if m.Locked {
		t.Fatalf("expected Locked to be false when endBlockNum >= LockedMilestoneNumber")
	}
	if len(m.LockedMilestoneIDs) != 0 {
		t.Fatalf("expected LockedMilestoneIDs to be cleared when unlocking sprint")
	}
	m.finality.RUnlock()
}

// TestMilestoneUnlockSprintRace exercises concurrent readers and writers
// of milestone lock state and future milestone lists.
//
// Run with `go test -race ./eth/downloader/whitelist -run TestMilestoneUnlockSprintRace -v`
// to detect any data races involving milestone.go.
func TestMilestoneUnlockSprintRace(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	svc := NewService(db, false, 0)

	m, ok := svc.milestoneService.(*milestone)
	if !ok {
		t.Fatalf("expected milestoneService to be *milestone, got %T", svc.milestoneService)
	}

	// Initialise lock state under write lock.
	m.finality.Lock()
	m.Locked = true
	m.LockedMilestoneNumber = 2
	m.LockedMilestoneHash = common.Hash{0x01}
	if m.LockedMilestoneIDs == nil {
		m.LockedMilestoneIDs = make(map[string]struct{})
	}
	m.LockedMilestoneIDs["milestoneID1"] = struct{}{}

	if m.FutureMilestoneList == nil {
		m.FutureMilestoneList = make(map[uint64]common.Hash)
	}
	if m.FutureMilestoneOrder == nil {
		m.FutureMilestoneOrder = make([]uint64, 0)
	}

	if err := rawdb.WriteLockField(db, m.Locked, m.LockedMilestoneNumber, m.LockedMilestoneHash, m.LockedMilestoneIDs); err != nil {
		t.Fatalf("failed to write initial lock field: %v", err)
	}
	m.finality.Unlock()

	// Build a small chain.
	var chain []*types.Header
	for i := uint64(1); i <= 4; i++ {
		h := &types.Header{Number: new(big.Int).SetUint64(i)}
		chain = append(chain, h)
	}
	current := chain[len(chain)-1]

	var (
		stop int32
		wg   sync.WaitGroup
	)

	// Goroutine 1: repeatedly call IsValidChain (read side).
	wg.Add(1)
	go func() {
		defer wg.Done()
		var iterations int64

		for atomic.LoadInt32(&stop) == 0 {
			_, err := m.IsValidChain(current, chain)
			if err != nil {
				t.Logf("IsValidChain error: %v", err)
			}

			iterations++
			if iterations%1_000 == 0 {
				locked := m.Locked
				t.Logf("IsValidChain iteration=%d, Locked=%v", iterations, locked)
			}
		}
	}()

	// Goroutine 2: repeatedly unlock sprint (write side).
	wg.Add(1)
	go func() {
		defer wg.Done()
		var iterations int64

		for atomic.LoadInt32(&stop) == 0 {
			m.UnlockSprint(4)

			iterations++
			if iterations%1_000 == 0 {
				t.Logf("UnlockSprint iteration=%d", iterations)
			}
		}
	}()

	// Goroutine 3: repeatedly process future milestones (write side).
	wg.Add(1)
	go func() {
		defer wg.Done()
		var (
			iterations int64
			num        uint64 = 3
		)

		for atomic.LoadInt32(&stop) == 0 {
			m.ProcessFutureMilestone(num, common.Hash{0x02})
			num++

			iterations++
			if iterations%1_000 == 0 {
				t.Logf("ProcessFutureMilestone iteration=%d, lastNum=%d", iterations, num-1)
			}
		}
	}()

	// Goroutine 4: repeatedly read milestone IDs list (read side).
	wg.Add(1)
	go func() {
		defer wg.Done()
		var iterations int64

		for atomic.LoadInt32(&stop) == 0 {
			ids := m.GetMilestoneIDsList()
			_ = ids // just to keep compiler happy

			iterations++
			if iterations%1_000 == 0 {
				t.Logf("GetMilestoneIDsList iteration=%d, len=%d", iterations, len(ids))
			}
		}
	}()

	runFor := 2 * time.Second
	t.Logf("Starting milestone race test for %s (run with -race to detect data races)", runFor)
	time.Sleep(runFor)
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
}
