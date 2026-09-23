package pathdb

import (
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// slowLockThreshold is the wait+hold time above which a pathdb lock site is
// logged. Normal acquisitions are microseconds; anything this slow is a stall.
const slowLockThreshold = 100 * time.Millisecond

var (
	lockWaitTimers = map[string]*metrics.Timer{}
	lockHoldTimers = map[string]*metrics.Timer{}
)

func init() {
	for _, site := range []string{
		"tree.add", "tree.cap", "tree.node", "tree.lookupAccount", "tree.lookupStorage", "tree.bottom",
		"disk.commit", "disk.node", "disk.account", "disk.storage", "disk.waitFlush",
	} {
		lockWaitTimers[site] = metrics.NewRegisteredTimer("pathdb/lock/"+site+"/wait", nil)
		lockHoldTimers[site] = metrics.NewRegisteredTimer("pathdb/lock/"+site+"/hold", nil)
	}
}

// recordLock records how long a lock site waited to acquire and how long it
// held the lock, logging it when the total crosses slowLockThreshold.
func recordLock(site string, wait, hold time.Duration) {
	lockWaitTimers[site].Update(wait)
	lockHoldTimers[site].Update(hold)
	if wait+hold >= slowLockThreshold {
		log.Warn("pathdb: slow lock", "site", site, "wait", wait, "hold", hold)
	}
}
