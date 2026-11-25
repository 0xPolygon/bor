package sequencer

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

// checkSeal cross-checks the sealed header against the open context this
// block was executed under and against the re-execution results, including
// the state root — the catch-all for anything execution missed. At a sprint
// start with a state-sync tx (absent from the stream) re-execution can't
// reproduce the sealed roots and this fails; sealResult treats that as a
// defer-to-canonical, not a bad preconf.
func (env *blockEnv) checkSeal(sealed *types.Header) error {
	if err := env.checkSealContext(sealed); err != nil {
		return err
	}

	receiptsRoot := types.DeriveSha(types.Receipts(env.receipts), trie.NewStackTrie(nil))
	if receiptsRoot != sealed.ReceiptHash {
		return fmt.Errorf("%w: receipts root %s != re-executed %s at block %s",
			errSealMismatch, sealed.ReceiptHash, receiptsRoot, sealed.Number)
	}

	root := env.statedb.IntermediateRoot(env.evm.ChainConfig().IsEIP158(env.header.Number))
	if root != sealed.Root {
		return fmt.Errorf("%w: state root %s != re-executed %s at block %s",
			errSealMismatch, sealed.Root, root, sealed.Number)
	}

	return nil
}

// checkSealContext cross-checks the sealed header against the open context
// this block was executed under and the re-executed gas used.
func (env *blockEnv) checkSealContext(sealed *types.Header) error {
	switch {
	case sealed.Number.Cmp(env.header.Number) != 0,
		sealed.Time != env.header.Time,
		sealed.ParentHash != env.header.ParentHash,
		sealed.GasLimit != env.header.GasLimit,
		!bigEqual(sealed.BaseFee, env.header.BaseFee):
		return fmt.Errorf("%w: open context mismatch at block %s", errSealMismatch, sealed.Number)
	case sealed.GasUsed != env.header.GasUsed:
		return fmt.Errorf("%w: gas used %d != re-executed %d at block %s",
			errSealMismatch, sealed.GasUsed, env.header.GasUsed, sealed.Number)
	}

	return nil
}

func bigEqual(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return a.Cmp(b) == 0
}
