package eth

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/crypto"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func policyTestServer(t *testing.T, trusted []*enode.Node, run func(*p2p.Peer, p2p.MsgReadWriter) error) *p2p.Server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	server := &p2p.Server{Config: p2p.Config{PrivateKey: key, MaxPeers: 4, NoDiscovery: true, ListenAddr: "127.0.0.1:0", TrustedNodes: trusted, Protocols: []p2p.Protocol{{Name: "policy", Version: 1, Length: 1, Run: run}}}}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	return server
}

func policyTestConnection(t *testing.T, mode string) *p2p.Peer {
	t.Helper()
	stop := make(chan struct{})
	remote := policyTestServer(t, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	var trusted []*enode.Node
	if mode == "trusted" {
		trusted = append(trusted, remote.Self())
	}
	connected := make(chan *p2p.Peer, 1)
	local := policyTestServer(t, trusted, func(p *p2p.Peer, _ p2p.MsgReadWriter) error { connected <- p; <-stop; return nil })
	t.Cleanup(func() { close(stop) })
	if mode == "static" {
		local.AddPeer(remote.Self())
	} else {
		remote.AddPeer(local.Self())
	}
	select {
	case peer := <-connected:
		return peer
	case <-time.After(5 * time.Second):
		t.Fatal("peer connection timed out")
		return nil
	}
}

type policyFaultRW struct{}

func (policyFaultRW) ReadMsg() (p2p.Msg, error) {
	return p2p.Msg{Code: 255, Payload: bytes.NewReader(nil)}, nil
}
func (policyFaultRW) WriteMsg(msg p2p.Msg) error { return msg.Discard() }

func TestPeerPolicyConnectionClasses(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated)), peers: newPeerSet()}
			peer := ethproto.NewPeer(ethproto.ETH68, policyTestConnection(t, mode), policyFaultRW{}, nil)
			defer peer.Close()
			if err := h.peers.registerPeer(peer, nil, nil); err != nil {
				t.Fatal(err)
			}
			want := uint64(0)
			if mode == "ordinary" {
				want = 60
			}
			h.peerPolicy.Observe(peer.ID(), peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
			h.observeProtocolPeer(peer)
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("initial classification: %+v", got)
			}
			h.peerPolicy = peerpolicy.New(new(mclock.Simulated))
			if err := ethproto.Handle(nil, peer); err == nil {
				t.Fatal("invalid framing accepted")
			}
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("protocol observation: %+v", got)
			}
			h.peerPolicy = peerpolicy.New(new(mclock.Simulated))
			h.observePeer(peer.ID(), peerpolicy.InvalidBlock)
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("async observation: %+v", got)
			}
		})
	}
}

func TestPeerContributionEvictionPreservesConnectionClass(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated)), peers: newPeerSet()}
			peer := ethproto.NewPeer(ethproto.ETH68, policyTestConnection(t, mode), policyFaultRW{}, nil)
			defer peer.Close()
			if err := h.peers.registerPeer(peer, nil, nil); err != nil {
				t.Fatal(err)
			}
			h.observeProtocolPeer(peer)
			for i := range 1024 {
				h.peerPolicy.Connected(fmt.Sprint(i))
			}
			if err := ethproto.Handle(nil, peer); err == nil {
				t.Fatal("invalid framing accepted")
			}
			got := h.peerPolicy.Snapshot(peer.ID())
			want := uint64(0)
			if mode == "ordinary" {
				want = 60
			}
			if got.Risk != want || got.Contribution.Protected != (mode != "ordinary") || got.Contribution.Status != "unknown" {
				t.Fatalf("evicted connection class/coverage: %+v, %+v", got, got.Contribution)
			}
		})
	}
}
