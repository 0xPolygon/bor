package p2p

import (
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func TestBulkSidecarPeerRecord(t *testing.T) {
	remote := newTestBulkServer(t)
	t.Cleanup(remote.close)
	remote.setQUICPort()
	record := remote.localnode.Node()
	inbound := enode.NewV4(record.Pubkey(), net.IPv4(127, 0, 0, 1), 40000, 40000)
	for _, source := range []string{"database", "static", "trusted", "bootstrap", "bootstrap-v5", "unknown"} {
		t.Run(source, func(t *testing.T) {
			local := newTestBulkServer(t)
			t.Cleanup(local.close)
			switch source {
			case "database":
				require.NoError(t, local.db.UpdateNode(record))
			case "static":
				local.server.StaticNodes = []*enode.Node{record}
			case "trusted":
				local.server.TrustedNodes = []*enode.Node{record}
			case "bootstrap":
				local.server.BootstrapNodes = []*enode.Node{record}
			case "bootstrap-v5":
				local.server.BootstrapNodesV5 = []*enode.Node{record}
			}
			got := local.bulk.peerRecord(inbound)
			if source == "unknown" {
				require.Same(t, inbound, got)
			} else {
				require.Equal(t, record.String(), got.String())
			}
			require.Same(t, local.localnode.Node(), local.bulk.peerRecord(local.localnode.Node()))
		})
	}
}

func TestBulkSidecarSessionRecordUpdates(t *testing.T) {
	remote := newTestBulkServer(t)
	t.Cleanup(remote.close)
	remote.setQUICPort()
	old := remote.localnode.Node()
	require.Same(t, old, new(BulkSidecar).peerRecord(old))
	remote.localnode.Set(enr.QUIC(30305))
	latest := remote.localnode.Node()
	record := *latest.Record()
	record.Set(enr.QUIC(30306))
	record.SetSeq(latest.Seq())
	require.NoError(t, enode.SignV4(&record, remote.server.PrivateKey))
	sameSeq, err := enode.New(enode.ValidSchemes, &record)
	require.NoError(t, err)
	inbound := enode.NewV4(old.Pubkey(), old.IP(), 40000, 40000)
	local := newTestBulkServer(t)
	t.Cleanup(local.close)
	session := local.bulk.session(old)
	require.Same(t, latest, newerBulkPeerRecord(old, latest))
	require.Same(t, latest, newerBulkPeerRecord(latest, old))
	require.Same(t, latest, newerBulkPeerRecord(latest, sameSeq))
	require.Same(t, latest, newerBulkPeerRecord(latest, nil))
	require.Same(t, old, newerBulkPeerRecord(old, local.localnode.Node()))

	var wg sync.WaitGroup
	for _, record := range []*enode.Node{old, latest, inbound} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				got := local.bulk.session(record)
				got.lock.Lock()
				id := got.remote.ID()
				got.lock.Unlock()
				require.Equal(t, old.ID(), id)
			}
		}()
	}
	wg.Wait()
	require.Same(t, session, local.bulk.session(inbound))
	require.Same(t, session, local.bulk.session(sameSeq))
	require.Same(t, latest, session.remote)
}
