package heimdallgrpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestNewHeimdallGRPCClientInvalidRESTEndpoint(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "h3:///status-only", time.Second)
	require.ErrorContains(t, err, "empty host")
	require.Nil(t, client)
}
