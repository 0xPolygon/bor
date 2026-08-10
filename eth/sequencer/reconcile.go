package sequencer

import (
	"bytes"
	"context"
	"errors"
	"time"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

var errRefold = errors.New("refold: unknown entry kind")

type reconcileOutcome int

const (
	recOK reconcileOutcome = iota
	recRetry
	recTerminal
)

// reconcile reads the store tail from the best-known position and classifies
// it against the local lineage. It runs on the transport
// goroutine; the enqueue side keeps folding throughout.
func (p *Publisher) reconcile(ctx context.Context) reconcileOutcome {
	start := time.Now()
	defer func() { reconcileTimer.UpdateSince(start) }()

	publishStateGauge.Update(gaugeResyncing)

	info, out := p.readTail(ctx)
	if out != recOK {
		return out
	}

	return p.applyTail(info)
}

// readTail walks the position ladder: last acked head, then a
// block-anchor probe, then the floor read. The probe starts from the
// in-flight build height when there is one, else from the chain's last
// imported block — a cold start locates the store tail from the local
// database rather than any persisted hint.
func (p *Publisher) readTail(ctx context.Context) (tailInfo, reconcileOutcome) {
	p.mu.Lock()
	anchor, confirmed, probeFrom := p.anchor, p.confirmed, p.curHeight
	p.mu.Unlock()

	if confirmed {
		actx, cancel := sliceDeadline(ctx)
		info, out, done := p.tryWalk(actx, headReq(anchor), true)
		cancel()

		if done {
			return info, out
		}
	}

	if probeFrom == 0 && p.chain != nil {
		if head := p.chain.CurrentBlock(); head != nil {
			probeFrom = head.Number.Uint64()
		}
	}

	if probeFrom > 0 {
		if info, out, done := p.read.probedWalk(ctx, probeFrom); done {
			return info, out
		}
	}

	return p.read.floorRead(ctx)
}

// sliceDeadline halves the parent's remaining budget, so the anchor rung
// cannot starve the probe and floor rungs below it.
func sliceDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, time.Now().Add(time.Until(dl)/2))
}

// tryWalk runs one reader rung with the publisher's policy attached: a
// matched walk compares tail entries against the unconfirmed journal, and a
// fold divergence there is version skew — terminal for this publisher, not
// for the reader.
func (p *Publisher) tryWalk(ctx context.Context, first *pb.RangeRequest, match bool) (tailInfo, reconcileOutcome, bool) {
	info, out, done := p.read.tryWalk(ctx, first, p.absorber(match))
	if done && out == recTerminal {
		p.fail("fold divergence", "err", errFoldDivergence)
	}

	return info, out, done
}

// absorber arms the divergence check for the anchor rung; unmatched walks
// carry no hook.
func (p *Publisher) absorber(match bool) func(*pb.Entry) error {
	if !match {
		return nil
	}

	p.mu.Lock()
	items, _ := p.journal.after(p.ackedSeq)
	snap := append([]journalItem(nil), items...)
	p.mu.Unlock()

	m := &matcher{snap: snap, on: true}

	return m.absorb
}

// matcher runs the divergence check on the anchor rung: tail entries are
// compared in lockstep with our unconfirmed journal items.
type matcher struct {
	snap []journalItem
	idx  int
	on   bool
}

func (m *matcher) absorb(entry *pb.Entry) error {
	if !m.on || m.idx >= len(m.snap) {
		m.on = false

		return nil
	}

	item := m.snap[m.idx]
	if !contentEqual(entry, item.entry) {
		m.on = false

		return nil
	}

	if !bytes.Equal(entryPrefix(entry), entryPrefix(item.entry)) {
		return errFoldDivergence
	}

	m.idx++

	return nil
}
