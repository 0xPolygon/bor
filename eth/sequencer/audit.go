package sequencer

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// unobservedMismatchReason marks a height whose stored generation never became
// canonical and which this node never served a preconfirmation from — the
// audit found it after the fact. The other reasons all mean a preconfirmation
// was published to callers and then invalidated, which is a stronger claim.
const unobservedMismatchReason = "unobserved_mismatch"

// servedMismatchReason marks a height the store no longer holds a generation
// for, whose canonical block does not carry the transactions this node
// preconfirmed there — the open-block loss window, where the producer died
// before sealing and its successor rebuilt the height without the store. The
// live path could not catch it (the node had crashed and lost its pending
// state); only the durable served commitment can.
const servedMismatchReason = "served_mismatch"

// auditReadTimeout bounds one per-height store read.
const auditReadTimeout = 5 * time.Second

// auditCheckpointInterval persists progress mid-pass so a node that keeps
// restarting still converges instead of re-walking the same prefix forever.
const auditCheckpointInterval = 256

// auditFloorEntries bounds the floor read. One entry is all it needs: the
// gateway's served window always begins at a BlockOpen — it skips entries
// until the first open on a cold start and evicts at generation boundaries —
// so the first entry of a Range with after unset names the oldest height the
// store serves whole. A handful of entries rather than one costs nothing and
// leaves the response readable in a log.
const auditFloorEntries = 8

// fetchGeneration reads the latest generation stored at a height. A NotFound
// error means the store holds nothing there.
type fetchGeneration func(ctx context.Context, height uint64) ([]*pb.Entry, error)

// fetchOldest reads the store's earliest retained entries.
type fetchOldest func(ctx context.Context) ([]*pb.Entry, error)

// auditChain is the canonical-chain surface the audit needs: it compares
// stored seals against canonical hashes and never executes anything, so it
// needs no state access.
type auditChain interface {
	CurrentBlock() *types.Header
	GetCanonicalHash(number uint64) common.Hash
	// GetBlockByNumber returns the canonical block at a height, for judging a
	// served commitment against the transactions the block actually carried.
	GetBlockByNumber(number uint64) *types.Block
}

type auditor struct {
	db     ethdb.Database
	chain  auditChain
	fetch  fetchGeneration
	oldest fetchOldest
	// finalized reports the newest finalized height and whether there is
	// one yet. It is the walk's ceiling (see rangeToAudit), and nil on a
	// node with no milestone source — which is not the same as a source
	// reporting nothing final, and gets the opposite treatment.
	finalized func() (uint64, bool)
	advance   func(uint64)
}

type auditVerdict int

const (
	auditMatch    auditVerdict = iota // the stored seal is the canonical block
	auditNoSeal                       // the generation was never sealed: nothing was final
	auditMismatch                     // the store sealed something else
	auditUnknown                      // not comparable (no canonical hash, undecodable seal)
)

type auditSummary struct {
	from    uint64
	through uint64
	// walked counts every height the pass visited; compared counts the ones it
	// judged — against the store's sealed generation, or, where the store could
	// not confirm the height, against this node's served commitment. Reporting
	// only walked cannot tell "audited the range, all matched" from "nothing was
	// comparable", which are very different answers for an operator.
	walked   uint64
	compared uint64
	mismatch uint64
	unknown  uint64
	// alreadyJudged counts mismatches the live path had already recorded, so
	// this pass left the stronger record in place.
	alreadyJudged uint64
	// unheld counts heights the store could not confirm — it answered NOT_FOUND,
	// or held a generation it never sealed — and where this node also kept no
	// served commitment to fall back on. Nothing could be compared, so there is
	// nothing to invalidate, but the watermark still advances over them.
	// (Retention-skipped heights are counted separately, in skipped.)
	unheld  uint64
	skipped uint64
}

// rangeToAudit reports the height range this pass should walk: everything
// between the watermark and finality. ok is false when there is nothing to
// do, which includes the first run on a node that has never audited — that
// seeds the watermark at the current head rather than walking backwards from
// an arbitrary point.
//
// Finality, not the head, is the ceiling. A verdict is only as good as the
// canonical chain it was reached against, and the watermark never rewinds,
// so a height judged before a reorg replaced it would keep a verdict about
// a block that no longer exists. At or below a milestone that cannot happen.
// Heights above it are not judged at all; they report as pendingFrom, which
// is what they are. The lag is a handful of blocks, so coverage is unchanged.
//
// A node with no milestone source falls back to the head: bounding at a
// finality it cannot see would freeze the watermark forever.
func (a *auditor) rangeToAudit() (from, through uint64, ok bool) {
	head, through, ok := a.ceiling()
	if !ok {
		return 0, 0, false
	}

	watermark, stored, err := rawdb.ReadPreconfAuditedThrough(a.db)
	if err != nil {
		// Absence seeds the watermark at the head; an unreadable watermark
		// must not, or a failed read would mark the whole range audited and
		// the monotonic writes would never let it back.
		log.Warn("Sequence store audit watermark unreadable", "err", err)

		return 0, 0, false
	}
	if !stored {
		// Seeded at the head rather than at finality: this judges nothing,
		// it only declines to walk history that predates the node.
		seed := head
		a.persist(seed)
		log.Info("Sequence store audit watermark seeded", "height", seed)

		return 0, 0, false
	}
	if watermark >= through {
		return 0, 0, false
	}

	return watermark + 1, through, true
}

// ceiling is the highest height a pass may judge: the head, bounded by
// finality when there is a source. ok is false when nothing is safe to judge.
func (a *auditor) ceiling() (head, through uint64, ok bool) {
	current := a.chain.CurrentBlock()
	if current == nil || current.Number == nil {
		return 0, 0, false
	}
	head = current.Number.Uint64()
	through = head

	if a.finalized != nil {
		final, have := a.finalized()
		if !have {
			return 0, 0, false // nothing is final yet; nothing is safe to judge
		}

		through = min(through, final)
	}

	return head, through, true
}

// hasRangeToWalk is rangeToAudit without its first-run seeding, for a caller
// that must not persist anything.
func (a *auditor) hasRangeToWalk() bool {
	watermark, stored, err := rawdb.ReadPreconfAuditedThrough(a.db)
	if err != nil || !stored {
		return false
	}
	_, through, ok := a.ceiling()

	return ok && watermark < through
}

// run walks from the watermark to the head and records the heights where the
// store's final generation disagrees with the canonical chain. The store's
// retention is the only bound on the walk: a second bound on this side would
// be one operators had to keep aligned with the store's.
func (a *auditor) run(ctx context.Context) (auditSummary, error) {
	var summary auditSummary
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	// Heights at or below the watermark are never walked again, and the live
	// path never clears a commitment once the head has passed it. Judge them
	// unconditionally, before rangeToAudit: after a rewind there may be no
	// range to walk for many passes.
	if err := a.sweepBelowWatermark(ctx, &summary); err != nil {
		return summary, err
	}

	from, through, ok := a.rangeToAudit()
	if !ok {
		return summary, nil
	}
	if a.fetch == nil {
		// The caller saw no range before dialing; one appeared since.
		// The next pass walks it.
		return summary, nil
	}

	summary.through = through
	summary.from, summary.skipped = a.skipToStoreFloor(ctx, from, through)

	// Skipped heights are never walked, so the store fetch that would trigger
	// their served fallback never runs. The store no longer holds them, but a
	// served commitment reconciles against the canonical chain regardless:
	// judge the ones that exist so a promise in the skipped range is recorded
	// and cleared, not advanced past unseen.
	if summary.skipped > 0 {
		if err := a.reconcileSkippedServed(ctx, from, summary.from-1, &summary); err != nil {
			return summary, err
		}
	}

	log.Info("Auditing sequence store against canonical chain", "from", summary.from, "through", through)

	for height := summary.from; height <= through; height++ {
		if err := ctx.Err(); err != nil {
			a.persist(height - 1)
			return summary, err
		}

		if err := a.auditHeightInto(ctx, height, &summary); err != nil {
			a.persist(height - 1)
			return summary, err
		}

		if (height-summary.from+1)%auditCheckpointInterval == 0 {
			a.persist(height)
		}
	}

	a.persist(through)
	a.report(&summary)

	return summary, nil
}

// report closes a pass. The uncompared counts are logged at warning level
// because the watermark has advanced over those heights: a later query
// covering them finds no invalidation, and that absence is not evidence they
// were checked.
func (a *auditor) report(summary *auditSummary) {
	log.Info("Sequence store audit complete", "from", summary.from, "through", summary.through,
		"walked", summary.walked, "compared", summary.compared,
		"mismatched", summary.mismatch, "uncomparable", summary.unknown,
		"alreadyJudged", summary.alreadyJudged, "unheld", summary.unheld)

	if summary.unheld != 0 {
		log.Warn("Sequence store held nothing for part of the audited range",
			"heights", summary.unheld, "from", summary.from, "through", summary.through)
	}
}

// skipToStoreFloor advances from past the heights the store no longer serves,
// and reports how many it passed over. Walking them would be one NOT_FOUND per
// height; the floor answers the whole run in one read.
//
// It is an optimisation, not a correctness boundary — an unresolved floor
// leaves the walk to find the same heights unheld, one at a time. Either way
// the watermark ends up above them without having compared them, which is
// what the counter is for.
func (a *auditor) skipToStoreFloor(ctx context.Context, from, through uint64) (uint64, uint64) {
	floor, ok := a.storeFloor(ctx)
	if !ok || floor <= from {
		return from, 0
	}

	last := floor - 1
	if last > through {
		last = through
	}
	skipped := last - from + 1
	auditRetentionSkipped.Inc(int64(skipped))
	log.Warn("Sequence store no longer retains part of the unaudited range",
		"from", from, "through", last, "heights", skipped)

	return floor, skipped
}

// storeFloor resolves the oldest height the store still serves in full: the
// block opened by the first entry of a Range with after unset.
//
// The store's contract is that a served window begins at a BlockOpen, so
// anything else as the first entry means the read is not the one this
// expects, and the floor goes unresolved rather than guessed. That costs
// nothing but the walk finding the same heights unheld one at a time, which
// is where it started.
func (a *auditor) storeFloor(ctx context.Context) (uint64, bool) {
	if a.oldest == nil {
		return 0, false
	}

	entries, err := a.oldest(ctx)
	if err != nil {
		log.Debug("Sequence store audit could not resolve the retention floor", "err", err)

		return 0, false
	}

	return floorFromEntries(entries)
}

// floorFromEntries reads the floor off a served window's first entry.
func floorFromEntries(entries []*pb.Entry) (uint64, bool) {
	if len(entries) == 0 {
		return 0, false
	}

	open := entries[0].GetBlockOpen()
	if open == nil {
		log.Warn("Sequence store served a window that does not start at an open",
			"kind", fmt.Sprintf("%T", entries[0].GetKind()))

		return 0, false
	}

	return open.GetBlockNumber(), true
}

// auditHeightInto compares one height and folds the verdict into summary. An
// error is a store read that failed, which ends the pass; a height the store
// does not hold is not an error.
func (a *auditor) auditHeightInto(ctx context.Context, height uint64, summary *auditSummary) error {
	entries, err := a.fetch(ctx, height)
	switch {
	case err == nil:
	case isNotFound(err):
		// The store holds no generation here. It may have held one this node
		// served preconfirmations from and then lost (producer died before
		// sealing, successor rebuilt store-blind): the store cannot answer,
		// but the node's own served commitment can. Reconcile against that;
		// with no commitment the height is genuinely unheld.
		summary.walked++
		a.reconcileServed(height, summary)

		return nil
	default:
		return err
	}

	summary.walked++
	verdict := auditHeight(entries, height, a.chain.GetCanonicalHash(height))
	if verdict == auditNoSeal {
		// The store holds an unsealed generation, so it confirmed nothing
		// canonical here — no differently than holding nothing at all. This is
		// the loss window's other half: the producer streamed the open and then
		// died before sealing. Fall back to the served commitment, or the
		// broken promise it left behind goes unrecorded.
		a.reconcileServed(height, summary)

		return nil
	}

	summary.compared++
	a.recordVerdict(height, verdict, summary)

	// auditUnknown means the store held entries but its seal is unusable — it
	// does not decode, or it names the wrong height. An unusable seal cannot
	// confirm the height canonical, so it must not clear a served commitment:
	// judge the commitment against canonical instead of dropping it, or a
	// garbage seal — from a corrupt store, or a malicious one — would bury a
	// broken preconfirmation unjudged before the watermark advances past it.
	if verdict == auditUnknown {
		count, digest, ok, err := rawdb.ReadPreconfServed(a.db, height)
		if err != nil {
			log.Warn("Served preconf commitment unreadable", "number", height, "err", err)
		}
		if err == nil && ok {
			a.judgeServed(height, count, digest, summary)

			return nil
		}
	}
	a.clearServed(height)

	return nil
}

// reconcileServed judges an unheld height against the commitment this node
// kept for the preconfirmations it served there. With no commitment nothing
// was served and the height is unheld; otherwise the served transactions must
// be the canonical block's leading transactions, in order, or a
// preconfirmation was broken and goes on record.
func (a *auditor) reconcileServed(height uint64, summary *auditSummary) {
	count, digest, ok, err := rawdb.ReadPreconfServed(a.db, height)
	if err != nil {
		log.Warn("Served preconf commitment unreadable", "number", height, "err", err)
	}
	if err != nil || !ok {
		// No commitment to fall back on (never served here, or unreadable), so
		// nothing was promised that can be judged: the height is unheld.
		summary.unheld++
		auditUnheldHeights.Inc(1)

		return
	}

	summary.compared++
	a.judgeServed(height, count, digest, summary)
}

type servedVerdict int

const (
	servedVerified    servedVerdict = iota // canonical carries the served prefix, same context
	servedDiverged                         // canonical differs, or is shorter than the served prefix
	servedUnjudgeable                      // the canonical block is not available locally
)

// judgeServed compares an already-read served commitment against the canonical
// block and records served_mismatch if they diverge, then clears the
// commitment. The caller owns the walked/compared/unheld bookkeeping; this only
// folds in the mismatch verdict and drops the now-judged commitment.
func (a *auditor) judgeServed(height, count uint64, digest common.Hash, summary *auditSummary) {
	switch a.judgeServedAgainstCanonical(height, count, digest) {
	case servedVerified:
		auditServedVerified.Inc(1)
		a.clearServed(height)
	case servedDiverged:
		summary.mismatch++
		auditServedMismatch.Inc(1)
		a.recordMismatch(height, servedMismatchReason, "Failed to record served preconf mismatch", summary)
		a.clearServed(height)
	case servedUnjudgeable:
		// The canonical block is not available locally — the header is
		// canonical but its body is pruned, or a snap-sync gap. The commitment
		// cannot be compared, so count it uncomparable rather than recording a
		// spurious mismatch (which would be a false entry in the ledger), and
		// leave it in place so a later pass can judge it if the body arrives.
		summary.unknown++
		auditUnknownCount.Inc(1)
	}
}

// reconcileSkippedServed judges the served commitments in a range the walk
// does not visit. It scans the served prefix, so it visits only the heights
// that actually carry a commitment.
func (a *auditor) reconcileSkippedServed(ctx context.Context, from, to uint64, summary *auditSummary) error {
	for _, height := range rawdb.ReadServedPreconfHeightsInRange(a.db, from, to) {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.reconcileServed(height, summary)
	}

	return nil
}

// sweepBelowWatermark judges the served commitments the walk never reaches,
// bounded by finality like the walk: a verdict above it is about a block that
// can still change.
func (a *auditor) sweepBelowWatermark(ctx context.Context, summary *auditSummary) error {
	watermark, stored, err := rawdb.ReadPreconfAuditedThrough(a.db)
	if err != nil || !stored {
		return nil
	}
	_, through, ok := a.ceiling()
	if !ok {
		return nil
	}

	return a.reconcileSkippedServed(ctx, 0, min(watermark, through), summary)
}

// judgeServedAgainstCanonical compares the served commitment at a height with
// the canonical block. servedVerified: the block carries the count served
// transactions as its leading prefix, in the same order and against the same
// execution context. servedDiverged: it does not, or it is shorter than the
// prefix (content was promised the chain did not keep). servedUnjudgeable: the
// canonical block is not available locally, so nothing can be concluded — a
// missing body must not read as a broken promise.
//
// Receipts are not compared: execution is deterministic given the parent state
// (pinned by ParentHash), the ordered transaction prefix, and the header
// context, so matching those already implies matching receipts. A receipts fold
// here would be redundant and would need canonical receipts the audit, unlike
// the live path, does not hold.
func (a *auditor) judgeServedAgainstCanonical(height, count uint64, digest common.Hash) servedVerdict {
	block := a.chain.GetBlockByNumber(height)
	if block == nil {
		return servedUnjudgeable
	}
	txs := block.Transactions()
	if uint64(len(txs)) < count {
		return servedDiverged
	}
	if foldServed(contextSeed(block.Header()), txs[:count]) == digest {
		return servedVerified
	}

	return servedDiverged
}

// contextSeed is the value the served fold starts from: a keccak over the
// block's execution context — the same fields the live path's
// sameExecutionContext checks (ParentHash, Number, Time, GasLimit, BaseFee,
// Difficulty). Seeding the fold with it makes the served commitment match
// canonical only when the context matches too, so a producer handover that
// rebuilds the height with a different context (succession-based Difficulty, and
// often Time or parent) is caught even when the transactions are unchanged.
func contextSeed(h *types.Header) common.Hash {
	if h == nil || h.Number == nil {
		return common.Hash{}
	}

	var buf []byte
	buf = append(buf, h.ParentHash.Bytes()...)
	buf = binary.BigEndian.AppendUint64(buf, h.Number.Uint64())
	buf = binary.BigEndian.AppendUint64(buf, h.Time)
	buf = binary.BigEndian.AppendUint64(buf, h.GasLimit)
	buf = append(buf, bigTo32(h.BaseFee)...)
	buf = append(buf, bigTo32(h.Difficulty)...)

	return crypto.Keccak256Hash(buf)
}

// bigTo32 left-pads a big.Int to 32 bytes; a nil value folds as zero.
func bigTo32(v *big.Int) []byte {
	if v == nil {
		return make([]byte, common.HashLength)
	}

	return common.BigToHash(v).Bytes()
}

// clearServed drops the served commitment once the audit has judged the
// height; it is only needed until then.
func (a *auditor) clearServed(height uint64) {
	clearServedPreconf(a.db, height)
}

// clearServedPreconf drops a served commitment once its height is judged —
// on the audit walk, or on the live head path in markCanonicalHeadAudited.
func clearServedPreconf(db ethdb.KeyValueWriter, height uint64) {
	if err := rawdb.DeletePreconfServed(db, height); err != nil {
		log.Warn("Failed to clear served preconf commitment", "number", height, "err", err)
	}
}

// recordMismatch writes an invalidation for a height, unless the live path
// already recorded a stronger one there — that record stands and the height
// counts as alreadyJudged instead.
func (a *auditor) recordMismatch(height uint64, reason, failLog string, summary *auditSummary) {
	wrote, err := rawdb.WriteInvalidPreconfIfAbsent(a.db, height, reason)
	switch {
	case err != nil:
		log.Warn(failLog, "number", height, "err", err)
	case !wrote:
		summary.alreadyJudged++
	}
}

// foldServed folds transaction hashes into a running commitment in served
// order: the result changes if any transaction in the prefix is reordered,
// dropped, or replaced. The serve path and the audit fold identically, so
// their commitments are comparable.
//
// This is deliberately not the sequence-store commitment.Fold scheme. That one
// folds raw transaction bytes under a domain tag for the on-wire stream; this
// digest never leaves the node — it is only ever compared against itself, serve
// time versus audit time — so it folds the already-cached tx.Hash() instead,
// which is cheaper on the serve path and needs no tag or chain seed.
func foldServed(prev common.Hash, txs types.Transactions) common.Hash {
	for _, tx := range txs {
		prev = crypto.Keccak256Hash(prev.Bytes(), tx.Hash().Bytes())
	}

	return prev
}

func (a *auditor) recordVerdict(height uint64, verdict auditVerdict, summary *auditSummary) {
	switch verdict {
	case auditMismatch:
		summary.mismatch++
		auditMismatchCount.Inc(1)

		// A record already at this height came from the live path, which
		// served a preconfirmation and then invalidated it. That is a
		// stronger claim than this pass can make, so it stands.
		a.recordMismatch(height, unobservedMismatchReason, "Failed to record unobserved preconfirmation mismatch", summary)
	case auditUnknown:
		// Held but undecidable — no canonical hash, or a seal that does not
		// decode or sits at the wrong height. Counted and logged rather than
		// marked invalid: all three causes are a store or data fault, not a
		// verdict about the preconfirmation at that height.
		summary.unknown++
		auditUnknownCount.Inc(1)
	case auditMatch, auditNoSeal:
	}
}

// persist raises the watermark. advance is injected so the consumer can route
// every write through one mutex-guarded path; a bare auditor (tests) writes
// directly.
func (a *auditor) persist(number uint64) {
	if a.advance != nil {
		a.advance(number)
		return
	}
	current, stored, err := rawdb.ReadPreconfAuditedThrough(a.db)
	if err != nil {
		log.Warn("Sequence store audit watermark unreadable; holding", "height", number, "err", err)

		return
	}
	if stored && current >= number {
		return
	}

	if err := rawdb.WritePreconfAuditedThrough(a.db, number); err != nil {
		log.Warn("Failed to persist sequence store audit watermark", "height", number, "err", err)
	}
}

// auditHeight compares the seal a generation ends with against the canonical
// hash at that height.
func auditHeight(entries []*pb.Entry, height uint64, canonical common.Hash) auditVerdict {
	seal := lastSeal(entries)
	if seal == nil {
		return auditNoSeal
	}
	if canonical == (common.Hash{}) {
		return auditUnknown
	}

	header, err := decodeSealHeader(seal.GetHeader())
	if err != nil {
		log.Warn("Sequence store audit found an undecodable seal", "number", height, "err", err)
		return auditUnknown
	}
	if header.Number == nil || header.Number.Uint64() != height {
		log.Warn("Sequence store audit found a seal at the wrong height", "number", height, "sealed", header.Number)
		return auditUnknown
	}
	if header.Hash() != canonical {
		return auditMismatch
	}

	return auditMatch
}

func lastSeal(entries []*pb.Entry) *pb.BlockSeal {
	for i := len(entries) - 1; i >= 0; i-- {
		if seal := entries[i].GetBlockSeal(); seal != nil {
			return seal
		}
	}

	return nil
}

// requestAudit asks for an audit pass without waiting for one. The trigger
// holds a single slot: a pass already queued covers everything a second
// request would, since the range is recomputed when the pass starts.
func (c *Consumer) requestAudit() {
	select {
	case c.auditTrigger <- struct{}{}:
	default:
	}
}

// auditLoop runs audit passes off the session loop, so closing a gap never
// delays a reconnect and the node keeps serving preconfirmations at the tip
// while the walk runs.
func (c *Consumer) auditLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.auditTrigger:
			c.runAuditPass(ctx)
		}
	}
}

func (c *Consumer) runAuditPass(ctx context.Context) {
	audit := &auditor{db: c.chain.DB(), chain: c.chain, advance: c.advanceAudited}

	// Left nil on a node that wires no milestone source, which is a
	// different answer from a source that has nothing final yet: the first
	// falls back to the head, the second judges nothing.
	if c.finality != nil {
		audit.finalized = c.finalizedHeight
	}

	// Only the walk needs the store; the sweep below the watermark is local
	// and runs every pass. Dial only when there is a range — grpc.NewClient
	// is lazy, so this saves the client, its teardown and a log line per
	// retry, not a connection. run skips the walk when fetch is nil and owns
	// the first-run seeding rangeToAudit does, so decide from a plain read.
	if audit.hasRangeToWalk() {
		conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(pendingInputLimit+1024*1024)))
		if err != nil {
			log.Warn("Sequence store audit could not dial", "err", err)
		} else {
			defer func() {
				if cerr := conn.Close(); cerr != nil {
					log.Warn("Sequence store audit connection close", "err", cerr)
				}
			}()

			client := pb.NewConsumerServiceClient(conn)
			audit.fetch = fetchGenerationVia(client)
			audit.oldest = fetchOldestVia(client)
		}
	}

	if _, err := audit.run(ctx); err != nil && ctx.Err() == nil {
		log.Warn("Sequence store audit stopped early", "err", err)
	}
}

func fetchGenerationVia(client pb.ConsumerServiceClient) fetchGeneration {
	return func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		readCtx, cancel := context.WithTimeout(ctx, auditReadTimeout)
		defer cancel()

		resp, err := client.GetBlock(readCtx, &pb.GetBlockRequest{BlockNumber: height})
		if err != nil {
			return nil, err
		}

		return resp.GetEntries(), nil
	}
}

func fetchOldestVia(client pb.ConsumerServiceClient) fetchOldest {
	return func(ctx context.Context) ([]*pb.Entry, error) {
		readCtx, cancel := context.WithTimeout(ctx, auditReadTimeout)
		defer cancel()

		// after unset resolves to the earliest retained entry.
		resp, err := client.Range(readCtx, &pb.RangeRequest{Limit: auditFloorEntries})
		if err != nil {
			return nil, err
		}

		return resp.GetEntries(), nil
	}
}

// advanceAudited raises the persisted audit watermark. It never lowers it: the
// audit pass and the canonical-head path both advance it, and a pass that
// finishes after the live path has moved on must not rewind the mark.
//
// A verdict is only ever as good as the canonical chain it was reached
// against, which is why nothing above finality is judged: at or below a
// milestone a reorg cannot replace a height this mark has passed, so a
// monotonic mark needs no rewinding and no re-walk.
func (c *Consumer) advanceAudited(number uint64) {
	c.auditMu.Lock()
	defer c.auditMu.Unlock()

	db := c.chain.DB()
	current, stored, err := rawdb.ReadPreconfAuditedThrough(db)
	if err != nil {
		log.Warn("Sequence store audit watermark unreadable; holding", "height", number, "err", err)

		return
	}
	if stored && current >= number {
		return
	}

	if err := rawdb.WritePreconfAuditedThrough(db, number); err != nil {
		log.Warn("Failed to persist sequence store audit watermark", "height", number, "err", err)
	}
}
