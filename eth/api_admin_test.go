package eth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestAdminSidecarAPISurface(t *testing.T) {
	server := rpc.NewServer("test", 1, 5*time.Second)
	t.Cleanup(server.Stop)
	require.NoError(t, server.RegisterName("admin", NewAdminAPI(nil)))
	client := rpc.DialInProc(server)
	t.Cleanup(client.Close)
	var status p2p.BulkSidecarStatus
	require.NoError(t, client.Call(&status, "admin_bulkSidecarStatus"))
	require.Equal(t, p2p.BulkSidecarStatus{}, status)
	for _, method := range []string{
		"seedWitnessForHead", "seedSnapTriggerFixtures", "triggerTxGossip",
		"triggerBlockAnnouncement", "triggerTxFetch", "triggerBlockBodyFetch",
		"triggerSnapAccountRangeFetch", "triggerSnapStorageRangeFetch",
		"triggerSnapByteCodeFetch", "triggerSnapTrieNodeFetch",
		"triggerWitnessAnnouncement", "triggerWitnessMetadataFetch",
		"triggerWitnessMetadataFetchByHash", "triggerWitnessFetchByHash",
	} {
		var result interface{}
		err := client.Call(&result, "admin_"+method)
		var rpcErr rpc.Error
		require.ErrorAs(t, err, &rpcErr)
		require.Equal(t, -32601, rpcErr.ErrorCode(), method)
	}
}
