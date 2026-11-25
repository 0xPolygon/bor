package params

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// validLaneConfig is the preset with a hard gate at 70%, which the gate and
// cap bounds below are written against.
func validLaneConfig() *PaymentLaneConfig {
	c := DefaultPaymentLaneConfig(MainnetPaymentLaneAssets)
	c.ActiveGasPercentage, c.RampEndGasPercentage = 70, 0

	return c
}

func TestPaymentLaneConfigValidate(t *testing.T) {
	t.Parallel()

	tooMany := make([]common.Address, PaymentLaneMaxAssets+1)
	for i := range tooMany {
		tooMany[i] = common.BigToAddress(big.NewInt(int64(i + 1)))
	}

	tests := []struct {
		name    string
		mutate  func(c *PaymentLaneConfig)
		wantErr string
	}{
		{"default", func(*PaymentLaneConfig) {}, ""},
		{"nil tip cap is uncapped", func(c *PaymentLaneConfig) { c.TipCap = nil }, ""},
		{"zero tip cap", func(c *PaymentLaneConfig) { c.TipCap = new(big.Int) }, ""},
		{"full base discount", func(c *PaymentLaneConfig) { c.BaseFeeDiscountBps = PaymentLaneMaxBps }, ""},
		{"full tip discount", func(c *PaymentLaneConfig) { c.TipDiscountBps = PaymentLaneMaxBps }, ""},
		{"no discount at all", func(c *PaymentLaneConfig) { c.BaseFeeDiscountBps, c.TipCap = 0, nil }, ""},
		{"max assets", func(c *PaymentLaneConfig) { c.Assets, c.MinAmounts = tooMany[:PaymentLaneMaxAssets], nil }, ""},
		{"always on", func(c *PaymentLaneConfig) { c.ActiveGasPercentage = 100 }, ""},
		{"free and always on", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipDiscountBps, c.ActiveGasPercentage = PaymentLaneMaxBps, PaymentLaneMaxBps, 100
		}, "payment gas free at any parent usage"},
		{"free by zero tip cap and always on", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipCap, c.ActiveGasPercentage = PaymentLaneMaxBps, new(big.Int), 100
		}, "payment gas free at any parent usage"},
		{"free behind a gate", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipDiscountBps, c.ActiveGasPercentage = PaymentLaneMaxBps, PaymentLaneMaxBps, 99
		}, ""},
		// The tip is no cost to a producer filling its own block, and nothing
		// in consensus sets a minimum one.
		{"always on, base free, tip charged", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipCap, c.ActiveGasPercentage = PaymentLaneMaxBps, big.NewInt(1), 100
		}, "payment gas free at any parent usage"},
		{"always on, base nearly free", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.ActiveGasPercentage = PaymentLaneMaxBps-1, 100
		}, ""},
		{"always on, tip free, base charged", func(c *PaymentLaneConfig) {
			c.BaseFeeDiscountBps, c.TipDiscountBps, c.ActiveGasPercentage = PaymentLaneMaxBps-1, PaymentLaneMaxBps, 100
		}, ""},
		{"ramp", func(c *PaymentLaneConfig) { c.RampEndGasPercentage = 85 }, ""},
		{"ramp to 100", func(c *PaymentLaneConfig) { c.RampEndGasPercentage = 100 }, ""},
		{"allowance pulls", func(c *PaymentLaneConfig) { c.CountAllowancePulls = true }, ""},
		{"no assets", func(c *PaymentLaneConfig) { c.Assets = nil }, "0 assets"},
		{"too many assets", func(c *PaymentLaneConfig) { c.Assets = tooMany }, "33 assets"},
		{"base discount above max", func(c *PaymentLaneConfig) { c.BaseFeeDiscountBps = PaymentLaneMaxBps + 1 }, "discount above"},
		{"tip discount above max", func(c *PaymentLaneConfig) { c.TipDiscountBps = PaymentLaneMaxBps + 1 }, "discount above"},
		{"negative tip cap", func(c *PaymentLaneConfig) { c.TipCap = big.NewInt(-1) }, "negative tip cap"},
		{"zero address", func(c *PaymentLaneConfig) { c.Assets = []common.Address{MainnetUSDC, {}} }, "zero asset"},
		{"duplicate", func(c *PaymentLaneConfig) { c.Assets = []common.Address{MainnetUSDC, MainnetDAI, MainnetUSDC} }, "duplicate"},
		{"gate zero", func(c *PaymentLaneConfig) { c.ActiveGasPercentage = 0 }, "active gas percentage 0"},
		{"gate above 100", func(c *PaymentLaneConfig) { c.ActiveGasPercentage = 101 }, "active gas percentage 101"},
		{"ramp equal to gate", func(c *PaymentLaneConfig) { c.RampEndGasPercentage = 70 }, "ramp end 70, want 71..100"},
		{"ramp below gate", func(c *PaymentLaneConfig) { c.RampEndGasPercentage = 60 }, "ramp end 60, want 71..100"},
		{"ramp above 100", func(c *PaymentLaneConfig) { c.RampEndGasPercentage = 101 }, "ramp end 101"},
		{"non-payment cap", func(c *PaymentLaneConfig) { c.NonPaymentGasCapPercentage = 90 }, ""},
		{"non-payment cap at gate", func(c *PaymentLaneConfig) { c.NonPaymentGasCapPercentage = 70 }, ""},
		{"non-payment cap below gate", func(c *PaymentLaneConfig) { c.NonPaymentGasCapPercentage = 69 }, "non-payment gas cap 69, want 0 or 70..99"},
		{"non-payment cap below the default target", func(c *PaymentLaneConfig) {
			c.ActiveGasPercentage, c.NonPaymentGasCapPercentage = 50, 60
		}, "non-payment gas cap 60, want 0 or 66..99"},
		{"non-payment cap at the default target", func(c *PaymentLaneConfig) {
			c.ActiveGasPercentage, c.NonPaymentGasCapPercentage = 50, 65
		}, "non-payment gas cap 65, want 0 or 66..99"},
		{"non-payment cap just above the default target", func(c *PaymentLaneConfig) {
			c.ActiveGasPercentage, c.NonPaymentGasCapPercentage = 50, 66
		}, ""},
		{"min amount", func(c *PaymentLaneConfig) {
			c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: big.NewInt(10_000)}
		}, ""},
		{"zero min amount", func(c *PaymentLaneConfig) { c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: new(big.Int)} }, ""},
		{"min amount unlisted", func(c *PaymentLaneConfig) { c.MinAmounts = map[common.Address]*big.Int{MainnetPUSD: big.NewInt(1)} }, "unlisted asset"},
		{"min amount nil", func(c *PaymentLaneConfig) { c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: nil} }, "invalid min amount"},
		{"min amount negative", func(c *PaymentLaneConfig) { c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: big.NewInt(-1)} }, "invalid min amount"},
		{"max uint256 min amount", func(c *PaymentLaneConfig) {
			c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: new(big.Int).Sub(new(big.Int).Lsh(common.Big1, 256), common.Big1)}
		}, ""},
		{"min amount too wide", func(c *PaymentLaneConfig) {
			c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: new(big.Int).Lsh(common.Big1, 256)}
		}, "invalid min amount"},
		{"non-payment cap 99", func(c *PaymentLaneConfig) { c.NonPaymentGasCapPercentage = 99 }, ""},
		{"non-payment cap 100", func(c *PaymentLaneConfig) { c.NonPaymentGasCapPercentage = 100 }, "non-payment gas cap 100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validLaneConfig()
			tt.mutate(c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), tt.wantErr), err.Error())
		})
	}
}

func TestPaymentLaneConfigIsAsset(t *testing.T) {
	t.Parallel()

	c := validLaneConfig()
	for _, a := range MainnetPaymentLaneAssets {
		require.True(t, c.IsAsset(a))
	}

	require.False(t, c.IsAsset(MainnetPUSD))
	require.False(t, c.IsAsset(common.HexToAddress("0x01")))
}

func TestDefaultPaymentLaneConfigCopiesAssets(t *testing.T) {
	t.Parallel()

	assets := []common.Address{MainnetUSDC}
	c := DefaultPaymentLaneConfig(assets)
	assets[0] = MainnetDAI
	require.Equal(t, []common.Address{MainnetUSDC}, c.Assets)
	require.NoError(t, c.Validate())
}

func TestIsPaymentLaneIsABlockCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fork   *big.Int
		number int64
		want   bool
	}{
		{"unset fork never active", nil, 1_000_000, false},
		{"genesis activation at 0", big.NewInt(0), 0, true},
		{"scheduled fork before activation", big.NewInt(100), 99, false},
		{"scheduled fork at activation", big.NewInt(100), 100, true},
		{"scheduled fork after activation", big.NewInt(100), 101, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The parameters are deliberately absent: activation must not
			// depend on them (they are validated at startup instead).
			c := &BorConfig{PaymentLaneBlock: tt.fork}
			require.Equal(t, tt.want, c.IsPaymentLane(big.NewInt(tt.number)))

			cfg := &ChainConfig{ChainID: big.NewInt(1), Bor: c}
			require.Equal(t, tt.want, cfg.Rules(big.NewInt(tt.number), false, 0).IsPaymentLane)
		})
	}
}

func TestValidatePaymentLane(t *testing.T) {
	t.Parallel()

	invalid := validLaneConfig()
	invalid.Assets = nil

	tests := []struct {
		name    string
		fork    *big.Int
		lane    map[string]*PaymentLaneConfig
		wantErr string
	}{
		{"not scheduled", nil, nil, ""},
		{"scheduled", big.NewInt(100), map[string]*PaymentLaneConfig{"100": validLaneConfig()}, ""},
		{"scheduled with a retune", big.NewInt(100), map[string]*PaymentLaneConfig{"100": validLaneConfig(), "200": validLaneConfig()}, ""},
		{"parameters without fork", nil, map[string]*PaymentLaneConfig{"100": validLaneConfig()}, "without paymentLaneBlock"},
		{"fork without parameters", big.NewInt(100), nil, "no parameters at paymentLaneBlock 100"},
		{"parameters only after fork", big.NewInt(100), map[string]*PaymentLaneConfig{"101": validLaneConfig()}, "no parameters at paymentLaneBlock 100"},
		{"parameters before fork", big.NewInt(100), map[string]*PaymentLaneConfig{"99": validLaneConfig(), "100": validLaneConfig()}, "before paymentLaneBlock"},
		{"invalid parameters", big.NewInt(100), map[string]*PaymentLaneConfig{"100": invalid}, "paymentLane at 100"},
		{"invalid retune", big.NewInt(100), map[string]*PaymentLaneConfig{"100": validLaneConfig(), "200": invalid}, "paymentLane at 200"},
		{"nil parameters", big.NewInt(100), map[string]*PaymentLaneConfig{"100": nil}, "canonical block numbers"},
		{"non-numeric key", big.NewInt(100), map[string]*PaymentLaneConfig{"100": validLaneConfig(), "x": validLaneConfig()}, "canonical block numbers"},
		{"non-canonical key", big.NewInt(100), map[string]*PaymentLaneConfig{"0100": validLaneConfig()}, "canonical block numbers"},
		{"fork out of range", new(big.Int).Lsh(common.Big1, 64), map[string]*PaymentLaneConfig{"100": validLaneConfig()}, "out of range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &BorConfig{PaymentLaneBlock: tt.fork, PaymentLane: tt.lane}
			err := c.ValidatePaymentLane()

			chainErr := (&ChainConfig{ChainID: big.NewInt(1), Bor: c}).CheckConfigForkOrder()
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NoError(t, chainErr)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, chainErr, tt.wantErr, "startup must refuse the config")
		})
	}
}

func TestPaymentLaneAt(t *testing.T) {
	t.Parallel()

	first, retune := validLaneConfig(), validLaneConfig()
	retune.BaseFeeDiscountBps = 5_000
	c := &BorConfig{PaymentLaneBlock: big.NewInt(100), PaymentLane: map[string]*PaymentLaneConfig{"100": first, "200": retune}}

	require.Nil(t, c.PaymentLaneAt(big.NewInt(99)))
	require.Same(t, first, c.PaymentLaneAt(big.NewInt(100)))
	require.Same(t, first, c.PaymentLaneAt(big.NewInt(199)))
	require.Same(t, retune, c.PaymentLaneAt(big.NewInt(200)))
	require.Same(t, retune, c.PaymentLaneAt(big.NewInt(1_000_000)))
	require.Equal(t, []uint64{100, 200}, c.PaymentLaneVersionBlocks())

	require.Nil(t, (&BorConfig{PaymentLane: map[string]*PaymentLaneConfig{"0": first}}).PaymentLaneAt(big.NewInt(1)), "parameters without a fork block")

	genesis := &BorConfig{PaymentLaneBlock: big.NewInt(0), PaymentLane: map[string]*PaymentLaneConfig{"0": first}}
	require.Same(t, first, genesis.PaymentLaneAt(big.NewInt(0)))
	require.Same(t, first, genesis.PaymentLaneAt(big.NewInt(5)))
}

func TestPaymentLaneScaleForParent(t *testing.T) {
	t.Parallel()

	hard := &PaymentLaneConfig{ActiveGasPercentage: 70}
	ramp := &PaymentLaneConfig{ActiveGasPercentage: 70, RampEndGasPercentage: 90}
	always := &PaymentLaneConfig{ActiveGasPercentage: 100}

	tests := []struct {
		name  string
		cfg   *PaymentLaneConfig
		used  uint64
		limit uint64
		want  uint64
	}{
		{"empty parent", hard, 0, 160_000_000, 10_000},
		{"just below threshold", hard, 111_999_999, 160_000_000, 10_000},
		{"at threshold", hard, 112_000_000, 160_000_000, 10_000},
		{"just above threshold", hard, 112_000_001, 160_000_000, 0},
		{"full parent", hard, 160_000_000, 160_000_000, 0},
		{"threshold floors", hard, 69, 99, 10_000},
		{"threshold floors above", hard, 70, 99, 0},
		{"huge limit", hard, ^uint64(0) / 2, ^uint64(0), 10_000},
		{"ramp at start", ramp, 112_000_000, 160_000_000, 10_000},
		{"ramp quarter", ramp, 120_000_000, 160_000_000, 7_500},
		{"ramp half", ramp, 128_000_000, 160_000_000, 5_000},
		{"ramp floors", ramp, 128_000_001, 160_000_000, 4_999},
		{"ramp just before end", ramp, 143_999_999, 160_000_000, 0},
		{"ramp at end", ramp, 144_000_000, 160_000_000, 0},
		{"ramp past end", ramp, 150_000_000, 160_000_000, 0},
		{"always on at full parent", always, 160_000_000, 160_000_000, 10_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.cfg.ScaleForParent(tt.used, tt.limit))
		})
	}
}

func TestDescriptionPaymentLaneBanner(t *testing.T) {
	t.Parallel()

	cfg := &ChainConfig{ChainID: big.NewInt(1), Bor: &BorConfig{PaymentLaneBlock: big.NewInt(123)}}
	require.Contains(t, cfg.Description(), " - PaymentLane:               #123")

	// Same column as the other Bor forks.
	cfg.Bor.HampiBlock = big.NewInt(1)
	lines := strings.Split(cfg.Description(), "\n")
	var hampi, lane string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, " - Hampi:"):
			hampi = l
		case strings.HasPrefix(l, " - PaymentLane:"):
			lane = l
		}
	}
	require.Equal(t, strings.Index(hampi, "#"), strings.Index(lane, "#"))

	cfg.Bor.PaymentLaneBlock = nil
	require.NotContains(t, cfg.Description(), "PaymentLane")
}

func TestPaymentLaneNonPaymentGasCap(t *testing.T) {
	t.Parallel()

	c := validLaneConfig()
	_, ok := c.NonPaymentGasCap(160_000_000)
	require.False(t, ok)

	c.NonPaymentGasCapPercentage = 90
	limit, ok := c.NonPaymentGasCap(160_000_000)
	require.True(t, ok)
	require.Equal(t, uint64(144_000_000), limit)

	limit, _ = c.NonPaymentGasCap(99)
	require.Equal(t, uint64(89), limit)

	limit, _ = c.NonPaymentGasCap(^uint64(0))
	require.Equal(t, ^uint64(0)/100*90+(^uint64(0)%100)*90/100, limit)
}

func TestPaymentLaneMeetsMinAmount(t *testing.T) {
	t.Parallel()

	c := validLaneConfig()
	c.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: big.NewInt(10_000)}

	word := func(v int64) []byte { return common.LeftPadBytes(big.NewInt(v).Bytes(), 32) }
	require.False(t, c.MeetsMinAmount(MainnetUSDC, word(9_999)))
	require.True(t, c.MeetsMinAmount(MainnetUSDC, word(10_000)))
	require.True(t, c.MeetsMinAmount(MainnetUSDC, word(10_001)))
	require.True(t, c.MeetsMinAmount(MainnetDAI, word(1)), "no minimum configured")
}

func TestCheckPaymentLaneTarget(t *testing.T) {
	t.Parallel()

	u := func(v uint64) *uint64 { return &v }
	on := true
	capped := func(pct uint64) map[string]*PaymentLaneConfig {
		c := validLaneConfig()
		c.NonPaymentGasCapPercentage = pct
		if pct != 0 {
			c.ActiveGasPercentage = min(c.ActiveGasPercentage, pct)
		}

		return map[string]*PaymentLaneConfig{"0": validLaneConfig(), "100": c}
	}

	tests := []struct {
		name    string
		bor     BorConfig
		wantErr string
	}{
		{"no lane", BorConfig{}, ""},
		{"no cap", BorConfig{PaymentLane: capped(0)}, ""},
		{"default target under cap", BorConfig{PaymentLane: capped(70)}, ""},
		{"default target equals cap", BorConfig{PaymentLane: capped(65)}, "target 65% not below the non-payment gas cap 65%"},
		{"default target just below cap", BorConfig{PaymentLane: capped(66)}, ""},
		{"static target above cap", BorConfig{PaymentLane: capped(70), TargetGasPercentage: u(75)}, "target 75% not below the non-payment gas cap 70% in force from block 100"},
		{"static target at cap", BorConfig{PaymentLane: capped(70), TargetGasPercentage: u(70)}, "target 70% not below the non-payment gas cap 70%"},
		{"static target just below cap", BorConfig{PaymentLane: capped(70), TargetGasPercentage: u(69)}, ""},
		{"dynamic max above cap", BorConfig{PaymentLane: capped(70), EnableDynamicTargetGas: &on, TargetGasMaxPercentage: u(80)}, "target 80%"},
		{"dynamic max at cap", BorConfig{PaymentLane: capped(70), EnableDynamicTargetGas: &on, TargetGasMinPercentage: u(15), TargetGasMaxPercentage: u(70)}, "target 70% not below"},
		{"dynamic max just below cap", BorConfig{PaymentLane: capped(71), EnableDynamicTargetGas: &on, TargetGasMinPercentage: u(15), TargetGasMaxPercentage: u(70)}, ""},
		{"dynamic max ignored when disabled", BorConfig{PaymentLane: capped(70), TargetGasMaxPercentage: u(80)}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.bor.CheckPaymentLaneTarget(common.Big0)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckConfigForkOrderWithoutBor(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&ChainConfig{ChainID: big.NewInt(1)}).CheckConfigForkOrder())
}

func TestCheckCompatiblePaymentLane(t *testing.T) {
	t.Parallel()

	lane := func(bps uint64) *PaymentLaneConfig {
		c := validLaneConfig()
		c.BaseFeeDiscountBps = bps

		return c
	}

	chain := func(fork *big.Int, versions map[string]*PaymentLaneConfig) *ChainConfig {
		return &ChainConfig{ChainID: big.NewInt(1), Bor: &BorConfig{PaymentLaneBlock: fork, PaymentLane: versions}}
	}

	stored := chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(5_000)})

	tests := []struct {
		name   string
		next   *ChainConfig
		head   uint64
		rewind uint64 // 0 = compatible
	}{
		{"unchanged", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(5_000)}), 300, 0},
		{"future version added", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(5_000), "400": lane(0)}), 300, 0},
		{"future version edited", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(1)}), 150, 0},
		{"past version edited", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(1)}), 300, 199},
		{"version in force at head edited", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "200": lane(1)}), 200, 199},
		{"past version removed", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500)}), 300, 199},
		{"past version inserted", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(7_500), "150": lane(1), "200": lane(5_000)}), 300, 149},
		{"first version edited", chain(big.NewInt(100), map[string]*PaymentLaneConfig{"100": lane(1), "200": lane(5_000)}), 300, 99},
		{"fork block moved", chain(big.NewInt(120), map[string]*PaymentLaneConfig{"120": lane(7_500), "200": lane(5_000)}), 300, 99},
		{"fork removed", &ChainConfig{ChainID: big.NewInt(1)}, 300, 99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := stored.CheckCompatible(tt.next, tt.head, 0)
			if tt.rewind == 0 {
				require.Nil(t, err)
				return
			}

			require.NotNil(t, err)
			require.Equal(t, tt.rewind, err.RewindToBlock)
		})
	}

	// A chain that never had the lane can schedule it in the future.
	require.Nil(t, (&ChainConfig{ChainID: big.NewInt(1)}).CheckCompatible(stored, 50, 0))
}

func TestCheckPaymentLaneTargetFollowsHead(t *testing.T) {
	t.Parallel()

	target := uint64(80)
	low, high := validLaneConfig(), validLaneConfig()
	low.NonPaymentGasCapPercentage, high.NonPaymentGasCapPercentage = 70, 90

	rising := &BorConfig{TargetGasPercentage: &target, PaymentLane: map[string]*PaymentLaneConfig{"0": low, "100": high}}
	require.ErrorContains(t, rising.CheckPaymentLaneTarget(big.NewInt(50)), "target 80% not below the non-payment gas cap 70% in force from block 0", "the version in force")
	require.NoError(t, rising.CheckPaymentLaneTarget(big.NewInt(100)), "superseded at head")
	require.NoError(t, rising.CheckPaymentLaneTarget(big.NewInt(150)))

	falling := &BorConfig{TargetGasPercentage: &target, PaymentLane: map[string]*PaymentLaneConfig{"0": high, "100": low}}
	require.ErrorContains(t, falling.CheckPaymentLaneTarget(big.NewInt(50)), "in force from block 100", "a scheduled version")
	require.ErrorContains(t, falling.CheckPaymentLaneTarget(big.NewInt(150)), "in force from block 100")

	require.NoError(t, (&BorConfig{PaymentLane: map[string]*PaymentLaneConfig{"0": low}}).CheckPaymentLaneTarget(big.NewInt(1)), "default target")
}

func TestDefaultPaymentLaneConfigMinAmounts(t *testing.T) {
	t.Parallel()

	c := DefaultPaymentLaneConfig(MainnetPaymentLaneAssets)
	require.Equal(t, map[common.Address]*big.Int{
		MainnetUSDC:  big.NewInt(10_000),
		MainnetUSDCe: big.NewInt(10_000),
		MainnetUSDT0: big.NewInt(10_000),
		MainnetDAI:   new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil),
	}, c.MinAmounts)
	require.NoError(t, c.Validate())

	c.MinAmounts[MainnetUSDC].SetInt64(1)
	require.Equal(t, big.NewInt(10_000), DefaultPaymentLaneConfig(MainnetPaymentLaneAssets).MinAmounts[MainnetUSDC], "presets are not shared")

	require.Nil(t, DefaultPaymentLaneConfig([]common.Address{common.HexToAddress("0x01")}).MinAmounts, "unknown assets get no minimum")
	require.Equal(t, big.NewInt(10_000), DefaultPaymentLaneConfig([]common.Address{AmoyUSDC}).MinAmounts[AmoyUSDC])
}

func TestPaymentLaneAtIgnoresMalformedKeys(t *testing.T) {
	t.Parallel()

	good := validLaneConfig()
	c := &BorConfig{PaymentLaneBlock: big.NewInt(0), PaymentLane: map[string]*PaymentLaneConfig{"0": good, "010": validLaneConfig(), "x": validLaneConfig(), "5": nil}}
	require.Same(t, good, c.PaymentLaneAt(big.NewInt(20)))
}

// The stored chain config went through JSON, which drops an empty
// minAmounts map: that must not read as a changed version on restart.
func TestCheckCompatiblePaymentLaneAfterJSONRoundTrip(t *testing.T) {
	t.Parallel()

	lane := validLaneConfig()
	lane.MinAmounts = map[common.Address]*big.Int{}
	cfg := &ChainConfig{ChainID: big.NewInt(1), Bor: &BorConfig{PaymentLaneBlock: big.NewInt(10), PaymentLane: map[string]*PaymentLaneConfig{"10": lane}}}

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	var stored ChainConfig
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Nil(t, stored.Bor.PaymentLane["10"].MinAmounts)
	require.Nil(t, stored.CheckCompatible(cfg, 100, 0))

	edited := validLaneConfig()
	edited.MinAmounts = map[common.Address]*big.Int{MainnetUSDC: big.NewInt(1)}
	cfg.Bor.PaymentLane["10"] = edited
	require.NotNil(t, stored.CheckCompatible(cfg, 100, 0))
}
