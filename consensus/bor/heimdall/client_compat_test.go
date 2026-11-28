package heimdall_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
)

func TestHeimdallHTTPTransportPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/checkpoints/count", r.URL.Path)
		_, err := io.WriteString(w, `{"ack_count":"42"}`)
		require.NoError(t, err)
	}))
	defer server.Close()
	client, err := heimdall.NewHeimdallClientWithError(server.URL, time.Second)
	require.NoError(t, err)
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	count, err := client.FetchCheckpointCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(42), count)
}

func TestHeimdallConstructorCompatibility(t *testing.T) {
	var constructor func(string, time.Duration) *heimdall.HeimdallClient = heimdall.NewHeimdallClient
	for _, endpoint := range []string{"http://localhost:1317", "h3://localhost:1317", "h3:///status-only", "://invalid"} {
		t.Run(endpoint, func(t *testing.T) {
			client := constructor(endpoint, time.Second)
			require.NotNil(t, client)
			t.Cleanup(client.Close)
		})
	}
}

func TestHeimdallConstructorValidation(t *testing.T) {
	for _, endpoint := range []string{"h3:///status-only", "://invalid"} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := heimdall.NewHeimdallClientWithError(endpoint, time.Second)
			require.Error(t, err)
			require.Nil(t, client)

			legacy := heimdall.NewHeimdallClient(endpoint, time.Second)
			t.Cleanup(legacy.Close)
			_, err = legacy.FetchStatus(t.Context())
			require.Error(t, err)
		})
	}
}

func TestHeimdallEndpointSchemesAndHosts(t *testing.T) {
	for _, endpoint := range []string{"", "localhost:1317", "/status", "http:///status", "https:///status", "http://:1317", "https://[]:1317", "ftp://localhost", "file:///status", "mailto:user@example.com", "http://localhost:bad"} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := heimdall.NewHeimdallClientWithError(endpoint, time.Second)
			require.Error(t, err)
			require.Nil(t, client)
			legacy := heimdall.NewHeimdallClient(endpoint, time.Second)
			require.NotNil(t, legacy)
			legacy.Close()
		})
	}
	for _, scheme := range []string{"http", "https", "h3"} {
		for _, host := range []string{"localhost:1317", "127.0.0.1:1317", "[::1]:1317", "heimdall.example"} {
			t.Run(scheme+"/"+host, func(t *testing.T) {
				client, err := heimdall.NewHeimdallClientWithError(scheme+"://"+host+"/api", time.Second)
				require.NoError(t, err)
				require.NotNil(t, client)
				client.Close()
			})
		}
	}
}
