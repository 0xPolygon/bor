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
// group i sweeps to salvadorRecoveries[i].
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

var salvadorRecoveries = []common.Address{
	common.HexToAddress("0x3dcE0a29139A851Da1dFCa56Af8e8a6440b4D952"),
	common.HexToAddress("0xd9fD39103674CB061D64D9C3b73B878Dbe610b80"),
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

	require.Equal(t, big.NewInt(98350200), BorMainnetChainConfig.Bor.SalvadorBlock)
	require.Equal(t, big.NewInt(53803400), AmoyChainConfig.Bor.SalvadorBlock)
	require.Contains(t, BorMainnetChainConfig.Description(), " - Salvador:                    #98350200")

	// Live tracers attribute BlockAlloc code changes at a sprint start to the state-sync tx.
	for _, c := range []*BorConfig{BorMainnetChainConfig.Bor, AmoyChainConfig.Bor} {
		require.False(t, c.IsSprintStart(c.SalvadorBlock.Uint64()), "salvadorBlock %s starts a sprint", c.SalvadorBlock)
	}
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

// checkSalvadorAlloc requires the reviewed Sweeper build at every listed
// address, sweeping to its group's recovery address, at the SalvadorBlock key.
func checkSalvadorAlloc(cfg *BorConfig) error {
	if cfg.SalvadorBlock == nil {
		return fmt.Errorf("salvadorBlock is not set")
	}
	key := cfg.SalvadorBlock.String()
	for i, group := range salvadorRecoveryGroups {
		for _, addr := range group {
			code, ok := allocCode(cfg.BlockAlloc[key], addr)
			if !ok {
				return fmt.Errorf("blockAlloc %s has no entry for %s", key, addr)
			}
			got, err := salvadorRecovery(code)
			if err != nil {
				return fmt.Errorf("%s: %w", addr, err)
			}
			if got != salvadorRecoveries[i] {
				return fmt.Errorf("%s recovers to %s, want %s", addr, got, salvadorRecoveries[i])
			}
		}
	}
	return nil
}

func TestSalvadorBlockAlloc(t *testing.T) {
	t.Parallel()

	preset := BorMainnetChainConfig.Bor.BlockAlloc[BorMainnetChainConfig.Bor.SalvadorBlock.String()].(map[string]interface{})
	codeAt := func(addr common.Address) string {
		return preset[addr.Hex()[2:]].(map[string]interface{})["code"].(string)
	}
	first := codeAt(salvadorRecoveryGroups[0][0])
	second := codeAt(salvadorRecoveryGroups[1][0])
	other := common.HexToAddress("0xa11ce")

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
	split := codes(first, second)
	split[salvadorRecoveryGroups[0][3]] = second
	missing := codes(first, second)
	delete(missing, salvadorRecoveryGroups[0][2])

	tests := []struct {
		name    string
		cfg     *BorConfig
		wantErr string
	}{
		{"mainnet preset", BorMainnetChainConfig.Bor, ""},
		{"amoy preset", AmoyChainConfig.Bor, ""},
		{"scheduled at fork key", scheduled(allocWith("100", codes(first, second))), ""},
		{"unscheduled", allocWith("0", codes(first, second)), "salvadorBlock is not set"},
		{"scheduled but entries left at 0", scheduled(allocWith("0", codes(first, second))), "blockAlloc 100 has no entry"},
		{"missing address", scheduled(allocWith("100", missing)), "has no entry for " + salvadorRecoveryGroups[0][2].Hex()},
		{"group member to other group's recovery", scheduled(allocWith("100", split)), salvadorRecoveryGroups[0][3].Hex() + " recovers to " + salvadorRecoveries[1].Hex()},
		{"groups swapped", scheduled(allocWith("100", codes(second, first))), "want " + salvadorRecoveries[0].Hex()},
		{"unknown recovery", scheduled(allocWith("100", codes(withRecovery(first, other), second))), "want " + salvadorRecoveries[0].Hex()},
		{"one slot patched", scheduled(allocWith("100", codes(patchImmutable(first, salvadorRecoveryOffsets[1], other), second))), "RECOVERY at 148"},
		{"zero recovery", scheduled(allocWith("100", codes(withRecovery(first, common.Address{}), second))), "RECOVERY is zero"},
		{"other code", scheduled(allocWith("100", codes(flipByte(first, 10), second))), "does not match the reviewed build"},
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
