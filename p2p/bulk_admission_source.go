package p2p

import (
	"net"
	"net/netip"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/ethereum/go-ethereum/p2p/netutil"
)

const bulkPendingPerSource = 2

type bulkPendingAdmission struct {
	mu      sync.Mutex
	limit   int
	count   int
	sources map[netip.Addr]int
}

func newBulkPendingAdmission(limit int) *bulkPendingAdmission {
	return &bulkPendingAdmission{limit: limit, sources: make(map[netip.Addr]int)}
}

func (a *bulkPendingAdmission) acquire(info *quic.ClientInfo) (func(), error) {
	if info == nil {
		return nil, errBulkAdmissionLimit
	}
	addr, ok := info.RemoteAddr.(*net.UDPAddr)
	if !ok || addr == nil {
		return nil, errBulkAdmissionLimit
	}
	ip := addr.AddrPort().Addr().Unmap()
	if !ip.IsValid() || ip.IsUnspecified() {
		return nil, errBulkAdmissionLimit
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.count >= a.limit || (!netutil.AddrIsLAN(ip) && a.sources[ip] >= bulkPendingPerSource) {
		return nil, errBulkAdmissionLimit
	}
	a.count++
	a.sources[ip]++
	return sync.OnceFunc(func() { a.release(ip) }), nil
}

func (a *bulkPendingAdmission) release(ip netip.Addr) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count--
	if a.sources[ip]--; a.sources[ip] == 0 {
		delete(a.sources, ip)
	}
}
