package p2p

import (
	"context"
	"math"
	"net"
	"sync"

	"github.com/quic-go/quic-go"
	"golang.org/x/sync/semaphore"
)

const (
	// Initial windows are what every connection starts with; the max values are
	// the ceiling quic-go may auto-tune up to while a connection sustains
	// throughput. Initial must stay below max — pinning them equal disables
	// auto-tuning, which caps a lane at window/RTT and would leave the sidecar
	// slower than the RLPx lane it offloads on any high-latency link.
	bulkConnReceiveWindow      = 1024 * 1024
	bulkStreamReceiveWindow    = 512 * 1024
	bulkStreamReceiveWindowMax = 2 * 1024 * 1024
	bulkConnReceiveWindowMax   = 4 * 1024 * 1024
)

// Reserve full receive windows for both directions, using the same configured
// connection limit as admission. The budget includes unread transport bytes.
func newBulkQUICWindows(maxPeers, maxPendingPeers int) *semaphore.Weighted {
	slots := bulkQUICWindowSlots(maxPeers, maxPendingPeers)
	return semaphore.NewWeighted(int64(slots) * bulkConnReceiveWindowMax)
}

func bulkQUICWindowSlots(maxPeers, maxPendingPeers int) int {
	if maxPendingPeers <= 0 {
		maxPendingPeers = defaultMaxPendingPeers
	}
	// Both the channel capacity and the byte reservation must be representable.
	slots := uint64(max(0, maxPeers)) + uint64(maxPendingPeers)
	return int(min(slots, uint64(math.MaxInt), uint64(math.MaxInt64/bulkConnReceiveWindowMax)))
}

func reserveBulkQUICWindow(windows *semaphore.Weighted) (func(), error) {
	if !windows.TryAcquire(bulkConnReceiveWindowMax) {
		return nil, errBulkAdmissionLimit
	}
	return sync.OnceFunc(func() { windows.Release(bulkConnReceiveWindowMax) }), nil
}

func bulkServerConnContext(srv *Server, windows *semaphore.Weighted) func(context.Context, *quic.ClientInfo) (context.Context, error) {
	admit := newBulkConnContext(srv.MaxPeers, srv.MaxPendingPeers, windows)
	return func(ctx context.Context, info *quic.ClientInfo) (context.Context, error) {
		if srv.NetRestrict != nil {
			addr, ok := info.RemoteAddr.(*net.UDPAddr)
			if !ok || !srv.NetRestrict.Contains(addr.IP) {
				return nil, errNetRestrict
			}
		}
		return admit(ctx, info)
	}
}
