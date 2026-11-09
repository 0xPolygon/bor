package p2p

import (
	"context"
	"errors"

	"github.com/quic-go/quic-go"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

var errBulkAdmissionLimit = errors.New("bulk sidecar connection limit reached")

type bulkPendingAuthKey struct{}

// Pending handshakes have global and per-source limits, independent of live peers.
func newBulkConnContext(maxPendingPeers int) func(context.Context, *quic.ClientInfo) (context.Context, error) {
	if maxPendingPeers <= 0 {
		maxPendingPeers = defaultMaxPendingPeers
	}
	pending := newBulkPendingAdmission(maxPendingPeers)
	return func(ctx context.Context, info *quic.ClientInfo) (context.Context, error) {
		releasePending, err := pending.acquire(info)
		if err != nil {
			return nil, err
		}
		// QUIC cancels this context on handshake failure as well as connection
		// close; releasePending is idempotent, so an early release on
		// authentication does not double-count here.
		context.AfterFunc(ctx, releasePending)
		return context.WithValue(ctx, bulkPendingAuthKey{}, releasePending), nil
	}
}

func releaseBulkPendingAuth(ctx context.Context) {
	if release, ok := ctx.Value(bulkPendingAuthKey{}).(func()); ok {
		release()
	}
}

// Acquire the identity lease before releasing pending capacity. RLPx admission
// remains authoritative for trusted peers and runtime peer-limit changes.
func (b *BulkSidecar) admitAuthenticatedConn(ctx context.Context, id enode.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, ok := b.leases.acquire(id)
	if !ok {
		return errBulkAdmissionLimit
	}
	context.AfterFunc(ctx, release)
	releaseBulkPendingAuth(ctx)
	return nil
}
