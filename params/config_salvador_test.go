package params

import (
	"fmt"
	"maps"
	"math/big"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// salvadorRecoveryGroups lists the addresses that get the Sweeper runtime;
// addresses in one group share one recovery address.
var salvadorRecoveryGroups = [][]common.Address{
	{
		common.HexToAddress("0xe4C4C39C1733e1C7c06B79f97ED442B68Df41EC6"),
		common.HexToAddress("0x5392587814Ce8c6af6789764f88e5e9AB0b9962C"),
		common.HexToAddress("0xc91ee0574a14446DE200923729c349B9CAE19b60"),
		common.HexToAddress("0x3f7019221a925537dC20767C38e80A7127fb1Ffa"),
	},
	{
		common.HexToAddress("0x5227f603C1eE34CE3C06eFe6477003BDfCCB2f8c"),
	},
}

// Recovery addresses the unscheduled presets carry until the real ones are known.
var salvadorPlaceholderRecoveries = []common.Address{
	common.HexToAddress("0x1111111111111111111111111111111111111111"),
	common.HexToAddress("0x2222222222222222222222222222222222222222"),
}

// RECOVERY immutable offsets in the Sweeper runtime built from
// the Sweeper contract with solc 0.8.30, 200 optimizer runs and no metadata hash.
var (
	salvadorRecoveryOffsets = []int{71, 148}

	// keccak256 of that runtime with both RECOVERY words zeroed.
	salvadorSweeperTemplateHash = common.HexToHash("0x61f6793ef0a5ccfc55186a2e53b489ff41f7d4598fd88230d367f02213dd5aab")
)

func TestIsSalvador(t *testing.T) {
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
		{"scheduled fork before activation", big.NewInt(100), 99, false},
		{"scheduled fork at activation", big.NewInt(100), 100, true},
		{"scheduled fork after activation", big.NewInt(100), 101, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &BorConfig{SalvadorBlock: tt.fork}
			require.Equal(t, tt.want, c.IsSalvador(big.NewInt(tt.number)))
		})
	}
}

func TestSalvadorBannerAndPresets(t *testing.T) {
	t.Parallel()

	cfg := *MergedTestChainConfig
	bor := *cfg.Bor
	cfg.Bor = &bor
	require.NotNil(t, bor.SalvadorBlock)
	require.Contains(t, cfg.Description(), " - Salvador:                    #0")

	bor.SalvadorBlock = nil
	require.NotContains(t, cfg.Description(), "Salvador")

	require.Nil(t, BorMainnetChainConfig.Bor.SalvadorBlock)
	require.Nil(t, AmoyChainConfig.Bor.SalvadorBlock)
}

// salvadorRecovery returns the recovery address built into code, or an error
// if code is not the reviewed Sweeper build with one address in every slot.
func salvadorRecovery(code []byte) (common.Address, error) {
	masked := slices.Clone(code)
	for _, off := range salvadorRecoveryOffsets {
		if len(masked) >= off+32 {
			clear(masked[off : off+32])
		}
	}
	if got := crypto.Keccak256Hash(masked); got != salvadorSweeperTemplateHash {
		return common.Address{}, fmt.Errorf("sweeper runtime %s does not match the reviewed build %s", got, salvadorSweeperTemplateHash)
	}
	recovery := immutableAt(code, salvadorRecoveryOffsets[0])
	for _, off := range salvadorRecoveryOffsets[1:] {
		if got := immutableAt(code, off); got != recovery {
			return common.Address{}, fmt.Errorf("sweeper RECOVERY at %d is %s, want %s", off, got, recovery)
		}
	}
	if recovery == (common.Address{}) {
		return common.Address{}, fmt.Errorf("sweeper RECOVERY is zero")
	}
	return recovery, nil
}

// checkSalvadorAlloc requires the Sweeper at every listed address, one recovery
// address per group and distinct across groups. Unscheduled presets keep the
// entries at "0"; a scheduled fork needs them at its own height with real
// recovery addresses.
func checkSalvadorAlloc(cfg *BorConfig) error {
	key := "0"
	if cfg.SalvadorBlock != nil {
		key = cfg.SalvadorBlock.String()
	}
	seen := make(map[common.Address]bool)
	for i, group := range salvadorRecoveryGroups {
		var recovery common.Address
		for _, addr := range group {
			code, ok := allocCode(cfg.BlockAlloc[key], addr)
			if !ok {
				return fmt.Errorf("blockAlloc %s has no entry for %s", key, addr)
			}
			got, err := salvadorRecovery(code)
			if err != nil {
				return fmt.Errorf("%s: %w", addr, err)
			}
			if recovery == (common.Address{}) {
				recovery = got
			} else if got != recovery {
				return fmt.Errorf("%s recovers to %s, rest of group %d to %s", addr, got, i, recovery)
			}
		}
		if seen[recovery] {
			return fmt.Errorf("group %d shares recovery address %s with another group", i, recovery)
		}
		seen[recovery] = true
		if cfg.SalvadorBlock != nil && slices.Contains(salvadorPlaceholderRecoveries, recovery) {
			return fmt.Errorf("salvadorBlock %s is scheduled with placeholder recovery %s", cfg.SalvadorBlock, recovery)
		}
	}
	return nil
}

func TestSalvadorBlockAlloc(t *testing.T) {
	t.Parallel()

	preset := BorMainnetChainConfig.Bor.BlockAlloc["0"].(map[string]interface{})
	codeAt := func(addr common.Address) string {
		return preset[addr.Hex()[2:]].(map[string]interface{})["code"].(string)
	}
	first := codeAt(salvadorRecoveryGroups[0][0])
	second := codeAt(salvadorRecoveryGroups[1][0])
	real1, real2 := common.HexToAddress("0xa11ce"), common.HexToAddress("0xb0b")

	allocWith := func(key string, codes map[common.Address]string) *BorConfig {
		accounts := make(map[string]interface{}, len(codes))
		for addr, code := range codes {
			accounts[addr.Hex()] = map[string]interface{}{"balance": "0x0", "code": code}
		}
		return &BorConfig{BlockAlloc: map[string]interface{}{key: accounts}}
	}
	codes := func(c1, c2 string) map[common.Address]string {
		out := make(map[common.Address]string)
		for _, addr := range salvadorRecoveryGroups[0] {
			out[addr] = c1
		}
		out[salvadorRecoveryGroups[1][0]] = c2
		return out
	}
	scheduled := func(cfg *BorConfig) *BorConfig {
		cfg.SalvadorBlock = big.NewInt(100)
		return cfg
	}
	withRecovery := func(code string, addr common.Address) string {
		for _, off := range salvadorRecoveryOffsets {
			code = patchImmutable(code, off, addr)
		}
		return code
	}
	realCodes := codes(withRecovery(first, real1), withRecovery(first, real2))
	split := codes(withRecovery(first, real1), withRecovery(first, real2))
	split[salvadorRecoveryGroups[0][3]] = withRecovery(first, real2)
	missing := codes(first, second)
	delete(missing, salvadorRecoveryGroups[0][2])

	tests := []struct {
		name    string
		cfg     *BorConfig
		wantErr string
	}{
		{"mainnet preset", BorMainnetChainConfig.Bor, ""},
		{"amoy preset", AmoyChainConfig.Bor, ""},
		{"scheduled with real recoveries", scheduled(allocWith("100", realCodes)), ""},
		{"scheduled with placeholders", scheduled(allocWith("100", codes(first, second))), "placeholder recovery"},
		{"scheduled but entries left at 0", scheduled(allocWith("0", realCodes)), "blockAlloc 100 has no entry"},
		{"missing address", allocWith("0", missing), "has no entry for " + salvadorRecoveryGroups[0][2].Hex()},
		{"group split across recoveries", allocWith("0", split), "rest of group 0"},
		{"groups share a recovery", allocWith("0", codes(first, first)), "shares recovery address"},
		{"one slot patched", allocWith("0", codes(patchImmutable(first, salvadorRecoveryOffsets[1], real1), second)), "RECOVERY at 148"},
		{"zero recovery", allocWith("0", codes(withRecovery(first, common.Address{}), second)), "RECOVERY is zero"},
		{"other code", allocWith("0", codes(flipByte(first, 10), second)), "does not match the reviewed build"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkSalvadorAlloc(tt.cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
	require.Len(t, slices.Collect(maps.Keys(preset)), 5)
}
