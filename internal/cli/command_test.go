package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMeta2Conn_NoToken: default behaviour (no token) keeps the original
// insecure-only dial. No regressions for the loopback no-auth flow.
func TestMeta2Conn_NoToken(t *testing.T) {
	t.Setenv("BOR_GRPC_TOKEN", "")
	m := &Meta2{addr: "127.0.0.1:3131"}
	conn, err := m.Conn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
}

// TestMeta2Conn_LoopbackWithToken: token attached over plaintext is allowed
// when the dial address is loopback (the typical same-host validator pair).
func TestMeta2Conn_LoopbackWithToken(t *testing.T) {
	t.Setenv("BOR_GRPC_TOKEN", "")
	m := &Meta2{addr: "127.0.0.1:3131", token: "secret"}
	conn, err := m.Conn()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
}

// TestMeta2Conn_NonLoopbackWithTokenRefused: refuse to send a bearer token in
// cleartext to a remote host. Mirrors the heimdall-side guarantee.
func TestMeta2Conn_NonLoopbackWithTokenRefused(t *testing.T) {
	t.Setenv("BOR_GRPC_TOKEN", "")
	m := &Meta2{addr: "bor.example.net:3131", token: "secret"}
	_, err := m.Conn()
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to send bearer token to non-loopback")
}

// TestMeta2Conn_EnvVarFallback: BOR_GRPC_TOKEN env var is used when --token is
// not passed. The non-loopback refusal applies to env-supplied tokens too.
func TestMeta2Conn_EnvVarFallback(t *testing.T) {
	t.Setenv("BOR_GRPC_TOKEN", "from-env")
	m := &Meta2{addr: "bor.example.net:3131"}
	_, err := m.Conn()
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to send bearer token to non-loopback")
}

// TestBearerCreds_GetRequestMetadata returns the correctly framed Authorization
// header with a Bearer scheme.
func TestBearerCreds_GetRequestMetadata(t *testing.T) {
	t.Parallel()

	md, err := bearerCreds{token: "abc123"}.GetRequestMetadata(nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer abc123", md["authorization"])
}
