package ethconfig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdallgrpc"
)

func TestHeimdallEndpoints(t *testing.T) {
	for _, test := range []struct {
		name       string
		http, grpc []string
		count      int
		invalid    bool
	}{
		{name: "none"},
		{name: "http", http: []string{"http://localhost:1317"}, count: 1},
		{name: "h3 failover", http: []string{"h3://localhost:1317", "http://localhost:1318"}, count: 2},
		{name: "grpc", http: []string{"h3://localhost:1317"}, grpc: []string{"http://localhost:9090"}, count: 1},
		{name: "grpc missing", http: []string{"http://localhost:1317"}, grpc: []string{""}, count: 1},
		{name: "grpc failure", http: []string{"http://localhost:1317"}, grpc: []string{"http://192.0.2.1:9090"}, count: 1},
		{name: "grpc failure without fallback", grpc: []string{"http://192.0.2.1:9090"}},
		{name: "invalid later endpoint", http: []string{"h3://localhost:1317", "h3:///missing"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clients, err := newHeimdallEndpoints(test.http, test.grpc, time.Second)
			for _, client := range clients {
				t.Cleanup(client.Close)
			}
			if test.invalid {
				require.Error(t, err)
				require.Nil(t, clients)
				return
			}
			require.NoError(t, err)
			require.Len(t, clients, test.count)
			if test.name == "grpc" {
				require.IsType(t, &heimdallgrpc.HeimdallGRPCClient{}, clients[0])
			} else if test.count > 0 {
				require.IsType(t, &heimdall.HeimdallClient{}, clients[0])
			}
		})
	}
}
