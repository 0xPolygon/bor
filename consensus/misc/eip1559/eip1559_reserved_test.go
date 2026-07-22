package eip1559

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// reservedFeeConfig builds a London+Cancun bor config with the reserved fork
// gated at reservedBlock (nil = never) and a single reserved client holding
// `capacity` gas. Pre-Dandeli, so the gas target is gasLimit/elasticity (÷2),
// which keeps the arithmetic in the tests exact and readable.
func reservedFeeConfig(reservedBlock *big.Int, capacity uint64) *params.ChainConfig {
	cc := &params.ChainConfig{
		ChainID:             big.NewInt(80003),
		HomesteadBlock:      big.NewInt(0),
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		MuirGlacierBlock:    big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		ShanghaiBlock:       big.NewInt(0),
		CancunBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			Period:                  map[string]uint64{"0": 1},
			Sprint:                  map[string]uint64{"0": 64},
			ReservedBlockspaceBlock: reservedBlock,
		},
	}
	if capacity > 0 {
		cc.Bor.ReservedClients = []params.ReservedClient{
			{Addresses: []common.Address{{0x01}}, QuotaGas: capacity},
		}
	}
	return cc
}

// extraWithReserved encodes a header Extra carrying the reserved-region field.
// The two preceding optional fields (GasTarget, BaseFeeChangeDenominator) must
// be non-nil for RLP to emit the trailing reserved optional.
func extraWithReserved(t *testing.T, reservedGasUsed uint64) []byte {
	t.Helper()
	zero := uint64(0)
	enc, err := rlp.EncodeToBytes(&types.BlockExtraData{
		GasTarget:                &zero,
		BaseFeeChangeDenominator: &zero,
		ReservedGasUsed:          &reservedGasUsed,
	})
	require.NoError(t, err)

	extra := make([]byte, types.ExtraVanityLength)
	extra = append(extra, enc...)
	extra = append(extra, make([]byte, types.ExtraSealLength)...)
	return extra
}

// TestReservedBaseFee_CapacityReducesTarget pins the capacity anchor: the
// public gas target excludes the reserved quotas, so a block whose usage lands
// exactly on the reduced target holds the base fee steady — whereas with the
// fork inactive the same usage sits below the full target and the fee drops.
func TestReservedBaseFee_CapacityReducesTarget(t *testing.T) {
	t.Parallel()

	const gasLimit = 60_000_000
	const capacity = 20_000_000
	baseFee := big.NewInt(params.InitialBaseFee)

	// Public target = (gasLimit - capacity) / 2 = 20M.
	parent := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: gasLimit,
		GasUsed:  (gasLimit - capacity) / 2,
		BaseFee:  baseFee,
	}

	active := reservedFeeConfig(big.NewInt(0), capacity)
	if got := CalcBaseFee(active, parent); got.Cmp(baseFee) != 0 {
		t.Errorf("reserved-active base fee = %s, want unchanged %s (usage == public target)", got, baseFee)
	}

	// Fork inactive: full target = gasLimit/2 = 30M, so 20M usage is below
	// target and the base fee must fall.
	inactive := reservedFeeConfig(nil, capacity)
	if got := CalcBaseFee(inactive, parent); got.Cmp(baseFee) >= 0 {
		t.Errorf("reserved-inactive base fee = %s, want < %s (usage below full target)", got, baseFee)
	}
}

// TestReservedBaseFee_NetsReservedGasUsed pins the used-side netting: the
// parent's ReservedGasUsed is subtracted from gas used before the controller
// runs, so reserved consumption doesn't move the public base fee.
func TestReservedBaseFee_NetsReservedGasUsed(t *testing.T) {
	t.Parallel()

	const gasLimit = 60_000_000
	const capacity = 20_000_000
	baseFee := big.NewInt(params.InitialBaseFee)
	cfg := reservedFeeConfig(big.NewInt(0), capacity)

	// Public target = (60M - 20M)/2 = 20M. Total used 35M, of which 15M is
	// reserved → public used = 20M == target → base fee unchanged.
	parent := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: gasLimit,
		GasUsed:  35_000_000,
		BaseFee:  baseFee,
		Extra:    extraWithReserved(t, 15_000_000),
	}
	if got := CalcBaseFee(cfg, parent); got.Cmp(baseFee) != 0 {
		t.Errorf("base fee = %s, want unchanged %s (public used == target after netting)", got, baseFee)
	}

	// Same total usage but zero reserved gas: public used = 35M > 20M target,
	// so the base fee must rise. Proves the netting is what held it steady above.
	parentNoReserved := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: gasLimit,
		GasUsed:  35_000_000,
		BaseFee:  baseFee,
		Extra:    extraWithReserved(t, 0),
	}
	if got := CalcBaseFee(cfg, parentNoReserved); got.Cmp(baseFee) <= 0 {
		t.Errorf("base fee = %s, want > %s (full usage above target without netting)", got, baseFee)
	}
}

// TestReservedBaseFee_CapacityExceedsLimitFallsBack guards the misconfiguration
// path: if reserved capacity is mis-set at or above the block gas limit, the
// target falls back to the full-limit curve instead of going to zero (which
// would divide by zero) or negative.
func TestReservedBaseFee_CapacityExceedsLimitFallsBack(t *testing.T) {
	t.Parallel()

	const gasLimit = 30_000_000
	baseFee := big.NewInt(params.InitialBaseFee)
	cfg := reservedFeeConfig(big.NewInt(0), gasLimit+1) // capacity > limit

	parent := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: gasLimit,
		GasUsed:  gasLimit / 2, // == full target → unchanged
		BaseFee:  baseFee,
	}

	var got *big.Int
	require.NotPanics(t, func() { got = CalcBaseFee(cfg, parent) },
		"CalcBaseFee must not panic when reserved capacity >= gas limit")
	if got.Cmp(baseFee) != 0 {
		t.Errorf("fallback base fee = %s, want unchanged %s (full target)", got, baseFee)
	}
}

// TestReservedBaseFee_CapacityEqualsLimitFallsBack pins the >= boundary in
// reservedAwareGasTarget: when reserved capacity equals the gas limit the public
// target would be zero, so the guard must fall back to the full target (not
// divide by a zero target). Distinguishes >= from > at the exact boundary.
func TestReservedBaseFee_CapacityEqualsLimitFallsBack(t *testing.T) {
	t.Parallel()

	const gasLimit = 30_000_000
	baseFee := big.NewInt(params.InitialBaseFee)
	cfg := reservedFeeConfig(big.NewInt(0), gasLimit) // capacity == limit

	parent := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: gasLimit,
		GasUsed:  gasLimit / 2, // == full target → unchanged
		BaseFee:  baseFee,
	}

	var got *big.Int
	require.NotPanics(t, func() { got = CalcBaseFee(cfg, parent) },
		"CalcBaseFee must not panic when reserved capacity == gas limit")
	if got.Cmp(baseFee) != 0 {
		t.Errorf("fallback base fee = %s, want unchanged %s (full target)", got, baseFee)
	}
}

// TestReservedBaseFee_VerifyAcceptsProducerValue checks that the reserved-aware
// base fee a producer computes passes strict (pre-Lisovo) header verification,
// since VerifyEIP1559Header recomputes via CalcBaseFee on that path.
func TestReservedBaseFee_VerifyAcceptsProducerValue(t *testing.T) {
	t.Parallel()

	cfg := reservedFeeConfig(big.NewInt(0), 20_000_000) // Lisovo nil → strict verify
	baseFee := big.NewInt(params.InitialBaseFee)
	parent := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: 60_000_000,
		GasUsed:  50_000_000,
		BaseFee:  baseFee,
	}
	expected := CalcBaseFee(cfg, parent)
	child := &types.Header{
		Number:   big.NewInt(2),
		GasLimit: 60_000_000,
		GasUsed:  0,
		BaseFee:  expected,
	}
	require.NoError(t, VerifyEIP1559Header(cfg, parent, child),
		"strict verification must accept the reserved-aware base fee")
}
