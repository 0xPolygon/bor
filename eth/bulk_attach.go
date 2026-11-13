package eth

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/p2p"
)

type bulkChannelOpener interface {
	OpenChannelContext(context.Context, *p2p.Peer, string) (p2p.MsgReadWriter, error)
}

func attachBulkProtocols(ctx context.Context, sidecar bulkChannelOpener, peer *eth.Peer, snapPeer *snap.Peer, witPeer *wit.Peer) {
	var protocols sync.WaitGroup
	protocols.Go(func() {
		attachBulkChannels(ctx, sidecar, peer.Peer,
			[]string{"eth-control", "eth-blocks", "eth-tx", "eth-tx-fetch", "eth-bulk"}, peer.AttachBulkChannelRW)
	})
	if snapPeer != nil {
		protocols.Go(func() {
			attachBulkChannels(ctx, sidecar, snapPeer.Peer,
				[]string{"snap-accounts", "snap-storage", "snap-code", "snap-trie"}, snapPeer.AttachBulkChannelRW)
		})
	}
	if witPeer != nil {
		protocols.Go(func() {
			attachBulkChannels(ctx, sidecar, witPeer.Peer, []string{"wit-bulk"},
				func(_ string, rw p2p.MsgReadWriter) { witPeer.AttachBulkRW(rw) })
		})
	}
	protocols.Wait()
}

func attachBulkChannels(ctx context.Context, sidecar bulkChannelOpener, peer *p2p.Peer, channels []string, attach func(string, p2p.MsgReadWriter)) {
	// The negotiated lane set is fixed. Open each independently so a missed
	// hello cannot hold up other lanes, and join their bounded attempts.
	var lanes sync.WaitGroup
	for _, channel := range channels {
		lanes.Go(func() {
			attachBulkChannel(ctx, sidecar, peer, channel, attach)
		})
	}
	lanes.Wait()
}

func attachBulkChannel(ctx context.Context, sidecar bulkChannelOpener, peer *p2p.Peer, channel string, attach func(string, p2p.MsgReadWriter)) {
	if ctx.Err() != nil {
		return
	}
	rw, err := sidecar.OpenChannelContext(ctx, peer, channel)
	if err != nil {
		peer.Log().Debug("Bulk sidecar unavailable", "channel", channel, "err", err)
		return
	}
	if ctx.Err() != nil {
		if closer, ok := rw.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				peer.Log().Debug("Bulk sidecar channel close failed", "channel", channel, "err", err)
			}
		}
		return
	}
	attach(channel, rw)
}
