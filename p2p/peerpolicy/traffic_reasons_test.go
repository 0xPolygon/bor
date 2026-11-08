package peerpolicy

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyConcurrentTrafficReasons(t *testing.T) {
	metrics.Enable()
	for _, family := range []Family{BlockAnnouncements, TransactionAnnouncements} {
		for _, bytes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/bytes=%t", family, bytes), func(t *testing.T) {
				clock := new(mclock.Simulated)
				tracker := New(clock)
				event := Evidence{Family: family, Items: 1, Hashes: []common.Hash{{1}}}
				for range 34 {
					tracker.Observe("peer", event)
				}
				assertRisk(t, tracker, "peer", 0, "none")
				event.Items = allowances[family].items + 1
				if bytes {
					event.Items, event.Bytes = 1, allowances[family].bytes+1
				}
				for window := range 2 {
					beforeEvents := [2]int64{}
					beforeWindows := [2]int64{}
					reasons := [...]Reason{AnnouncementVolume, AnnouncementRepetition}
					for i, reason := range reasons {
						beforeEvents[i] = tracker.metrics.reasons[reason].Snapshot().Count()
						beforeWindows[i] = tracker.metrics.windows[reason].Snapshot().Count()
					}
					if window > 0 {
						clock.Run(windowWidth)
						for range 32 {
							tracker.Observe("peer", Evidence{Family: family, Hashes: event.Hashes})
						}
					}
					tracker.Observe("peer", event)
					tracker.Observe("peer", event)
					got := tracker.Snapshot("peer")
					require.Equal(t, uint64((window+1)*20), got.Risk)
					for i, reason := range reasons {
						require.Equal(t, uint64(window+1), got.Windows[reason.String()])
						require.Equal(t, int64(2), tracker.metrics.reasons[reason].Snapshot().Count()-beforeEvents[i])
						require.Equal(t, int64(1), tracker.metrics.windows[reason].Snapshot().Count()-beforeWindows[i])
					}
				}
				tracker.Observe("peer", Evidence{Reason: InvalidBlock})
				assertRisk(t, tracker, "peer", 80, "throttle")
				clock.Run(windowCount * windowWidth)
				require.Empty(t, tracker.Snapshot("peer").Windows)
				assertRisk(t, tracker, "peer", 0, "none")
			})
		}
	}
}
