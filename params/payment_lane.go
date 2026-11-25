package params

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
)

const (
	PaymentLaneMaxBps    = 10_000
	PaymentLaneMaxAssets = 32

	// PaymentLaneMaxFrameGas and PaymentLaneMaxTransferFrameGas bound the
	// gas one payment frame is credited with, for EIP-3009 calls and for
	// transfer/transferFrom. Real ones use up to ~102k and ~61k on mainnet;
	// the bounds keep memory or returndata tricks inside the asset's own
	// context from buying more discounted gas.
	PaymentLaneMaxFrameGas         = 150_000
	PaymentLaneMaxTransferFrameGas = 100_000

	// paymentLaneMinCapPercentage is the lowest non-payment gas cap: one above
	// the default EIP-1559 target (TargetGasPercentagePostDandeli) when the
	// lane was introduced, since the cap must sit strictly above the target.
	// Fixed here so a later default can never invalidate parameters that are
	// already in force.
	paymentLaneMinCapPercentage = 66

	// paymentLaneFreeEndPercentage is the parent usage by which a free base
	// fee must be gone: the default EIP-1559 target when the lane was
	// introduced, fixed for the same reason as paymentLaneMinCapPercentage.
	paymentLaneFreeEndPercentage = 65
)

var errPaymentLaneConfig = errors.New("invalid payment lane config")

// PaymentLaneConfig holds the consensus parameters of the payment lane.
// Gas accounting is never changed by the lane: only the price charged for
// the gas spent on payment actions of the listed assets.
type PaymentLaneConfig struct {
	// Assets are the ERC-20 contracts whose transfers are priced as payments.
	Assets []common.Address `json:"assets"`

	// MinAmounts optionally sets, per asset, the smallest amount (in token
	// units) that counts as a payment, to keep dust transfers out.
	MinAmounts map[common.Address]*big.Int `json:"minAmounts,omitempty"`

	// BaseFeeDiscountBps is the share of the base fee not charged on payment
	// gas (0 = none, 10000 = all of it).
	BaseFeeDiscountBps uint64 `json:"baseFeeDiscountBps"`

	// TipDiscountBps is the share of the priority fee not charged on payment
	// gas, applied before TipCap.
	TipDiscountBps uint64 `json:"tipDiscountBps"`

	// TipCap is the most a payment pays as priority fee per gas, after
	// TipDiscountBps (nil = uncapped).
	TipCap *big.Int `json:"tipCap,omitempty"`

	// ActiveGasPercentage gates the lane on the parent block: full discount
	// while parent gasUsed <= this share of the parent gas limit (100 = always).
	// Pinned in config rather than read from the per-node runtime target,
	// because the gate is consensus.
	ActiveGasPercentage uint64 `json:"activeGasPercentage"`

	// RampEndGasPercentage, when above ActiveGasPercentage, scales the
	// discount down linearly to zero at this parent usage instead of
	// switching it off at ActiveGasPercentage (0 = hard switch).
	RampEndGasPercentage uint64 `json:"rampEndGasPercentage,omitempty"`

	// CountAllowancePulls also prices transferFrom calls made by a third
	// party (allowance pulls, e.g. exchange settlement) as payments. Off, only
	// movements the payer authorised directly count.
	CountAllowancePulls bool `json:"countAllowancePulls,omitempty"`

	// NonPaymentGasCapPercentage caps the gas non-payment transactions may
	// use in a block, as a share of its gas limit, so payments always have
	// room even when other traffic fills blocks (0 = no cap).
	NonPaymentGasCapPercentage uint64 `json:"nonPaymentGasCapPercentage,omitempty"`
}

func (c *PaymentLaneConfig) Validate() error {
	if c.BaseFeeDiscountBps > PaymentLaneMaxBps || c.TipDiscountBps > PaymentLaneMaxBps {
		return fmt.Errorf("%w: discount above %d bps", errPaymentLaneConfig, PaymentLaneMaxBps)
	}

	if c.TipCap != nil && c.TipCap.Sign() < 0 {
		return fmt.Errorf("%w: negative tip cap", errPaymentLaneConfig)
	}

	if err := c.validateGate(); err != nil {
		return err
	}

	// A free base fee still in force above the EIP-1559 target would let a
	// producer keep its own blocks above it with payments that cost nothing,
	// pushing up the base fee everyone else pays. The tip doesn't count:
	// nothing in consensus sets a minimum, and a producer pays it to itself.
	if c.BaseFeeDiscountBps == PaymentLaneMaxBps && c.discountEndPercentage() > paymentLaneFreeEndPercentage {
		return fmt.Errorf("%w: free base fee past the EIP-1559 target", errPaymentLaneConfig)
	}

	if err := validatePaymentLaneAssets(c.Assets); err != nil {
		return err
	}

	return c.validateMinAmounts()
}

// discountEndPercentage is the parent usage up to which some discount
// applies: the end of the ramp, or the gate itself without one.
func (c *PaymentLaneConfig) discountEndPercentage() uint64 {
	if c.RampEndGasPercentage != 0 {
		return c.RampEndGasPercentage
	}

	return c.ActiveGasPercentage
}

func (c *PaymentLaneConfig) validateGate() error {
	if c.ActiveGasPercentage == 0 || c.ActiveGasPercentage > 100 {
		return fmt.Errorf("%w: active gas percentage %d, want 1..100", errPaymentLaneConfig, c.ActiveGasPercentage)
	}

	if c.RampEndGasPercentage != 0 && (c.RampEndGasPercentage <= c.ActiveGasPercentage || c.RampEndGasPercentage > 100) {
		return fmt.Errorf("%w: ramp end %d, want %d..100", errPaymentLaneConfig, c.RampEndGasPercentage, c.ActiveGasPercentage+1)
	}

	// Below the gate, blocks without payments could never reach the usage
	// that turns the lane off; at or below the default EIP-1559 target they
	// could never go above the target either, and nodes running it would not
	// start.
	lowest := max(c.ActiveGasPercentage, paymentLaneMinCapPercentage)
	if c.NonPaymentGasCapPercentage != 0 && (c.NonPaymentGasCapPercentage < lowest || c.NonPaymentGasCapPercentage >= 100) {
		return fmt.Errorf("%w: non-payment gas cap %d, want 0 or %d..99", errPaymentLaneConfig, c.NonPaymentGasCapPercentage, lowest)
	}

	return nil
}

// validatePaymentLaneAssets is quadratic, which is fine for a list capped at
// PaymentLaneMaxAssets and checked once at startup.
func validatePaymentLaneAssets(assets []common.Address) error {
	if len(assets) == 0 || len(assets) > PaymentLaneMaxAssets {
		return fmt.Errorf("%w: %d assets, want 1..%d", errPaymentLaneConfig, len(assets), PaymentLaneMaxAssets)
	}

	for i, a := range assets {
		if a == (common.Address{}) {
			return fmt.Errorf("%w: zero asset address", errPaymentLaneConfig)
		}

		if slices.Contains(assets[:i], a) {
			return fmt.Errorf("%w: duplicate asset %s", errPaymentLaneConfig, a)
		}
	}

	return nil
}

func (c *PaymentLaneConfig) validateMinAmounts() error {
	for addr, amount := range c.MinAmounts {
		if !c.IsAsset(addr) {
			return fmt.Errorf("%w: min amount for unlisted asset %s", errPaymentLaneConfig, addr)
		}

		if amount == nil || amount.Sign() < 0 || amount.BitLen() > 256 {
			return fmt.Errorf("%w: invalid min amount for %s", errPaymentLaneConfig, addr)
		}
	}

	return nil
}

// MeetsMinAmount reports whether amount (a 32-byte big-endian word from
// calldata) reaches the asset's configured minimum.
func (c *PaymentLaneConfig) MeetsMinAmount(asset common.Address, amount []byte) bool {
	minimum, ok := c.MinAmounts[asset]
	if !ok {
		return true
	}

	return new(big.Int).SetBytes(amount).Cmp(minimum) >= 0
}

// IsAsset is called on every CALL frame while the lane is active, so it stays
// a linear scan over a short, fork-fixed list.
func (c *PaymentLaneConfig) IsAsset(addr common.Address) bool {
	return slices.Contains(c.Assets, addr)
}

// ScaleForParent returns the share of the configured discounts (in bps) that
// applies to a block whose parent used parentGasUsed out of parentGasLimit.
func (c *PaymentLaneConfig) ScaleForParent(parentGasUsed, parentGasLimit uint64) uint64 {
	used := new(big.Int).Mul(new(big.Int).SetUint64(parentGasUsed), big.NewInt(100))
	limit := new(big.Int).SetUint64(parentGasLimit)
	full := new(big.Int).Mul(limit, new(big.Int).SetUint64(c.ActiveGasPercentage))

	if used.Cmp(full) <= 0 {
		return PaymentLaneMaxBps
	}

	// Without a ramp end is 0, so any parent above the gate gets no discount.
	end := new(big.Int).Mul(limit, new(big.Int).SetUint64(c.RampEndGasPercentage))
	if used.Cmp(end) >= 0 {
		return 0
	}

	// scale = (end - used) / (end - full), in bps, floored.
	scale := new(big.Int).Sub(end, used)
	scale.Mul(scale, big.NewInt(PaymentLaneMaxBps))
	scale.Div(scale, new(big.Int).Sub(end, full))

	return scale.Uint64()
}

// NonPaymentGasCap returns the most gas non-payment transactions may use in a
// block with gasLimit, and whether a cap applies at all.
func (c *PaymentLaneConfig) NonPaymentGasCap(gasLimit uint64) (uint64, bool) {
	if c.NonPaymentGasCapPercentage == 0 {
		return 0, false
	}

	// ⌊gasLimit × pct / 100⌋, split so the product cannot overflow.
	pct := c.NonPaymentGasCapPercentage

	return gasLimit/100*pct + gasLimit%100*pct/100, true
}

// IsPaymentLane reports whether the payment lane fork is active at number.
// It is a pure block check, like every other fork: a missing or invalid lane
// config is refused at startup (ValidatePaymentLane) instead of silently
// leaving the fork off while the fork ID and banner advertise it.
func (c *BorConfig) IsPaymentLane(number *big.Int) bool {
	return isBlockForked(c.PaymentLaneBlock, number)
}

// PaymentLaneAt returns the lane parameters in force at number: the entry
// with the highest activation block not above it, nil before the fork.
// Parameters are versioned by block so a later retune never changes how
// historical blocks re-execute.
func (c *BorConfig) PaymentLaneAt(number *big.Int) *PaymentLaneConfig {
	if !c.IsPaymentLane(number) {
		return nil
	}

	var (
		best   *PaymentLaneConfig
		bestAt uint64
	)

	// Called per transaction, so it walks the (tiny) map without
	// allocating; keys are unique, so no two versions tie.
	for key, cfg := range c.PaymentLane {
		at, ok := parseVersionKey(key)
		if ok && cfg != nil && at <= number.Uint64() && (best == nil || at > bestAt) {
			best, bestAt = cfg, at
		}
	}

	return best
}

// parseVersionKey parses a canonical decimal block number (no sign, no
// leading zeros), without allocating.
func parseVersionKey(key string) (uint64, bool) {
	if key == "" || (key[0] == '0' && key != "0") {
		return 0, false
	}

	at, err := strconv.ParseUint(key, 10, 64)

	return at, err == nil
}

// PaymentLaneVersionBlocks returns the activation blocks of every lane
// parameter set, so each retune is part of the fork ID.
func (c *BorConfig) PaymentLaneVersionBlocks() []uint64 {
	return sortedKeys(c.paymentLaneVersions())
}

func sortedKeys(versions map[uint64]*PaymentLaneConfig) []uint64 {
	keys := make([]uint64, 0, len(versions))
	for at := range versions {
		keys = append(keys, at)
	}

	slices.Sort(keys)

	return keys
}

// paymentLaneVersions parses the keys of PaymentLane, skipping malformed
// ones (which ValidatePaymentLane rejects at startup).
func (c *BorConfig) paymentLaneVersions() map[uint64]*PaymentLaneConfig {
	out := make(map[uint64]*PaymentLaneConfig, len(c.PaymentLane))

	for key, cfg := range c.PaymentLane {
		if at, ok := parseVersionKey(key); ok && cfg != nil {
			out[at] = cfg
		}
	}

	return out
}

// ValidatePaymentLane checks the lane wiring: with the fork scheduled, there
// must be a valid parameter set at the fork block, and every later version
// must be valid too; without it, there must be no parameters at all.
func (c *BorConfig) ValidatePaymentLane() error {
	if c.PaymentLaneBlock == nil {
		if len(c.PaymentLane) > 0 {
			return fmt.Errorf("%w: parameters without paymentLaneBlock", errPaymentLaneConfig)
		}

		return nil
	}

	if !c.PaymentLaneBlock.IsUint64() {
		return fmt.Errorf("%w: paymentLaneBlock %v out of range", errPaymentLaneConfig, c.PaymentLaneBlock)
	}

	versions := c.paymentLaneVersions()
	if len(versions) != len(c.PaymentLane) {
		return fmt.Errorf("%w: parameter keys must be canonical block numbers with a config", errPaymentLaneConfig)
	}

	fork := c.PaymentLaneBlock.Uint64()
	if versions[fork] == nil {
		return fmt.Errorf("%w: no parameters at paymentLaneBlock %d", errPaymentLaneConfig, fork)
	}

	return validatePaymentLaneVersions(versions, fork)
}

func validatePaymentLaneVersions(versions map[uint64]*PaymentLaneConfig, fork uint64) error {
	for _, at := range sortedKeys(versions) {
		if at < fork {
			return fmt.Errorf("%w: parameters at %d, before paymentLaneBlock %d", errPaymentLaneConfig, at, fork)
		}

		if err := versions[at].Validate(); err != nil {
			return fmt.Errorf("paymentLane at %d: %w", at, err)
		}
	}

	return nil
}

// checkPaymentLane is the startup check of the lane wiring.
func (c *BorConfig) checkPaymentLane() error {
	if c == nil {
		return nil
	}

	return c.ValidatePaymentLane()
}

// CheckPaymentLaneTarget checks the node's configured EIP-1559 target
// against the non-payment gas cap of the parameters in force at head and of
// every later version: blocks without payments could never go above a target
// at or above the cap, so the base fee could never rise from non-payment
// demand. A node without a target flag uses the default, which every valid
// cap allows.
func (c *BorConfig) CheckPaymentLaneTarget(head *big.Int) error {
	target := uint64(TargetGasPercentagePostDandeli)
	if c.TargetGasPercentage != nil {
		target = *c.TargetGasPercentage
	}

	if c.EnableDynamicTargetGas != nil && *c.EnableDynamicTargetGas && c.TargetGasMaxPercentage != nil {
		target = max(target, *c.TargetGasMaxPercentage)
	}

	versions := c.paymentLaneVersions()
	blocks := sortedKeys(versions)

	// The version in force at head is the last one at or below it; the
	// earlier ones no longer apply.
	first := 0
	for i, at := range blocks {
		if at <= head.Uint64() {
			first = i
		}
	}

	for _, at := range blocks[first:] {
		if cap := versions[at].NonPaymentGasCapPercentage; cap != 0 && target >= cap {
			return fmt.Errorf("%w: miner EIP-1559 target %d%% not below the non-payment gas cap %d%% in force from block %d",
				errPaymentLaneConfig, target, cap, at)
		}
	}

	return nil
}

// checkPaymentLaneCompatible refuses a new config that changes the payment
// lane for blocks already processed: any parameter version in force at or
// before head (added, removed or edited). A valid config always has a
// version at its fork block, so this also covers moving or dropping the fork.
func (c *ChainConfig) checkPaymentLaneCompatible(newcfg *ChainConfig, head *big.Int) *ConfigCompatError {
	old, cur := borOrEmpty(c).paymentLaneVersions(), borOrEmpty(newcfg).paymentLaneVersions()
	for _, at := range sortedKeys(unionVersions(old, cur)) {
		if at > head.Uint64() {
			break
		}

		if !sameLaneVersion(old[at], cur[at]) {
			block := new(big.Int).SetUint64(at)
			return newBlockCompatError("PaymentLane parameters", block, block)
		}
	}

	return nil
}

// sameLaneVersion compares two parameter versions as they are stored: the
// chain config goes through JSON, which drops empty optional fields (and
// encodes a missing version as null).
func sameLaneVersion(a, b *PaymentLaneConfig) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)

	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func borOrEmpty(c *ChainConfig) *BorConfig {
	if c.Bor == nil {
		return &BorConfig{}
	}

	return c.Bor
}

func unionVersions(a, b map[uint64]*PaymentLaneConfig) map[uint64]*PaymentLaneConfig {
	out := map[uint64]*PaymentLaneConfig{}
	maps.Copy(out, a)
	maps.Copy(out, b)

	return out
}
