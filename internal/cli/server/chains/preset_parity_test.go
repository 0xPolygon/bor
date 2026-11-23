package chains

import (
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type presetSurfaces struct {
	params  *params.ChainConfig
	runtime *params.ChainConfig
	genesis string
}

func networkPresets() map[string]presetSurfaces {
	return map[string]presetSurfaces{
		"mainnet": {params.BorMainnetChainConfig, mainnetBor.Genesis.Config, "../../../../builder/files/genesis-mainnet-v1.json"},
		"amoy":    {params.AmoyChainConfig, amoyTestnet.Genesis.Config, "../../../../builder/files/genesis-amoy.json"},
	}
}

func loadPackagedGenesis(t *testing.T, path string) *params.ChainConfig {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var g core.Genesis
	require.NoError(t, json.Unmarshal(raw, &g))
	require.NotNil(t, g.Config)
	require.NotNil(t, g.Config.Bor)
	return g.Config
}

func borForkBlocks(cfg *params.BorConfig) map[string]*big.Int {
	out := make(map[string]*big.Int)
	v := reflect.ValueOf(*cfg)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if b, ok := v.Field(i).Interface().(*big.Int); ok && strings.HasSuffix(name, "Block") {
			out[name] = b
		}
	}
	return out
}

func decodeBlockAlloc(t *testing.T, cfg *params.BorConfig) map[string]types.GenesisAlloc {
	t.Helper()
	out := make(map[string]types.GenesisAlloc, len(cfg.BlockAlloc))
	for height, alloc := range cfg.BlockAlloc {
		raw, err := json.Marshal(alloc)
		require.NoError(t, err)
		var decoded types.GenesisAlloc
		require.NoError(t, json.Unmarshal(raw, &decoded), "blockAlloc %s", height)
		out[height] = decoded
	}
	return out
}

// TestBorForkPresetParity requires the params preset, the server runtime preset
// and the packaged genesis to agree on every Bor fork block and code swap, so a
// fork set on only one surface cannot stay inactive on another startup path.
func TestBorForkPresetParity(t *testing.T) {
	t.Parallel()

	for network, p := range networkPresets() {
		t.Run(network, func(t *testing.T) {
			t.Parallel()
			packaged := loadPackagedGenesis(t, p.genesis)

			want := borForkBlocks(p.params.Bor)
			require.Contains(t, want, "UburuBlock")
			for surface, cfg := range map[string]*params.ChainConfig{"runtime": p.runtime, "genesis": packaged} {
				got := borForkBlocks(cfg.Bor)
				for name, block := range want {
					require.True(t, sameBlock(block, got[name]), "%s %s: params=%v %s=%v", network, name, block, surface, got[name])
				}
				require.Equal(t, decodeBlockAlloc(t, p.params.Bor), decodeBlockAlloc(t, cfg.Bor), "%s blockAlloc params vs %s", network, surface)
			}
		})
	}
}

func sameBlock(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Cmp(b) == 0
}
