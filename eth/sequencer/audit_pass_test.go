package sequencer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
)

type stubAuditChain struct {
	head   uint64
	hashes map[uint64]common.Hash
}

func (s *stubAuditChain) CurrentBlock() *types.Header {
	return &types.Header{Number: new(big.Int).SetUint64(s.head)}
}

func (s *stubAuditChain) GetCanonicalHash(number uint64) common.Hash {
	return s.hashes[number]
}

// auditFixture builds a chain whose canonical hash at every height is the
// header the store also sealed, so the store and the chain agree everywhere
// until a test makes them disagree.
func auditFixture(t *testing.T, through uint64) (*stubAuditChain, map[uint64]*types.Header) {
	t.Helper()

	chain := &stubAuditChain{head: through, hashes: map[uint64]common.Hash{}}
	sealed := map[uint64]*types.Header{}

	for height := uint64(1); height <= through; height++ {
		header := testHeader(height, common.Hash{byte(height)})
		sealed[height] = header
		chain.hashes[height] = header.Hash()
	}

	return chain, sealed
}

func sealedGeneration(t *testing.T, header *types.Header) []*pb.Entry {
	t.Helper()

	raw, err := rlp.EncodeToBytes(header)
	if err != nil {
		t.Fatalf("rlp: %v", err)
	}

	return []*pb.Entry{
		{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: header.Number.Uint64()}}},
		{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{Header: raw}}},
	}
}

func fetchFrom(t *testing.T, sealed map[uint64]*types.Header) fetchGeneration {
	t.Helper()

	return func(_ context.Context, height uint64) ([]*pb.Entry, error) {
		header, ok := sealed[height]
		if !ok {
			return nil, status.Error(codes.NotFound, "unretained")
		}

		return sealedGeneration(t, header), nil
	}
}

func auditedThrough(t *testing.T, db ethdb.Database) uint64 {
	t.Helper()

	number, ok, _ := rawdb.ReadPreconfAuditedThrough(db)
	if !ok {
		t.Fatal("no audit watermark stored")
	}

	return number
}

func TestAuditFirstRunSeedsWatermarkWithoutWalking(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 20)

	walked := 0
	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		walked++
		return fetchFrom(t, sealed)(ctx, height)
	}}

	if _, err := audit.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if walked != 0 {
		t.Fatalf("first run read %d heights, want 0: a node that never audited has no window", walked)
	}

	if got := auditedThrough(t, db); got != 20 {
		t.Fatalf("watermark = %d, want the head 20", got)
	}
}

func TestAuditRecordsMismatchTheNodeNeverServed(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// The store's final generation at height 7 sealed a different block than
	// the one that became canonical, and this node was down for it.
	sealed[7] = testHeader(7, common.Hash{0xee})

	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.from != 5 || summary.through != 12 {
		t.Fatalf("window = [%d,%d], want [5,12]", summary.from, summary.through)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatches = %d, want 1", summary.mismatch)
	}

	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != unobservedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, unobservedMismatchReason)
	}

	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12", got)
	}
}

func TestAuditCleanWindowRecordsNothing(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 30)

	if err := rawdb.WritePreconfAuditedThrough(db, 10); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 0 || summary.unknown != 0 {
		t.Fatalf("summary = %+v, want a clean window", summary)
	}

	if records := rawdb.ReadInvalidPreconfsInRange(db, 0, 30); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}

	if got := auditedThrough(t, db); got != 30 {
		t.Fatalf("watermark = %d, want 30", got)
	}
}

// Heights the store never held, and heights it left open, promised nothing —
// the watermark still has to clear them or the same window is re-walked forever.
func TestAuditAdvancesPastHeightsThatPromisedNothing(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 8)

	delete(sealed, 6) // unretained: NotFound
	unsealed := uint64(7)

	if err := rawdb.WritePreconfAuditedThrough(db, 5); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == unsealed {
			return []*pb.Entry{{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: height}}}}, nil
		}

		return fetchFrom(t, sealed)(ctx, height)
	}}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// 6 is unretained and 7 was never sealed: both walked, only 7 compared.
	if summary.walked != 3 || summary.compared != 2 {
		t.Fatalf("walked/compared = %d/%d, want 3/2", summary.walked, summary.compared)
	}

	if records := rawdb.ReadInvalidPreconfsInRange(db, 0, 8); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}

	if got := auditedThrough(t, db); got != 8 {
		t.Fatalf("watermark = %d, want 8", got)
	}
}

func TestAuditPersistsProgressWhenTheStoreFails(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 40)

	if err := rawdb.WritePreconfAuditedThrough(db, 9); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	transport := errors.New("connection reset")
	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == 15 {
			return nil, transport
		}

		return fetchFrom(t, sealed)(ctx, height)
	}}

	if _, err := audit.run(context.Background()); !errors.Is(err, transport) {
		t.Fatalf("err = %v, want the transport error", err)
	}

	if got := auditedThrough(t, db); got != 14 {
		t.Fatalf("watermark = %d, want 14: progress up to the failing height is kept", got)
	}
}

func TestAuditPersistsProgressOnCancel(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 40)

	if err := rawdb.WritePreconfAuditedThrough(db, 9); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	audit := &auditor{db: db, chain: chain, fetch: func(fctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == 12 {
			cancel()
		}

		return fetchFrom(t, sealed)(fctx, height)
	}}

	defer cancel()

	if _, err := audit.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12", got)
	}
}

func TestAuditDoesNotRewindTheWatermark(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 20)

	if err := rawdb.WritePreconfAuditedThrough(db, 5); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}

	// The live path races ahead while the pass is walking.
	if err := rawdb.WritePreconfAuditedThrough(db, 500); err != nil {
		t.Fatalf("advance: %v", err)
	}

	if _, err := audit.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := auditedThrough(t, db); got != 500 {
		t.Fatalf("watermark = %d, want 500: a finishing pass must not rewind it", got)
	}
}

func TestAuditRangeIsEmptyAtTheHead(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, _ := auditFixture(t, 20)

	if err := rawdb.WritePreconfAuditedThrough(db, 20); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: func(context.Context, uint64) ([]*pb.Entry, error) {
		t.Fatal("audited with nothing to audit")
		return nil, nil
	}}

	if _, _, ok := audit.rangeToAudit(); ok {
		t.Fatal("range is non-empty at the head")
	}

	if _, err := audit.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestAuditHeight(t *testing.T) {
	header := testHeader(9, common.Hash{0x09})
	other := testHeader(9, common.Hash{0xaa})

	cases := []struct {
		name      string
		entries   []*pb.Entry
		canonical common.Hash
		want      auditVerdict
	}{
		{
			name:      "seal is canonical",
			entries:   sealedGeneration(t, header),
			canonical: header.Hash(),
			want:      auditMatch,
		},
		{
			name:      "seal is not canonical",
			entries:   sealedGeneration(t, other),
			canonical: header.Hash(),
			want:      auditMismatch,
		},
		{
			name:      "never sealed",
			entries:   []*pb.Entry{{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: 9}}}},
			canonical: header.Hash(),
			want:      auditNoSeal,
		},
		{
			name:      "no canonical block to compare",
			entries:   sealedGeneration(t, header),
			canonical: common.Hash{},
			want:      auditUnknown,
		},
		{
			name:      "undecodable seal",
			entries:   []*pb.Entry{{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{Header: []byte{0xde, 0xad}}}}},
			canonical: header.Hash(),
			want:      auditUnknown,
		},
		{
			name:      "seal carries the wrong height",
			entries:   sealedGeneration(t, testHeader(11, common.Hash{0x0b})),
			canonical: header.Hash(),
			want:      auditUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auditHeight(tc.entries, 9, tc.canonical); got != tc.want {
				t.Fatalf("verdict = %d, want %d", got, tc.want)
			}
		})
	}
}

// The last seal in a generation is the one that counts: a republished window
// can carry an earlier seal ahead of the one that stands.
func TestLastSealTakesTheFinalSeal(t *testing.T) {
	first := testHeader(4, common.Hash{0x01})
	last := testHeader(4, common.Hash{0x02})

	entries := append(sealedGeneration(t, first), sealedGeneration(t, last)...)

	seal := lastSeal(entries)
	if seal == nil {
		t.Fatal("no seal found")
	}

	header, err := decodeSealHeader(seal.GetHeader())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if header.Hash() != last.Hash() {
		t.Fatal("lastSeal returned an earlier seal")
	}

	if lastSeal(nil) != nil {
		t.Fatal("empty generation yielded a seal")
	}
}

func TestAuditRangeWithoutAHead(t *testing.T) {
	audit := &auditor{db: rawdb.NewMemoryDatabase(), chain: &headlessAuditChain{}}
	if _, _, ok := audit.rangeToAudit(); ok {
		t.Fatal("range resolved without a canonical head")
	}
}

type headlessAuditChain struct{}

func (headlessAuditChain) CurrentBlock() *types.Header         { return nil }
func (headlessAuditChain) GetCanonicalHash(uint64) common.Hash { return common.Hash{} }

// The counters are the pass's report to its caller; without asserting them a
// mutation to any of the tallies goes unnoticed.
func TestAuditSummaryCounts(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 10)

	sealed[7] = testHeader(7, common.Hash{0xee}) // mismatch
	delete(sealed, 8)                            // NotFound
	chain.hashes[9] = common.Hash{}              // uncomparable: no canonical hash

	if err := rawdb.WritePreconfAuditedThrough(db, 5); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Heights 6..10: five walked, but 8 is not in the store, so four were
	// actually compared. One mismatch at 7, one uncomparable at 9.
	if summary.walked != 5 {
		t.Fatalf("walked = %d, want 5", summary.walked)
	}
	if summary.compared != 4 {
		t.Fatalf("compared = %d, want 4: a height the store does not hold is walked, not compared", summary.compared)
	}
	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1", summary.mismatch)
	}
	if summary.unknown != 1 {
		t.Fatalf("unknown = %d, want 1", summary.unknown)
	}
	if summary.unheld != 1 {
		t.Fatalf("unheld = %d, want 1: height 8 was walked but the store held nothing for it", summary.unheld)
	}
	if summary.skipped != 0 {
		t.Fatalf("skipped = %d, want 0 with no retention floor resolved", summary.skipped)
	}
}

func TestRecordVerdict(t *testing.T) {
	cases := []struct {
		name     string
		verdict  auditVerdict
		mismatch uint64
		unknown  uint64
		recorded bool
	}{
		{name: "mismatch", verdict: auditMismatch, mismatch: 1, recorded: true},
		{name: "unknown", verdict: auditUnknown, unknown: 1},
		{name: "match", verdict: auditMatch},
		{name: "no seal", verdict: auditNoSeal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			audit := &auditor{db: db}
			summary := auditSummary{}

			audit.recordVerdict(42, tc.verdict, &summary)

			if summary.mismatch != tc.mismatch {
				t.Fatalf("mismatch = %d, want %d", summary.mismatch, tc.mismatch)
			}
			if summary.unknown != tc.unknown {
				t.Fatalf("unknown = %d, want %d", summary.unknown, tc.unknown)
			}

			records := rawdb.ReadInvalidPreconfsInRange(db, 42, 42)
			if tc.recorded && len(records) != 1 {
				t.Fatalf("records = %+v, want one", records)
			}
			if !tc.recorded && len(records) != 0 {
				t.Fatalf("records = %+v, want none", records)
			}
		})
	}
}

// The watermark is written at the checkpoint interval, so a pass that is
// interrupted repeatedly still converges instead of re-walking its prefix.
func TestAuditCheckpointsMidPass(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	through := uint64(auditCheckpointInterval) + 10
	chain, sealed := auditFixture(t, through)

	if err := rawdb.WritePreconfAuditedThrough(db, 0); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	checkpoint := uint64(auditCheckpointInterval)
	var atCheckpoint uint64
	seen := false

	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == checkpoint+1 && !seen {
			seen = true
			atCheckpoint, _, _ = rawdb.ReadPreconfAuditedThrough(db)
		}

		return fetchFrom(t, sealed)(ctx, height)
	}}

	if _, err := audit.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !seen {
		t.Fatalf("the pass never reached height %d", checkpoint+1)
	}
	if atCheckpoint != checkpoint {
		t.Fatalf("watermark at the checkpoint = %d, want %d", atCheckpoint, checkpoint)
	}
}

// persist routes through the consumer's guarded writer when one is set, and
// writes directly otherwise.
func TestAuditPersistUsesTheInjectedWriter(t *testing.T) {
	db := rawdb.NewMemoryDatabase()

	var advanced []uint64
	audit := &auditor{db: db, advance: func(number uint64) { advanced = append(advanced, number) }}

	audit.persist(11)

	if len(advanced) != 1 || advanced[0] != 11 {
		t.Fatalf("advance calls = %v, want [11]", advanced)
	}
	if _, ok, _ := rawdb.ReadPreconfAuditedThrough(db); ok {
		t.Fatal("persist wrote directly while a writer was injected")
	}
}

// persist holds at the stored height rather than lowering it.
func TestAuditPersistNeverLowersTheWatermark(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	audit := &auditor{db: db}

	audit.persist(20)
	audit.persist(20)
	audit.persist(19)

	if got, _, _ := rawdb.ReadPreconfAuditedThrough(db); got != 20 {
		t.Fatalf("watermark = %d, want 20", got)
	}
}

// failingWriteDB fails every write, direct or batched, so the pass's
// write-error paths run.
type failingWriteDB struct {
	ethdb.Database
}

func (failingWriteDB) Put([]byte, []byte) error { return errWriteRefused }

func (d failingWriteDB) NewBatch() ethdb.Batch {
	return failingBatch{Batch: d.Database.NewBatch()}
}

type failingBatch struct {
	ethdb.Batch
}

func (failingBatch) Write() error { return errWriteRefused }

var errWriteRefused = errors.New("write refused")

// A database that refuses writes must not stop the walk: the pass logs and
// keeps auditing, because the alternative is losing the whole window.
func TestAuditSurvivesWriteFailures(t *testing.T) {
	chain, sealed := auditFixture(t, 100)
	sealed[95] = testHeader(95, common.Hash{0xee})

	backing := rawdb.NewMemoryDatabase()
	if err := rawdb.WritePreconfAuditedThrough(backing, 90); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: failingWriteDB{backing}, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The walk still covered the window and still counted the mismatch, even
	// though none of the three writes it attempted could land.
	if summary.walked != 10 {
		t.Fatalf("walked = %d, want 10", summary.walked)
	}
	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1", summary.mismatch)
	}
	if got, _, _ := rawdb.ReadPreconfAuditedThrough(backing); got != 90 {
		t.Fatalf("watermark = %d, want it unchanged at 90 when writes fail", got)
	}
}

// failingReadDB reports a stored watermark it then cannot hand over, which is
// the read-only failure that used to read as "never audited".
type failingReadDB struct {
	ethdb.Database
}

func (failingReadDB) Has([]byte) (bool, error)   { return true, nil }
func (failingReadDB) Get([]byte) ([]byte, error) { return nil, errReadRefused }

var errReadRefused = errors.New("read refused")

// An unreadable watermark must not be treated as absence. Absence seeds the
// mark at the current head, which would declare the whole unaudited window
// clean — and the monotonic writes would never let it back.
func TestAuditDoesNotSeedTheWatermarkOnAReadFailure(t *testing.T) {
	chain, sealed := auditFixture(t, 50)

	// The assertion has to be that nothing was written: "no window resolved"
	// holds whether the read failed or reported absence, so it proves nothing.
	backing := rawdb.NewMemoryDatabase()
	audit := &auditor{db: failingReadDB{backing}, chain: chain, fetch: fetchFrom(t, sealed)}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.walked != 0 {
		t.Fatalf("walked = %d, want 0", summary.walked)
	}

	if number, stored, err := rawdb.ReadPreconfAuditedThrough(backing); stored || err != nil {
		t.Fatalf("watermark = (%d, %v, %v), want nothing written: an unreadable "+
			"mark was seeded at the head, declaring the window clean", number, stored, err)
	}
}

// persist holds when it cannot read the current mark, rather than writing over
// a value it could not compare against.
func TestAuditPersistHoldsOnAReadFailure(t *testing.T) {
	backing := rawdb.NewMemoryDatabase()
	if err := rawdb.WritePreconfAuditedThrough(backing, 9); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: failingReadDB{backing}}
	audit.persist(100)

	if got, _, _ := rawdb.ReadPreconfAuditedThrough(backing); got != 9 {
		t.Fatalf("watermark = %d, want it held at 9", got)
	}
}

func TestAuditLeavesALiveRecordInPlace(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// Height 7 sealed something that never became canonical, and the live
	// path already served a preconfirmation from it and invalidated it.
	sealed[7] = testHeader(7, common.Hash{0xee})
	if err := rawdb.WriteInvalidPreconf(db, 7, "canonical_mismatch"); err != nil {
		t.Fatalf("seed live record: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 {
		t.Fatalf("records = %+v, want one at 7", records)
	}
	// unobserved_mismatch claims nothing was served from the height, which
	// would be a weaker and wrong account of a preconfirmation that was.
	if records[0].Reason != "canonical_mismatch" {
		t.Fatalf("reason = %q, want canonical_mismatch preserved", records[0].Reason)
	}
	if summary.alreadyJudged != 1 {
		t.Fatalf("alreadyJudged = %d, want 1", summary.alreadyJudged)
	}
}

// A node down longer than the store's retention finds nothing anywhere. The
// watermark still advances — the alternative is re-walking the range forever —
// so the count is the only record that nothing was compared.
func TestAuditCountsARangeTheStoreHeldNothingFor(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, _ := auditFixture(t, 12)

	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, map[uint64]*types.Header{})}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.compared != 0 || summary.walked != 8 || summary.unheld != 8 {
		t.Fatalf("walked/compared/unheld = %d/%d/%d, want 8/0/8",
			summary.walked, summary.compared, summary.unheld)
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12", got)
	}
}

// Where the unheld heights sit does not change how they are treated: a
// retention floor at the oldest end and the store having been down for a few
// heights mid-range are the same NOT_FOUND, and both are counted.
func TestAuditCountsUnheldHeightsWhereverTheySit(t *testing.T) {
	cases := []struct {
		name         string
		unheld       []uint64
		wantCompared uint64
	}{
		{name: "at the oldest end of the range", unheld: []uint64{5, 6, 7}, wantCompared: 5},
		{name: "in the middle of the range", unheld: []uint64{9, 10}, wantCompared: 6},
		{name: "at the newest end of the range", unheld: []uint64{11, 12}, wantCompared: 6},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			chain, sealed := auditFixture(t, 12)
			for _, height := range tc.unheld {
				delete(sealed, height)
			}
			if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
				t.Fatalf("seed watermark: %v", err)
			}

			audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
			summary, err := audit.run(context.Background())
			if err != nil {
				t.Fatalf("run: %v", err)
			}

			if summary.compared != tc.wantCompared {
				t.Fatalf("compared = %d, want %d", summary.compared, tc.wantCompared)
			}
			if summary.unheld != uint64(len(tc.unheld)) {
				t.Fatalf("unheld = %d, want %d", summary.unheld, len(tc.unheld))
			}
			if got := auditedThrough(t, db); got != 12 {
				t.Fatalf("watermark = %d, want 12", got)
			}
		})
	}
}

// oldestFrom serves Range from the store's earliest retained entry, one page
// per call, so a test can hand back a page with no block boundary in it. It
// counts its calls into calls, which is how a test tells "stopped scanning"
// from "kept asking".
func oldestFrom(calls *int, pages ...[]*pb.Entry) fetchOldest {
	return func(context.Context, []byte) ([]*pb.Entry, []byte, error) {
		*calls++
		if *calls > len(pages) {
			return nil, nil, nil
		}

		return pages[*calls-1], []byte{byte(*calls)}, nil
	}
}

// The floor is what bounds the walk now, so a pass starts at the oldest height
// the store still serves rather than probing the aged-out ones one at a time.
func TestAuditStartsAtTheStoreRetentionFloor(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 20)

	for height := uint64(1); height < 15; height++ {
		delete(sealed, height)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	probed := map[uint64]bool{}
	fetch := func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		probed[height] = true

		return fetchFrom(t, sealed)(ctx, height)
	}
	audit := &auditor{db: db, chain: chain, fetch: fetch, oldest: oldestFrom(new(int), sealedGeneration(t, sealed[15]))}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.from != 15 {
		t.Fatalf("from = %d, want the retention floor 15", summary.from)
	}
	if summary.skipped != 10 {
		t.Fatalf("skipped = %d, want the ten aged-out heights 5..14", summary.skipped)
	}
	if summary.unheld != 0 {
		t.Fatalf("unheld = %d, want 0: the aged-out heights were skipped, not probed", summary.unheld)
	}
	for height := uint64(5); height < 15; height++ {
		if probed[height] {
			t.Fatalf("height %d was probed despite being below the retention floor", height)
		}
	}
	if got := auditedThrough(t, db); got != 20 {
		t.Fatalf("watermark = %d, want 20", got)
	}
}

// A floor at or below the watermark is not a skip, and one above the head
// cannot report more skipped heights than the range held.
func TestSkipToStoreFloor(t *testing.T) {
	cases := []struct {
		name        string
		floor       *uint64
		wantFrom    uint64
		wantSkipped uint64
	}{
		{name: "unresolved floor leaves the range alone", wantFrom: 10},
		{name: "floor below the range start", floor: ptrToHeight(4), wantFrom: 10},
		{name: "floor at the range start", floor: ptrToHeight(10), wantFrom: 10},
		{name: "floor inside the range", floor: ptrToHeight(15), wantFrom: 15, wantSkipped: 5},
		{name: "floor above the head clamps to the range", floor: ptrToHeight(400), wantFrom: 400, wantSkipped: 11},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audit := &auditor{}
			if tc.floor != nil {
				audit.oldest = oldestFrom(new(int), []*pb.Entry{
					{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: *tc.floor}}},
				})
			}

			from, skipped := audit.skipToStoreFloor(context.Background(), 10, 20)
			if from != tc.wantFrom || skipped != tc.wantSkipped {
				t.Fatalf("from/skipped = %d/%d, want %d/%d", from, skipped, tc.wantFrom, tc.wantSkipped)
			}
		})
	}
}

func ptrToHeight(v uint64) *uint64 { return &v }

// Retention can age out the middle of a block, leaving a page that opens with
// records or a seal. Only an open proves a block is held whole, so a seal
// reached first puts the floor at the height above it, and records alone prove
// nothing about the page's own height.
func TestFloorFromEntries(t *testing.T) {
	header := testHeader(9, common.Hash{0x09})
	raw, err := rlp.EncodeToBytes(header)
	if err != nil {
		t.Fatalf("rlp: %v", err)
	}
	record := &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{Transactions: [][]byte{{0x01}}}}}
	seal := &pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{Header: raw}}}
	open := &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: 12}}}

	cases := []struct {
		name    string
		entries []*pb.Entry
		want    uint64
		wantOk  bool
	}{
		{name: "an open is the floor", entries: []*pb.Entry{open}, want: 12, wantOk: true},
		{name: "records before an open do not move it", entries: []*pb.Entry{record, record, open}, want: 12, wantOk: true},
		{name: "a seal first puts the floor above it", entries: []*pb.Entry{record, seal, open}, want: 10, wantOk: true},
		{name: "records alone resolve nothing", entries: []*pb.Entry{record, record}},
		{name: "an empty page resolves nothing"},
		{name: "an undecodable seal is skipped", entries: []*pb.Entry{
			{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{Header: []byte{0xff}}}}, open,
		}, want: 12, wantOk: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := floorFromEntries(tc.entries)
			if got != tc.want || ok != tc.wantOk {
				t.Fatalf("floor = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
}

// The floor is an optimisation: when the store cannot answer, the walk starts
// at the watermark and discovers the same heights unheld one at a time.
func TestStoreFloorFallsBackWhenTheStoreCannotAnswer(t *testing.T) {
	records := []*pb.Entry{{Kind: &pb.Entry_Record{Record: &pb.Record{Transactions: [][]byte{{0x01}}}}}}

	cases := []struct {
		name      string
		pages     [][]*pb.Entry
		failRead  bool
		wantCalls int
	}{
		{name: "no reader wired"},
		{name: "the read fails", failRead: true, wantCalls: 1},
		// An empty page ends the scan: the store has nothing to hand over,
		// and asking again up to the page budget is wasted round trips.
		{name: "the store returns nothing", pages: [][]*pb.Entry{nil}, wantCalls: 1},
		{name: "no block boundary within the page budget", pages: [][]*pb.Entry{
			records, records, records, records, records,
		}, wantCalls: auditFloorPages},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			audit := &auditor{}

			switch {
			case tc.failRead:
				audit.oldest = func(context.Context, []byte) ([]*pb.Entry, []byte, error) {
					calls++

					return nil, nil, errReadRefused
				}
			case tc.pages != nil:
				audit.oldest = oldestFrom(&calls, tc.pages...)
			}

			if height, ok := audit.storeFloor(context.Background()); ok {
				t.Fatalf("floor = %d, want none resolved", height)
			}
			if calls != tc.wantCalls {
				t.Fatalf("read the store %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// Paging: a block with more records than one page holds still resolves, as
// long as the boundary arrives inside the page budget.
func TestStoreFloorPagesToTheNextBoundary(t *testing.T) {
	records := []*pb.Entry{{Kind: &pb.Entry_Record{Record: &pb.Record{Transactions: [][]byte{{0x01}}}}}}
	audit := &auditor{oldest: oldestFrom(new(int), records, records, []*pb.Entry{
		{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: 31}}},
	})}

	height, ok := audit.storeFloor(context.Background())
	if !ok || height != 31 {
		t.Fatalf("floor = (%d, %v), want (31, true)", height, ok)
	}
}
