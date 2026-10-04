package eth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

type bulkTestOpener func(context.Context, *p2p.Peer, string) (p2p.MsgReadWriter, error)

func (open bulkTestOpener) OpenChannelContext(ctx context.Context, peer *p2p.Peer, name string) (p2p.MsgReadWriter, error) {
	return open(ctx, peer, name)
}

func TestBulkAttachLifecycle(t *testing.T) {
	for _, stage := range []string{"success", "before", "during", "error"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			peer := p2p.NewPeer(enode.ID{}, "bulk", nil)
			app, rw := p2p.MsgPipe()
			defer app.Close()
			defer rw.Close()
			var opened, attached []string
			open := bulkTestOpener(func(got context.Context, gotPeer *p2p.Peer, channel string) (p2p.MsgReadWriter, error) {
				require.Equal(t, ctx, got)
				require.Same(t, peer, gotPeer)
				opened = append(opened, channel)
				if stage == "during" {
					cancel()
				}
				if stage == "error" && channel == "eth-control" {
					return nil, errors.New("unavailable")
				}
				return rw, nil
			})
			if stage == "before" {
				cancel()
			}
			attachBulkChannels(ctx, open, peer, []string{"eth-control", "eth-bulk"}, func(channel string, got p2p.MsgReadWriter) {
				require.Same(t, rw, got)
				attached = append(attached, channel)
			})
			switch stage {
			case "before":
				require.Empty(t, opened)
				require.Empty(t, attached)
			case "during":
				require.Equal(t, []string{"eth-control"}, opened)
				require.Empty(t, attached)
				require.ErrorIs(t, rw.WriteMsg(p2p.Msg{}), p2p.ErrPipeClosed)
			case "error":
				require.Equal(t, []string{"eth-bulk"}, attached)
			case "success":
				require.Equal(t, []string{"eth-control", "eth-bulk"}, attached)
			}
		})
	}
}
