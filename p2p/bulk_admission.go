package p2p

import (
	"context"
	"errors"
	"sync"

	"github.com/quic-go/quic-go"
	"golang.org/x/sync/semaphore"
)

var errBulkAdmissionLimit = errors.New("bulk sidecar connection limit reached")

type bulkPendingAuthKey struct{}

func newBulkConnContext(maxPeers, maxPendingPeers int, windows *semaphore.Weighted) func(context.Context, *quic.ClientInfo) (context.Context, error) {
	if maxPendingPeers <= 0 {
		maxPendingPeers = defaultMaxPendingPeers
	}
	pending := make(chan struct{}, maxPendingPeers)
	connections := make(chan struct{}, bulkQUICWindowSlots(maxPeers, maxPendingPeers))
	return func(ctx context.Context, _ *quic.ClientInfo) (context.Context, error) {
		select {
		case pending <- struct{}{}:
		default:
			return nil, errBulkAdmissionLimit
		}
		select {
		case connections <- struct{}{}:
		default:
			<-pending
			return nil, errBulkAdmissionLimit
		}
		releaseWindow, err := reserveBulkQUICWindow(windows)
		if err != nil {
			<-connections
			<-pending
			return nil, err
		}
		releasePending := sync.OnceFunc(func() { <-pending })
		// QUIC cancels this context on handshake failure as well as connection close.
		context.AfterFunc(ctx, func() {
			releasePending()
			<-connections
			releaseWindow()
		})
		return context.WithValue(ctx, bulkPendingAuthKey{}, releasePending), nil
	}
}

func releaseBulkPendingAuth(ctx context.Context) {
	if release, ok := ctx.Value(bulkPendingAuthKey{}).(func()); ok {
		release()
	}
}
