package p2p

import (
	"testing"

	"github.com/naoina/toml"
	"github.com/stretchr/testify/require"
)

func TestBulkConfigTOMLRoundTrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		want := Config{EnableBulkSidecar: enabled, BulkListenAddr: "127.0.0.1:30304", MaxPeers: 25}
		encoded, err := want.MarshalTOML()
		require.NoError(t, err)
		data, err := toml.Marshal(encoded)
		require.NoError(t, err)
		var got Config
		require.NoError(t, toml.Unmarshal(data, &got))
		require.Equal(t, want.EnableBulkSidecar, got.EnableBulkSidecar)
		require.Equal(t, want.BulkListenAddr, got.BulkListenAddr)
		require.Equal(t, want.MaxPeers, got.MaxPeers)
	}
}

func TestBulkConfigTOMLOmittedAndExplicit(t *testing.T) {
	config := Config{EnableBulkSidecar: true, BulkListenAddr: "127.0.0.1:30304"}
	require.NoError(t, toml.Unmarshal([]byte("MaxPeers = 10"), &config))
	require.True(t, config.EnableBulkSidecar)
	require.Equal(t, "127.0.0.1:30304", config.BulkListenAddr)
	require.NoError(t, toml.Unmarshal([]byte("EnableBulkSidecar = false\nBulkListenAddr = \"\""), &config))
	require.False(t, config.EnableBulkSidecar)
	require.Empty(t, config.BulkListenAddr)
	require.Error(t, toml.Unmarshal([]byte("EnableBulkSidecar = 3"), &config))
}
