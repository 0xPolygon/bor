package peerpolicy

import "github.com/ethereum/go-ethereum/common/mclock"

// Limited contribution is weaker evidence than a protocol fault. Its score
// stays bounded rather than accumulating with elapsed time or scrape frequency.
const behaviorRisk = 20

func (s *ContributionSnapshot) score() {
	if !s.Protected && (s.Status == "idle" || s.Status == "request-only") {
		s.Risk = behaviorRisk
		s.RiskReason = s.Status
	}
}

func (t *Tracker) snapshotPeer(id string, p *peerRecord, now mclock.AbsTime, windows map[string]uint64) (Snapshot, bool) {
	connected := p.activity.registered
	if t.classResolver != nil {
		protected, live := t.classResolver(id)
		connected = connected && live
		if live {
			p.setExempt(protected)
		}
	}
	t.prepareActivity(&p.activity, now)
	contribution := p.activity.snapshot(now, t.ready && connected, p.exempt)
	abuse := p.risk(int64(now)/int64(windowWidth), windows)
	risk := max(abuse, contribution.Risk)
	return Snapshot{
		Mode: "observe", Risk: risk, AbuseRisk: abuse, Action: actionFor(risk),
		Windows: windows, Contribution: contribution,
	}, connected
}
