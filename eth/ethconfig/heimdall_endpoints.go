package ethconfig

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdallgrpc"
	"github.com/ethereum/go-ethereum/log"
)

// gRPC takes priority where configured, preserving the existing HTTP fallback.
func newHeimdallEndpoints(httpURLs, grpcAddrs []string, timeout time.Duration) ([]heimdall.Endpoint, error) {
	var clients []heimdall.Endpoint
	for i := 0; i < max(len(httpURLs), len(grpcAddrs)); i++ {
		client, err := newHeimdallEndpoint(httpURLs, grpcAddrs, i, timeout)
		if err != nil {
			for _, opened := range clients {
				opened.Close()
			}
			return nil, err
		}
		if client != nil {
			clients = append(clients, client)
		}
	}
	return clients, nil
}

func newHeimdallEndpoint(httpURLs, grpcAddrs []string, i int, timeout time.Duration) (heimdall.Endpoint, error) {
	if i < len(grpcAddrs) && grpcAddrs[i] != "" {
		var httpURL string
		if len(httpURLs) > 0 {
			httpURL = httpURLs[min(i, len(httpURLs)-1)]
		}
		client, err := heimdallgrpc.NewHeimdallGRPCClient(grpcAddrs[i], httpURL, timeout)
		if err == nil {
			return client, nil
		}
		log.Error("Failed to initialize Heimdall gRPC client; falling back to HTTP",
			"index", i, "grpc", grpcAddrs[i], "err", err)
	}
	if i >= len(httpURLs) {
		return nil, nil
	}
	client, err := heimdall.NewHeimdallClientWithError(httpURLs[i], timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Heimdall HTTP client for %q: %w", httpURLs[i], err)
	}
	return client, nil
}
