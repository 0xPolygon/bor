package heimdall_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
)

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
