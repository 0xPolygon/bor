package p2p

import (
	"context"
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

	// Sized so the byte budget never binds before the connection-count limit in
	// newBulkConnContext does: 128 connections at the per-connection ceiling,
	// against a default MaxPeers+MaxPendingPeers of 100.
	bulkQUICBufferLimit = 128 * bulkConnReceiveWindowMax
)

// Reserve the entire connection receive window, including data not yet read
// into the application budget. Both inbound and outbound connections count.
var bulkQUICWindows = semaphore.NewWeighted(bulkQUICBufferLimit)

func reserveBulkQUICWindow() (func(), error) {
	if !bulkQUICWindows.TryAcquire(bulkConnReceiveWindowMax) {
		return nil, errBulkAdmissionLimit
	}
	return sync.OnceFunc(func() { bulkQUICWindows.Release(bulkConnReceiveWindowMax) }), nil
}

func bulkServerConnContext(srv *Server) func(context.Context, *quic.ClientInfo) (context.Context, error) {
	admit := newBulkConnContext(srv.MaxPeers, srv.MaxPendingPeers)
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
