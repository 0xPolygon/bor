package core

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// paymentLaneNoParentCounter counts block contexts built from the payment
// lane fork on without a resolvable parent, which are priced at full price.
// Block import always has the parent; a non-zero rate means some execution
// path (RPC, tracing, speculative execution) diverges from consensus prices.
var paymentLaneNoParentCounter = metrics.NewRegisteredCounter("chain/paymentlane/noparent", nil)

// paymentLaneScale reads the parent header, so every node derives the same
// answer from chain data alone.
func paymentLaneScale(header *types.Header, chain ChainContext) uint64 {
	if chain == nil || header.Number.Sign() == 0 {
		return 0
	}

	cfg := chain.Config()
	if cfg == nil || cfg.Bor == nil || !cfg.Bor.IsPaymentLane(header.Number) {
		return 0
	}

	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		paymentLaneNoParentCounter.Inc(1)
		log.Debug("Payment lane parent unavailable, pricing at full price", "number", header.Number, "parent", header.ParentHash)

		return 0
	}

	return PaymentLaneScaleFor(cfg, header.Number, parent)
}

// PaymentLaneScaleFor is the discount scale of the block at number given its
// parent header. It is the same computation the node derives from the chain,
// so callers that have the parent but no chain (the evm runtime, t8n) price as
// an importing node would. It returns 0 when the lane is not active at number
// or the parent is unknown, which is also the "price at full price" answer.
func PaymentLaneScaleFor(cfg *params.ChainConfig, number *big.Int, parent *types.Header) uint64 {
	if cfg == nil || cfg.Bor == nil || number == nil || parent == nil {
		return 0
	}

	lane := cfg.Bor.PaymentLaneAt(number)
	if lane == nil {
		return 0
	}

	return lane.ScaleForParent(parent.GasUsed, parent.GasLimit)
}

// checkChainConfig is the startup check of a chain config at head: fork
// order and wiring, and this node's EIP-1559 target against the payment
// lane cap that applies from head on.
func checkChainConfig(cfg *params.ChainConfig, head *big.Int) error {
	if err := cfg.CheckConfigForkOrder(); err != nil {
		return err
	}

	if cfg.Bor == nil {
		return nil
	}

	return cfg.Bor.CheckPaymentLaneTarget(head)
}
