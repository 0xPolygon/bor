package params

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

var uburuStateReceiver = common.HexToAddress("0x0000000000000000000000000000000000001001")

// legacyBlockAllocHeights are code swaps, per chain ID, that shipped before every
// swap was tied to a named fork.
var legacyBlockAllocHeights = map[uint64]map[string]bool{
	137:   {"22156660": true}, // mainnet MRC20
	80002: {"12121856": true}, // amoy WPOL
}

// uburuRootSetters are the rootSetter immutables the Uburu StateReceiver must
// carry per chain ID.
var uburuRootSetters = map[uint64]common.Address{
	137:   common.HexToAddress("0xeb1cd9e44ab6bfe5a55ee96c468086e51b1b873a"),
	80002: common.HexToAddress("0xbe971fef2bb60f709e1daf3e55d00914e230cd94"),
}

// Immutable offsets in the StateReceiver runtime built from
// genesis-contracts-ghsa-g597-g75m-fm9x#1 at 8c07edc; a rebuild with other
// offsets fails here and needs a fresh check.
var (
	uburuRootSetterOffsets = []int{2833, 4731}
	uburuGasOracleOffsets  = []int{3655, 4025}

	// keccak256 of that runtime with every immutable word zeroed.
	uburuStateReceiverTemplateHash = common.HexToHash("0x9bf24607efd61134b0bee07495fdca58e5c7990855a43c79c0cf8e74aadb3d20")
)

func TestIsUburu(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fork   *big.Int
		number int64
		want   bool
	}{
		{"unset fork never active", nil, 0, false},
		{"unset fork never active at height", nil, 1_000_000, false},
		{"genesis activation at 0", big.NewInt(0), 0, true},
		{"genesis activation above 0", big.NewInt(0), 1, true},
		{"scheduled fork before activation", big.NewInt(100), 99, false},
		{"scheduled fork at activation", big.NewInt(100), 100, true},
		{"scheduled fork after activation", big.NewInt(100), 101, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &BorConfig{UburuBlock: tt.fork}
			require.Equal(t, tt.want, c.IsUburu(big.NewInt(tt.number)))
		})
	}
}

func TestUburuBannerAndPresets(t *testing.T) {
	t.Parallel()

	cfg := *MergedTestChainConfig
	bor := *cfg.Bor
	cfg.Bor = &bor
	require.NotNil(t, bor.UburuBlock)
	require.Contains(t, cfg.Description(), " - Uburu:                       #0")

	bor.UburuBlock = nil
	require.NotContains(t, cfg.Description(), "Uburu")

	// Mainnet and Amoy heights are not scheduled yet.
	require.Nil(t, BorMainnetChainConfig.Bor.UburuBlock)
	require.Nil(t, AmoyChainConfig.Bor.UburuBlock)
}

// configuredForkBlocks returns every exported *Block height set on cfg and cfg.Bor.
func configuredForkBlocks(cfg *ChainConfig) map[uint64]bool {
	out := make(map[uint64]bool)
	collect := func(v reflect.Value) {
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() || !strings.HasSuffix(field.Name, "Block") {
				continue
			}
			if b, ok := v.Field(i).Interface().(*big.Int); ok && b != nil {
				out[b.Uint64()] = true
			}
		}
	}
	collect(reflect.ValueOf(*cfg))
	collect(reflect.ValueOf(*cfg.Bor))
	return out
}

func allocCode(alloc interface{}, want common.Address) ([]byte, bool) {
	accounts, ok := alloc.(map[string]interface{})
	if !ok {
		return nil, false
	}
	for addr, account := range accounts {
		if common.HexToAddress(addr) != want {
			continue
		}
		fields, _ := account.(map[string]interface{})
		code, _ := fields["code"].(string)
		return common.FromHex(code), true
	}
	return nil, false
}

func immutableAt(code []byte, offset int) common.Address {
	if len(code) < offset+32 {
		return common.Address{}
	}
	return common.BytesToAddress(code[offset : offset+32])
}

func maskedUburuRuntimeHash(code []byte) common.Hash {
	masked := slices.Clone(code)
	for _, off := range append(slices.Clone(uburuRootSetterOffsets), uburuGasOracleOffsets...) {
		if len(masked) >= off+32 {
			clear(masked[off : off+32])
		}
	}
	return crypto.Keccak256Hash(masked)
}

// checkUburuRuntime requires the reviewed StateReceiver build with the network
// rootSetter and one non-zero gas oracle in every immutable slot, so neither
// other code, another network's build nor a placeholder-oracle build can be
// scheduled.
func checkUburuRuntime(code []byte, rootSetter common.Address) error {
	if got := maskedUburuRuntimeHash(code); got != uburuStateReceiverTemplateHash {
		return fmt.Errorf("uburu StateReceiver runtime %s does not match the reviewed build %s", got, uburuStateReceiverTemplateHash)
	}
	for _, off := range uburuRootSetterOffsets {
		if got := immutableAt(code, off); got != rootSetter {
			return fmt.Errorf("uburu StateReceiver rootSetter at %d is %s, want %s", off, got, rootSetter)
		}
	}
	oracle := immutableAt(code, uburuGasOracleOffsets[0])
	if oracle == (common.Address{}) {
		return fmt.Errorf("uburu StateReceiver gasOracle is zero")
	}
	for _, off := range uburuGasOracleOffsets[1:] {
		if got := immutableAt(code, off); got != oracle {
			return fmt.Errorf("uburu StateReceiver gasOracle at %d is %s, want %s", off, got, oracle)
		}
	}
	return nil
}

// checkBlockAllocForks requires every BlockAlloc height to be a configured fork
// block, and a scheduled Uburu to carry the StateReceiver swap at its own height.
func checkBlockAllocForks(cfg *ChainConfig) error {
	forks := configuredForkBlocks(cfg)
	for key := range cfg.Bor.BlockAlloc {
		height, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			return fmt.Errorf("blockAlloc key %q: %w", key, err)
		}
		if !forks[height] && !legacyBlockAllocHeights[cfg.ChainID.Uint64()][key] {
			return fmt.Errorf("blockAlloc key %s is not a configured fork block", key)
		}
	}
	if cfg.Bor.UburuBlock == nil {
		return nil
	}
	code, ok := allocCode(cfg.Bor.BlockAlloc[cfg.Bor.UburuBlock.String()], uburuStateReceiver)
	if !ok {
		return fmt.Errorf("uburuBlock %s has no blockAlloc entry for %s", cfg.Bor.UburuBlock, uburuStateReceiver)
	}
	if len(code) == 0 {
		return fmt.Errorf("uburuBlock %s has empty code for %s", cfg.Bor.UburuBlock, uburuStateReceiver)
	}
	if rootSetter, ok := uburuRootSetters[cfg.ChainID.Uint64()]; ok {
		return checkUburuRuntime(code, rootSetter)
	}
	return nil
}

func TestBlockAllocKeysAreForkBlocks(t *testing.T) {
	t.Parallel()

	stateReceiverAt := func(height string, addr string) map[string]interface{} {
		return map[string]interface{}{
			height: map[string]interface{}{
				addr: map[string]interface{}{"balance": "0x0", "code": "0x00"},
			},
		}
	}
	uburuAt := func(fork int64, alloc map[string]interface{}) *ChainConfig {
		return &ChainConfig{ChainID: big.NewInt(1), Bor: &BorConfig{UburuBlock: big.NewInt(fork), BlockAlloc: alloc}}
	}
	mainnetUburuAt := func(code string) *ChainConfig {
		cfg := uburuAt(100, stateReceiverAt("100", "0x0000000000000000000000000000000000001001"))
		cfg.ChainID = big.NewInt(137)
		cfg.Bor.BlockAlloc["100"].(map[string]interface{})["0x0000000000000000000000000000000000001001"].(map[string]interface{})["code"] = code
		return cfg
	}
	oracle := common.HexToAddress("0x5eed")
	mainnetBuild := uburuRuntime(t, uburuRootSetters[137], oracle)

	tests := []struct {
		name    string
		cfg     *ChainConfig
		wantErr string
	}{
		{"mainnet preset", BorMainnetChainConfig, ""},
		{"amoy preset", AmoyChainConfig, ""},
		{"uburu swap at fork block", uburuAt(100, stateReceiverAt("100", "0x0000000000000000000000000000000000001001")), ""},
		{"uburu swap with unprefixed address", uburuAt(100, stateReceiverAt("100", "0000000000000000000000000000000000001001")), ""},
		{"uburu swap one block late", uburuAt(100, stateReceiverAt("101", "0x0000000000000000000000000000000000001001")), "blockAlloc key 101"},
		{"uburu without swap", uburuAt(100, nil), "has no blockAlloc entry"},
		{"uburu swap on another address", uburuAt(100, stateReceiverAt("100", "0x0000000000000000000000000000000000001010")), "has no blockAlloc entry"},
		{"non-numeric key", uburuAt(100, stateReceiverAt("0x64", "0x0000000000000000000000000000000000001001")), "blockAlloc key \"0x64\""},
		{"empty swap code", uburuAt(100, map[string]interface{}{"100": map[string]interface{}{"0x0000000000000000000000000000000000001001": map[string]interface{}{"balance": "0x0", "code": "0x"}}}), "has empty code"},
		{"mainnet build", mainnetUburuAt(mainnetBuild), ""},
		{"mainnet with amoy build", mainnetUburuAt(uburuRuntime(t, uburuRootSetters[80002], oracle)), "rootSetter at 2833"},
		{"mainnet with zero oracle", mainnetUburuAt(uburuRuntime(t, uburuRootSetters[137], common.Address{})), "gasOracle is zero"},
		{"mainnet with placeholder fixture", mainnetUburuAt(uburuOracle0Fixture(t)), "gasOracle is zero"},
		{"mainnet with split oracle", mainnetUburuAt(patchImmutable(mainnetBuild, uburuGasOracleOffsets[1], common.HexToAddress("0xbad"))), "gasOracle at 4025"},
		{"mainnet with other code", mainnetUburuAt(flipByte(mainnetBuild, 100)), "does not match the reviewed build"},
		{"mainnet with legacy code", mainnetUburuAt("0x6080"), "does not match the reviewed build"},
		{"amoy legacy key on mainnet", &ChainConfig{ChainID: big.NewInt(137), Bor: &BorConfig{BlockAlloc: stateReceiverAt("12121856", "0x0000000000000000000000000000000000001001")}}, "blockAlloc key 12121856"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBlockAllocForks(tt.cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestUburuFromGenesisJSON decodes the genesis fields a devnet sets to schedule
// Uburu with its StateReceiver swap.
func TestUburuFromGenesisJSON(t *testing.T) {
	t.Parallel()

	raw := `{"chainId": 4927, "bor": {"uburuBlock": 256, "blockAlloc": {"256": {"0x0000000000000000000000000000000000001001": {"balance": "0x0", "code": "0x6080"}}}}}`
	var cfg ChainConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))

	require.False(t, cfg.Bor.IsUburu(big.NewInt(255)))
	require.True(t, cfg.Bor.IsUburu(big.NewInt(256)))
	require.NoError(t, checkBlockAllocForks(&cfg))
}

func patchImmutable(code string, offset int, addr common.Address) string {
	raw := common.FromHex(code)
	copy(raw[offset:offset+32], common.BytesToHash(addr.Bytes()).Bytes())
	return hexutil.Encode(raw)
}

// uburuOracle0Fixture is the mainnet Uburu StateReceiver runtime built with a
// zero gas oracle, shared with the consensus/bor execution test.
func uburuOracle0Fixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../consensus/bor/testdata/statereceiver-mainnet-uburu-oracle0.hex")
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

// uburuRuntime returns the reviewed runtime with the given immutables.
func uburuRuntime(t *testing.T, rootSetter, oracle common.Address) string {
	t.Helper()
	code := uburuOracle0Fixture(t)
	for _, off := range uburuRootSetterOffsets {
		code = patchImmutable(code, off, rootSetter)
	}
	for _, off := range uburuGasOracleOffsets {
		code = patchImmutable(code, off, oracle)
	}
	return code
}

func flipByte(code string, offset int) string {
	raw := common.FromHex(code)
	raw[offset] ^= 0xff
	return hexutil.Encode(raw)
}
