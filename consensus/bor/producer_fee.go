package bor

import (
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
)

// producerFeeSharePercent is the part of a block's priority fees that goes to
// the block's producer. The rest stays with the coinbase.
const producerFeeSharePercent = 26

// payProducerFeeShare moves the producer's share of the block's priority fees
// from the coinbase, which was credited all of them as the transactions ran,
// to the producer's signer address. It runs before the block's system calls,
// so the coinbase contract only ever sees its own part.
//
// producer is called only when there is something to pay: on import it
// recovers the signer from the header's seal, which a header under
// construction doesn't have.
func (c *Bor) payProducerFeeShare(state vm.StateDB, header *types.Header, body *types.Body, receipts []*types.Receipt, producer func() (common.Address, error)) error {
	if !c.config.IsNativeStaking(header.Number) {
		return nil
	}

	fees, err := blockPriorityFees(header.BaseFee, body.Transactions, receipts)
	if err != nil {
		return fmt.Errorf("summing priority fees: %w", err)
	}

	share := producerFeeShare(fees)
	if share.IsZero() {
		return nil
	}

	signer, err := producer()
	if err != nil {
		return fmt.Errorf("resolving block producer: %w", err)
	}

	coinbase := common.HexToAddress(c.config.CalculateCoinbase(header.Number.Uint64()))

	// The coinbase is a contract and a transaction in this block may already
	// have moved fees out of it. Every node sees the same balance here.
	if balance := state.GetBalance(coinbase); balance.Lt(share) {
		log.Warn("Coinbase holds less than the producer fee share", "number", header.Number, "share", share, "balance", balance)
		share = new(uint256.Int).Set(balance)
	}

	state.SubBalance(coinbase, share, tracing.BalanceChangeTransfer)
	state.AddBalance(signer, share, tracing.BalanceChangeTransfer)

	return nil
}

// localProducer is the producer of a block this node is building: the signer
// it will seal the block with.
func (c *Bor) localProducer() (common.Address, error) {
	return c.authorizedSigner.Load().signer, nil
}

// blockPriorityFees sums gas used times effective tip over the block's user
// transactions, which is what the coinbase was credited for them. receipts are
// the user transactions' receipts, in order; txs may have system transactions
// after them.
func blockPriorityFees(baseFee *big.Int, txs types.Transactions, receipts []*types.Receipt) (*uint256.Int, error) {
	total := new(uint256.Int)

	for i, receipt := range receipts {
		if i >= len(txs) || txs[i].IsBorSystemTx() {
			break
		}

		fee, err := priorityFee(baseFee, txs[i], receipt.GasUsed)
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}

		if _, overflow := total.AddOverflow(total, fee); overflow {
			return nil, types.ErrUint256Overflow
		}
	}

	return total, nil
}

// priorityFee is what one transaction paid in priority fees: its effective
// tip times the gas it used.
func priorityFee(baseFee *big.Int, tx *types.Transaction, gasUsed uint64) (*uint256.Int, error) {
	tip, err := tx.EffectiveGasTip(baseFee)
	if err != nil {
		return nil, err
	}

	fee, overflow := uint256.FromBig(tip)
	if overflow {
		return nil, types.ErrUint256Overflow
	}

	if _, overflow = fee.MulOverflow(fee, uint256.NewInt(gasUsed)); overflow {
		return nil, types.ErrUint256Overflow
	}

	return fee, nil
}

// producerFeeShare is fees * producerFeeSharePercent / 100, rounded down.
func producerFeeShare(fees *uint256.Int) *uint256.Int {
	share := new(big.Int).Mul(fees.ToBig(), big.NewInt(producerFeeSharePercent))
	share.Div(share, big.NewInt(100))

	return uint256.MustFromBig(share)
}
