package sequencer

import (
	"context"
	"encoding/binary"
	"sync"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/params"
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
// with hash foreign and our block ours agree on transactions, receipts, the
// fields a child's open context derives from, and state.
//
// Matching receipts do not prove matching state: a transaction can store a
// BLOCKHASH result, which differs under another seal, and leave a receipt
// identical to the original's, and a later transaction can diverge on that
// stored value without reading a block hash itself. The state roots are
// compared with the EIP-2935 history slots accounted for, the one difference
// a rebuild under another seal cannot avoid; see historyOnlyDiff.
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
	if theirs == nil || theirs.Hash() != foreign || !sameExecution(theirs, mine) {
		return false
	}

	p.lineage.add(theirs)

	if !p.historyOnlyDiff(ctx, theirs, mine) {
		log.Info("Not following the store's window: the parents' states differ beyond the history slots",
			"number", number, "storeParent", foreign, "parent", ours, "storeRoot", theirs.Root, "root", mine.Root)

		return false
	}

	return true
}

// historyOnlyDiff reports whether our parent's state and the store's differ
// only in the history contract's slots for heights the two chains do not
// share. Every block writes its parent's hash there (EIP-2935), so once a
// rebuild under another seal diverges in hash, each slot from that height up
// holds our hash on one side and the store's on the other. Writing the
// store's hashes into those slots of our state must then give the store's
// root exactly; any other difference keeps the roots apart.
//
// A slot is rewritten only while ours holds a hash the store's chain does
// not have there: an empty slot was never written (no history before
// Prague), and a matching one is a height both chains share.
func (p *Publisher) historyOnlyDiff(ctx context.Context, theirs, mine *types.Header) bool {
	if theirs.Root == mine.Root {
		return true
	}

	// Not StateAt: a pipelined import still committing is served as an
	// overlay whose root leaves the block's own writes out.
	statedb, err := p.chain.SealingStateAt(mine.Root)
	if err != nil {
		return false
	}

	top := theirs.Number.Uint64() - 1
	hash := theirs.ParentHash

	for n := top; n > 0 && top-n < uint64(lineageMax); n-- {
		slot := historySlot(n)

		recorded := statedb.GetState(params.HistoryStorageAddress, slot)
		if recorded == (common.Hash{}) || recorded == hash {
			break
		}

		statedb.SetState(params.HistoryStorageAddress, slot, hash)

		parent, ok := p.storeParent(ctx, n, hash)
		if !ok {
			break
		}

		hash = parent
	}

	return statedb.IntermediateRoot(true) == theirs.Root
}

// storeParent returns the parent hash of the store chain's block at number,
// whose hash is hash. The links the bridge has read answer it; one it has
// not, as after a restart mid-run, is read from the store's sealed
// generation on the build start's recovery budget.
func (p *Publisher) storeParent(ctx context.Context, number uint64, hash common.Hash) (common.Hash, bool) {
	if parent, ok := p.lineage.parentOf(number, hash); ok {
		return parent, true
	}

	rctx, cancel := recoverCtx(ctx)
	defer cancel()

	entries, err := p.read.generation(rctx, number)
	if err != nil {
		return common.Hash{}, false
	}

	header := generationSealHeader(entries)
	if header == nil || header.Hash() != hash {
		return common.Hash{}, false
	}

	p.lineage.backfill(header)

	return header.ParentHash, true
}

// historySlot is the history contract's storage slot for the hash of block
// number: block number+1 stores its parent hash at
// (NUMBER-1) % HistoryServeWindow.
func historySlot(number uint64) common.Hash {
	var slot common.Hash

	binary.BigEndian.PutUint64(slot[common.HashLength-8:], number%params.HistoryServeWindow)

	return slot
}

// lineageMax bounds how far back the history check follows a bridged run,
// and so how many store links it keeps: past it a run stops bridging, and
// its later heights are rebuilt from the pool. It stays under the EIP-2935
// window, so no two heights it covers share a slot. Var for tests.
var lineageMax = 1024

// storeLineage keeps the hash and parent hash of each sealed store header
// the bridge has read, so the history check can recover the store chain's
// hashes below a parent without reading every height again.
type storeLineage struct {
	mu    sync.Mutex
	links map[uint64]storeLink
}

type storeLink struct{ hash, parent common.Hash }

// add records a sealed header, keeping only the links of the lineageMax
// heights up to it.
func (l *storeLineage) add(header *types.Header) {
	number := header.Number.Uint64()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.links == nil {
		l.links = make(map[uint64]storeLink)
	}

	l.links[number] = storeLink{hash: header.Hash(), parent: header.ParentHash}

	for n := range l.links {
		if n > number || n+uint64(lineageMax) <= number {
			delete(l.links, n)
		}
	}
}

// parentOf returns the parent hash of the link at number, if the link read
// there has hash hash.
func (l *storeLineage) parentOf(number uint64, hash common.Hash) (common.Hash, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	link, ok := l.links[number]
	if !ok || link.hash != hash {
		return common.Hash{}, false
	}

	return link.parent, true
}

// backfill records a sealed header below the newest link. Unlike add, it
// keeps the links above it: it fills in heights read before a restart, not a
// newer read of the store's tail.
func (l *storeLineage) backfill(header *types.Header) {
	number := header.Number.Uint64()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.links == nil {
		l.links = make(map[uint64]storeLink)
	}

	newest := number
	for n := range l.links {
		newest = max(newest, n)
	}

	if number+uint64(lineageMax) <= newest {
		return
	}

	l.links[number] = storeLink{hash: header.Hash(), parent: header.ParentHash}
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
