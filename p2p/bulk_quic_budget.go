package p2p

import (
	"context"
	"net"

	"github.com/quic-go/quic-go"
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

// Pending handshakes are bounded separately from authenticated peers. Each
// connection retains its receive-window cap as the live peer count changes.
func bulkServerConnContext(srv *Server) func(context.Context, *quic.ClientInfo) (context.Context, error) {
	admit := newBulkConnContext(srv.MaxPendingPeers)
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
