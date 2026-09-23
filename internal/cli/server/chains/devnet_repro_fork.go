//go:build devnet_repro

package chains

import (
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// Devnet-only isolated fork: when BOR_FORK_START and BOR_FORK_VALIDATOR are
// set, the mainnet validator set is overridden from BOR_FORK_START onward with
// a single throwaway signer, so an isolated (0-peer) clone can produce its own
// chain on top of real mainnet state. Never compiled into production builds.
func init() {
	startStr, validator := os.Getenv("BOR_FORK_START"), os.Getenv("BOR_FORK_VALIDATOR")
	if startStr == "" || validator == "" {
		return
	}
	start, err := strconv.ParseUint(startStr, 10, 64)
	if err != nil || !common.IsHexAddress(validator) {
		log.Error("devnet_repro: invalid fork override", "start", startStr, "validator", validator)
		return
	}
	cfg := mainnetBor.Genesis.Config.Bor
	cfg.OverrideValidatorSetInRange = append(cfg.OverrideValidatorSetInRange, params.BlockRangeOverrideValidatorSet{
		StartBlock: start,
		EndBlock:   start + 10_000_000,
		Validators: []common.Address{common.HexToAddress(validator)},
	})
	log.Warn("devnet_repro: isolated fork validator override active", "start", start, "validator", validator)
}
