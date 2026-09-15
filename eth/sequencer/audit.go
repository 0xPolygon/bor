package sequencer

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// unobservedMismatchReason marks a height whose stored generation never became
// canonical and which this node never served a preconfirmation from — the
// audit found it after the fact. The other reasons all mean a preconfirmation
// was published to callers and then invalidated, which is a stronger claim.
const unobservedMismatchReason = "unobserved_mismatch"

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
	// walked counts every height the pass visited; compared counts the ones
	// the store actually held a generation for. Reporting only walked cannot
	// tell "audited the range, all matched" from "the store held nothing for
	// any of it", which are very different answers for an operator.
	walked   uint64
	compared uint64
	mismatch uint64
	unknown  uint64
	// alreadyJudged counts mismatches the live path had already recorded, so
	// this pass left the stronger record in place.
	alreadyJudged uint64
	// unheld counts heights the store answered NOT_FOUND for, and skipped the
	// heights its retention floor had already passed when this pass started.
	// Nothing was promised at either, so there is nothing to invalidate — but
	// nothing was compared either, and the watermark advances over both.
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
	head := a.chain.CurrentBlock()
	if head == nil || head.Number == nil {
		return 0, 0, false
	}
	through = head.Number.Uint64()

	if a.finalized != nil {
		final, have := a.finalized()
		if !have {
			return 0, 0, false // nothing is final yet; nothing is safe to judge
		}

		through = min(through, final)
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
		seed := head.Number.Uint64()
		a.persist(seed)
		log.Info("Sequence store audit watermark seeded", "height", seed)

		return 0, 0, false
	}
	if watermark >= through {
		return 0, 0, false
	}

	return watermark + 1, through, true
}

// run walks from the watermark to the head and records the heights where the
// store's final generation disagrees with the canonical chain. The store's
// retention is the only bound on the walk: a second bound on this side would
// be one operators had to keep aligned with the store's.
func (a *auditor) run(ctx context.Context) (auditSummary, error) {
	from, through, ok := a.rangeToAudit()
	if !ok {
		return auditSummary{}, nil
	}

	summary := auditSummary{through: through}
	summary.from, summary.skipped = a.skipToStoreFloor(ctx, from, through)

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
		// The store holds nothing here: it was down, the producer never
		// published, or retention aged the height out mid-walk. Nothing was
		// promised, so there is nothing to invalidate, and the walk advances
		// — counted, because the height went uncompared.
		summary.walked++
		summary.unheld++
		auditUnheldHeights.Inc(1)

		return nil
	default:
		return err
	}

	summary.walked++
	summary.compared++
	a.recordVerdict(height, auditHeight(entries, height, a.chain.GetCanonicalHash(height)), summary)

	return nil
}

func (a *auditor) recordVerdict(height uint64, verdict auditVerdict, summary *auditSummary) {
	switch verdict {
	case auditMismatch:
		summary.mismatch++
		auditMismatchCount.Inc(1)

		// A record already at this height came from the live path, which
		// served a preconfirmation and then invalidated it. That is a
		// stronger claim than this pass can make, so it stands.
		wrote, err := rawdb.WriteInvalidPreconfIfAbsent(a.db, height, unobservedMismatchReason)
		switch {
		case err != nil:
			log.Warn("Failed to record unobserved preconfirmation mismatch", "number", height, "err", err)
		case !wrote:
			summary.alreadyJudged++
		}
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

	// Resolve the range before building a client: the common case is nothing
	// to audit and a trigger fires on every session retry. grpc.NewClient is
	// lazy, so this saves a client and its teardown rather than a connection,
	// and it keeps a bad endpoint from logging once per retry while there is
	// no work to do. run recomputes the range, so removing this changes
	// nothing observable in-process — there is deliberately no test for it.
	from, through, ok := audit.rangeToAudit()
	if !ok {
		return
	}

	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(pendingInputLimit+1024*1024)))
	if err != nil {
		log.Warn("Sequence store audit could not dial", "from", from, "through", through, "err", err)
		return
	}

	defer func() {
		if cerr := conn.Close(); cerr != nil {
			log.Warn("Sequence store audit connection close", "err", cerr)
		}
	}()

	client := pb.NewConsumerServiceClient(conn)
	audit.fetch = fetchGenerationVia(client)
	audit.oldest = fetchOldestVia(client)

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
