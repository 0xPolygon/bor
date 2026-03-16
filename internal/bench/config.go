package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/cli/server"
)

// LoadConfig loads a Bor config file and applies benchmark-specific overrides.
// It returns the prepared config and a cleanup function for the temporary datadir.
func LoadConfig(opts Options) (*server.Config, func(), error) {
	loaded, err := server.ReadConfigFile(opts.ConfigPath)
	if err != nil {
		return nil, nil, WrapError(ErrCodeConfigLoad, "load config file", err)
	}

	cfg := server.DefaultConfig()
	if err := cfg.Merge(loaded); err != nil {
		return nil, nil, WrapError(ErrCodeConfigLoad, "merge config", err)
	}

	// Set genesis path
	cfg.Chain = opts.GenesisPath

	// Apply benchmark runtime overrides
	applyBenchmarkOverrides(cfg)

	// Derive etherbase from genesis if not set
	if cfg.Sealer.Etherbase == "" {
		if derived := deriveBenchmarkEtherbase(opts.GenesisPath); derived != "" {
			cfg.Sealer.Etherbase = derived
		}
	}

	// Set up datadir
	cleanup := func() {}
	if opts.DataDir != "" {
		cfg.DataDir = opts.DataDir
	} else {
		tempDir, err := os.MkdirTemp("", "bor-bench-*")
		if err != nil {
			return nil, nil, WrapError(ErrCodeConfigLoad, "create temp datadir", err)
		}
		cfg.DataDir = tempDir
		cleanup = func() { _ = os.RemoveAll(tempDir) }
	}

	return cfg, cleanup, nil
}

// applyBenchmarkOverrides forces benchmark-safe settings on the config.
func applyBenchmarkOverrides(cfg *server.Config) {
	// Consensus: no Heimdall, use fake author for block production
	cfg.Heimdall.Without = true
	cfg.DevFakeAuthor = true

	// Network isolation: no peers, no discovery
	cfg.P2P.MaxPeers = 0
	cfg.P2P.MaxPendPeers = 0
	cfg.P2P.NoDiscover = true
	cfg.P2P.Discovery.DiscoveryV4 = false
	cfg.P2P.Discovery.DiscoveryV5 = false

	// Disable telemetry
	cfg.Telemetry.Enabled = false

	// Disable gRPC
	cfg.GRPC.Addr = ""

	// Disable all RPC interfaces
	cfg.JsonRPC.IPCDisable = true
	cfg.JsonRPC.Http.Enabled = false
	cfg.JsonRPC.Ws.Enabled = false
	cfg.JsonRPC.Graphql.Enabled = false

	// Disable auto-mining; we start manually after preflight checks
	cfg.Sealer.Enabled = false

	// Skip pending-block snapshot updates — saves ~8% CPU.
	// The snapshot is only used by eth_getBlockByNumber("pending") and similar
	// RPC calls, which are disabled in benchmark mode.
	cfg.Sealer.DisableSnapshot = true

	// Disable FilterMaps log indexing - unnecessary overhead for benchmarking.
	// Saves ~6% CPU by eliminating the indexerLoop goroutine.
	if cfg.History != nil {
		cfg.History.LogNoHistory = true
	}

	// Disable transaction indexing entirely - avoids iterateTransactions overhead
	// (RLP decoding all block bodies for tx index updates, ~6% CPU).
	cfg.Cache.NoTxIndex = true

	// Skip writing receipts to DB - saves ~5% CPU from receipt RLP encoding.
	// Not needed since we don't query receipts in benchmark mode.
	cfg.Cache.SkipReceiptWrite = true

	// Skip writing block bodies to DB - saves ~6% CPU from body RLP encoding.
	// Blocks remain in the 256-entry LRU block cache for any reads needed
	// during the benchmark. Safe for short runs with < 256 blocks.
	cfg.Cache.SkipBodyWrite = true

	// Minimize state history retention for benchmarking.
	if cfg.History != nil {
		cfg.History.StateHistory = 1
	}

	// Skip all Bor fee transfer logs — eliminates ~11% CPU from topic hashing,
	// bloom computation, data allocation, and 4 extra GetBalance calls per tx.
	core.SkipBorFeeLogs = true

	// Skip receipt trie root computation — saves CPU from receipt DeriveSha.
	// In DevFakeAuthor mode nobody validates receipt roots.
	types.SkipReceiptRoot = true

	// Skip tx trie root computation — saves ~14% CPU from tx DeriveSha.
	// In DevFakeAuthor mode, mined blocks bypass ValidateBody so tx root is not checked.
	types.SkipTxRoot = true

	// Disable memory profiling to reduce allocation tracking overhead.
	runtime.MemProfileRate = 0
}

// genesisAlloc is a minimal struct for parsing genesis alloc addresses.
type genesisAlloc struct {
	Alloc map[string]json.RawMessage `json:"alloc"`
}

// deriveBenchmarkEtherbase attempts to derive a suitable etherbase address
// from the genesis file's alloc section. It prefers non-prefixed addresses
// (validator-style) over 0x-prefixed addresses, excluding known system contracts.
func deriveBenchmarkEtherbase(genesisPath string) string {
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		return ""
	}

	var g genesisAlloc
	if err := json.Unmarshal(data, &g); err != nil {
		return ""
	}

	if len(g.Alloc) == 0 {
		return ""
	}

	// Known Bor system contract addresses to exclude
	systemContracts := map[string]bool{
		"0000000000000000000000000000000000001000": true, // ValidatorContract
		"0000000000000000000000000000000000001001": true, // StateReceiverContract
		"0000000000000000000000000000000000001010": true, // MaticTokenContract
	}

	var nonPrefixed, prefixed []string

	for addr := range g.Alloc {
		orig := strings.TrimSpace(addr)
		normalized := strings.TrimPrefix(strings.ToLower(orig), "0x")

		// Skip invalid addresses
		if len(normalized) != 40 {
			continue
		}

		// Skip system contracts
		if systemContracts[normalized] {
			continue
		}

		// Categorize by original format
		if strings.HasPrefix(strings.ToLower(orig), "0x") {
			prefixed = append(prefixed, "0x"+normalized)
		} else {
			nonPrefixed = append(nonPrefixed, "0x"+normalized)
		}
	}

	// Sort for deterministic selection
	sort.Strings(nonPrefixed)
	sort.Strings(prefixed)

	// Prefer non-prefixed (validator-style) addresses
	if len(nonPrefixed) > 0 {
		return nonPrefixed[0]
	}
	if len(prefixed) > 0 {
		return prefixed[0]
	}

	return ""
}

// GetConfigEtherbase returns the configured etherbase address.
func GetConfigEtherbase(cfg *server.Config) string {
	if cfg.Sealer != nil {
		return cfg.Sealer.Etherbase
	}
	return ""
}

// ValidateConfig performs basic validation of the benchmark config.
func ValidateConfig(cfg *server.Config) error {
	if cfg.Chain == "" {
		return fmt.Errorf("genesis path (Chain) is required")
	}
	if cfg.DataDir == "" {
		return fmt.Errorf("datadir is required")
	}
	if !cfg.Heimdall.Without {
		return fmt.Errorf("benchmark requires bor.withoutheimdall=true")
	}
	if !cfg.DevFakeAuthor {
		return fmt.Errorf("benchmark requires bor.devfakeauthor=true")
	}
	return nil
}
