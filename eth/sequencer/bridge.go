package sequencer

import (
	"context"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
)

// Following a store window across a parent that differs only in its seal:
// the takeover case, where the producer that sequenced a run of heights
// is gone and another signer rebuilds them.

// bridgedWindow records a live foreign window this build seeded across an
// equivalent parent, so the seal flush can tell that displacing it drops
// nothing. The zero value records nothing (heights start at 1).
type bridgedWindow struct {
	height      uint64
	storeParent common.Hash
}

// bridgeAtBuildStart runs the equivalence check for the two build-start
// shapes that can follow a window across a seal-only parent difference: a
// sealed generation being recovered, and a live window at our own height.
// Either way the store's tail is a dead producer's and stops moving, so the
// frozen-tail rule is suspended while the build follows it.
func (p *Publisher) bridgeAtBuildStart(ctx context.Context, info tailInfo, state buildStartState,
	number uint64, parent common.Hash,
) tailInfo {
	log.Debug("Sequencer build-start read", "number", number, "state", state,
		"tipOpen", info.tipOpen, "tipOpenHeight", info.tipOpenHeight, "tipOpenParent", info.tipOpenParent,
		"lastSeal", info.lastSealHeight, "window", len(info.window))

	switch {
	case state == buildRecover:
		p.read.breaker.recovered()

		return p.bridgeEquivalentParent(ctx, info, number, parent)
	case state == buildAtBoundary && info.tipOpen && info.tipOpenHeight == number && len(info.window) > 0:
		info = p.bridgeEquivalentParent(ctx, info, number, parent)
		if info.bridgedParent == parent {
			p.read.breaker.recovered()
		}
	}

	return info
}

// recoverWindowLocked rebuilds a dead producer's sealed generation exactly:
// its transactions were acked, so they are promised at this height. The
// store already holds the whole generation, so publish nothing and hand the
// content to the worker to seal and broadcast — without pool fill, which
// would change the state the store's next sealed height executed on.
func (p *Publisher) recoverWindowLocked(info tailInfo, number uint64, parent common.Hash) *miner.AdoptedWindow {
	a, _, ok := parseWindow(info)
	if ok && a.parent != parent && info.bridgedParent == parent {
		a.parent = parent
	}

	if !ok || a.number != number || a.parent != parent {
		return p.muteLocked(number)
	}

	p.mode = buildMode{kind: modeRecover, height: number}

	w := offerLocked(a)
	w.Complete = true

	return w
}

// bridgeEquivalentParent lets a build follow the store's window at its
// height — a sealed generation being recovered, or a dead producer's live
// window — when the window's parent is not ours by hash but is the same
// block in everything execution reads. A bor seal signs the header, so a
// height rebuilt by another producer never reproduces the original hash:
// without this, a takeover follows a dead producer's run for one block, and
// every height after it is rebuilt from the pool in a new order, changing
// the results of transactions that were already preconfirmed.
func (p *Publisher) bridgeEquivalentParent(ctx context.Context, info tailInfo, number uint64, parent common.Hash) tailInfo {
	if info.tipOpenParent == parent || !p.equivalentParent(ctx, info.tipOpenParent, parent, number) {
		return info
	}

	info.bridgedParent = parent

	publishBridgeCount.Inc(1)
	log.Info("Following the store's window across an equivalent parent",
		"number", number, "storeParent", info.tipOpenParent, "parent", parent)

	return info
}

// equivalentParent reports whether the store's sealed block at number-1
// with hash foreign and our block ours agree on transactions, receipts, and
// the fields a child's open context derives from.
//
// The state root is deliberately not compared: EIP-2935 writes each block's
// parent hash into the history contract, so past the first rebuilt height
// the roots differ by that slot alone even when every transaction executed
// identically. The receipt root is the check that matters — it covers every
// result a preconfirmation carried — and a transaction that reads the
// differing hash and diverges shows up in the next height's receipts,
// ending the bridge there.
func (p *Publisher) equivalentParent(ctx context.Context, foreign, ours common.Hash, number uint64) bool {
	if number < 2 || p.chain == nil {
		return false
	}

	mine := p.chain.GetHeaderByHash(ours)
	if mine == nil || mine.Number.Uint64() != number-1 {
		return false
	}

	rctx, cancel := recoverCtx(ctx)
	defer cancel()

	entries, err := p.read.generation(rctx, number-1)
	if err != nil {
		return false
	}

	theirs := generationSealHeader(entries)
	if theirs == nil || theirs.Hash() != foreign {
		return false
	}

	return sameExecution(theirs, mine)
}

func generationSealHeader(entries []*pb.Entry) *types.Header {
	for _, e := range entries {
		seal := e.GetBlockSeal()
		if seal == nil {
			continue
		}

		header, err := decodeSealHeader(seal.GetHeader())
		if err != nil {
			return nil
		}

		return header
	}

	return nil
}

func sameExecution(a, b *types.Header) bool {
	if a.Number == nil || b.Number == nil || a.BaseFee == nil || b.BaseFee == nil {
		return false
	}

	return a.Number.Cmp(b.Number) == 0 &&
		a.TxHash == b.TxHash && a.ReceiptHash == b.ReceiptHash &&
		a.GasUsed == b.GasUsed && a.GasLimit == b.GasLimit &&
		a.Time == b.Time && a.BaseFee.Cmp(b.BaseFee) == 0
}

// carriesBridgedWindowLocked reports whether the foreign window at the flush
// height is one this build seeded across an equivalent parent and our flush
// still carries in full, in order. Displacing it then drops no acked record
// and changes no served result, so it needs no proof from the chain — and
// waiting for that proof would refuse the very block that honors the window.
func (p *Publisher) carriesBridgedWindowLocked(info tailInfo, flushHeight uint64) bool {
	if p.bridged.height != flushHeight || !info.tipOpen || info.tipOpenHeight != flushHeight ||
		info.tipOpenParent != p.bridged.storeParent {
		return false
	}

	theirs, ok := windowTxHashes(info.window)
	if !ok {
		return false
	}

	ours := p.journal.suffixFromHeight(flushHeight)
	if len(ours) == 0 || ours[0].kind != entryOpen {
		return false
	}

	carried := journalRecordHashes(ours[1:])
	if len(carried) < len(theirs) {
		return false
	}

	for i, h := range theirs {
		if carried[i] != h {
			return false
		}
	}

	return true
}
