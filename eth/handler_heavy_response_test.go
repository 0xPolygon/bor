package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/stretchr/testify/require"
)

func TestHandlerHeavyResponseLimiter(t *testing.T) {
	for _, test := range []struct {
		name   string
		server *p2p.Server
	}{
		{"no server", nil},
		{"disabled", &p2p.Server{}},
		{"enabled", &p2p.Server{Config: p2p.Config{EnableBulkSidecar: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
				config.p2pServer = test.server
				return config
			})
			t.Cleanup(first.close)
			second := newTestHandlerWithConfig(func(config *handlerConfig) *handlerConfig {
				config.p2pServer = test.server
				return config
			})
			t.Cleanup(second.close)
			if test.server != nil && test.server.EnableBulkSidecar {
				require.NotNil(t, first.handler.heavyResponseLimiter)
				require.NotNil(t, second.handler.heavyResponseLimiter)
				require.NotSame(t, first.handler.heavyResponseLimiter, second.handler.heavyResponseLimiter)
			} else {
				require.Nil(t, first.handler.heavyResponseLimiter)
				require.Nil(t, second.handler.heavyResponseLimiter)
			}
		})
	}
}
