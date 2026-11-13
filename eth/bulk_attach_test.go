package eth

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/log"
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
			var mu sync.Mutex
			open := bulkTestOpener(func(got context.Context, gotPeer *p2p.Peer, channel string) (p2p.MsgReadWriter, error) {
				mu.Lock()
				defer mu.Unlock()
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
				mu.Lock()
				defer mu.Unlock()
				require.Same(t, rw, got)
				attached = append(attached, channel)
			})
			switch stage {
			case "before":
				require.Empty(t, opened)
				require.Empty(t, attached)
			case "during":
				require.NotEmpty(t, opened)
				require.Empty(t, attached)
				require.ErrorIs(t, rw.WriteMsg(p2p.Msg{}), p2p.ErrPipeClosed)
			case "error":
				require.Equal(t, []string{"eth-bulk"}, attached)
			case "success":
				require.ElementsMatch(t, []string{"eth-control", "eth-bulk"}, attached)
			}
		})
	}
}

func TestBulkAttachIndependentLanes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := p2p.NewPeer(enode.ID{}, "bulk", nil)
	app, rw := p2p.MsgPipe()
	defer app.Close()
	defer rw.Close()
	blocked, release := make(chan struct{}), make(chan struct{})
	done, attached := make(chan struct{}), make(chan string, 4)
	open := bulkTestOpener(func(ctx context.Context, _ *p2p.Peer, channel string) (p2p.MsgReadWriter, error) {
		if channel == "eth-control" {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, context.DeadlineExceeded
		}
		return rw, nil
	})
	go func() {
		defer close(done)
		attachBulkChannels(ctx, open, peer,
			[]string{"eth-control", "eth-blocks", "eth-tx", "eth-tx-fetch", "eth-bulk"},
			func(channel string, _ p2p.MsgReadWriter) { attached <- channel })
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("control lane did not start")
	}
	var got []string
	for range 4 {
		select {
		case channel := <-attached:
			got = append(got, channel)
		case <-time.After(time.Second):
			t.Fatal("later lanes waited for the stalled control lane")
		}
	}
	require.ElementsMatch(t, []string{"eth-blocks", "eth-tx", "eth-tx-fetch", "eth-bulk"}, got)
	select {
	case <-done:
		t.Fatal("attachment returned before all lane workers finished")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attachment did not finish after the missed lane failed")
	}
}

type bulkCloseTestRW struct {
	p2p.MsgReadWriter
	closed bool
	err    error
}

func (rw *bulkCloseTestRW) Close() error {
	rw.closed = true
	return rw.err
}

func TestBulkAttachErrors(t *testing.T) {
	failure := errors.New("transport failure")
	for _, test := range []struct {
		name     string
		openErr  error
		closeErr error
		message  string
	}{
		{name: "open", openErr: failure, message: "Bulk sidecar unavailable"},
		{name: "close", closeErr: failure, message: "Bulk sidecar channel close failed"},
		{name: "closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Root()
			log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(&output, log.LevelDebug, false)))
			defer log.SetDefault(previous)
			peer := p2p.NewPeer(enode.ID{}, "bulk", nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rw := &bulkCloseTestRW{err: test.closeErr}
			open := bulkTestOpener(func(context.Context, *p2p.Peer, string) (p2p.MsgReadWriter, error) {
				cancel()
				return rw, test.openErr
			})
			attachBulkChannels(ctx, open, peer, []string{"eth-control"}, func(string, p2p.MsgReadWriter) {
				t.Error("cancelled lane was attached")
			})
			require.Equal(t, test.openErr == nil, rw.closed)
			if test.message == "" {
				require.Empty(t, output.String())
			} else {
				require.Contains(t, output.String(), test.message)
				require.Contains(t, output.String(), "eth-control")
				require.Contains(t, output.String(), failure.Error())
			}
		})
	}
}
