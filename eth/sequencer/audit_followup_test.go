package sequencer

import (
	"context"
	"testing"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestAuditHeadAtOrBelowWatermarkQueuesSweep(t *testing.T) {
	for _, offset := range []uint64{0, 5} {
		t.Run(map[uint64]string{0: "at watermark", 5: "below watermark"}[offset], func(t *testing.T) {
			h := startExecHarness(t)
			c := newAuditTestConsumer(h)
			c.endpoint = "unknown-scheme://%%"
			c.watching.Store(true)
			head := h.chain.CurrentBlock().Number.Uint64()
			if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), head+offset); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				c.persistServed(head, 3, common.Hash{0xab})
				c.markCanonicalHeadAudited()
				select {
				case <-c.auditTrigger:
					c.runAuditPass(t.Context())
				default:
					t.Fatal("head reconciliation did not queue the outstanding-commitment sweep")
				}
				wantServedMismatch(t, h.chain.DB(), head, head)
			}
			if got := auditedThrough(t, h.chain.DB()); got != head+offset {
				t.Fatalf("watermark = %d, want %d", got, head+offset)
			}
		})
	}
}

func TestAuditReconcilesCommitmentWrittenDuringFetch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		storeMismatch bool
		missingBody   bool
	}{
		{name: "matching seal"},
		{name: "matching seal with missing body", missingBody: true},
		{name: "mismatching seal", storeMismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startExecHarness(t)
			c := newAuditTestConsumer(h)
			chain, sealed := auditFixture(t, 7)
			block := canonicalBlock(7, servedTxs(3))
			chain.blocks[7], chain.hashes[7], sealed[7] = block, block.Hash(), block.Header()
			if tc.storeMismatch {
				sealed[7] = testHeader(7, common.Hash{0xee})
			}
			if tc.missingBody {
				delete(chain.blocks, 7)
			}
			c.persistServed(7, 3, servedDigest(7, servedTxs(3)))
			fetch := fetchFrom(t, sealed)
			a := &auditor{
				db: h.chain.DB(), chain: chain, servedMu: &c.servedMu,
				fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
					entries, err := fetch(ctx, height)
					c.persistServed(height, 4, servedDigest(height, servedTxs(4)))
					return entries, err
				},
			}
			if err := a.auditHeightInto(t.Context(), 7, new(auditSummary)); err != nil {
				t.Fatal(err)
			}
			_, _, pending, err := rawdb.ReadPreconfServed(h.chain.DB(), 7)
			if err != nil || pending != tc.missingBody {
				t.Fatalf("pending = %v, err = %v, want %v", pending, err, tc.missingBody)
			}
			records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), 7, 7)
			if tc.missingBody {
				if len(records) != 0 {
					t.Fatalf("unjudgeable promise recorded as invalid: %v", records)
				}
			} else if len(records) != 1 {
				t.Fatalf("replacement promise disappeared without an invalidation: %v", records)
			}
		})
	}
}
