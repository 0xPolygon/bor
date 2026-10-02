package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
)

type fakeDropCandidate struct {
	trusted, static, dynDialed, inbound bool
	lifetime                            mclock.AbsTime
}

func (p fakeDropCandidate) Trusted() bool            { return p.trusted }
func (p fakeDropCandidate) Static() bool             { return p.static }
func (p fakeDropCandidate) DynDialed() bool          { return p.dynDialed }
func (p fakeDropCandidate) Inbound() bool            { return p.inbound }
func (p fakeDropCandidate) Lifetime() mclock.AbsTime { return p.lifetime }

func TestDropperDoNotDrop(t *testing.T) {
	old := mclock.AbsTime(2 * doNotDropBefore)
	cm := &dropper{maxDialPeers: 10, maxInboundPeers: 20}

	tests := []struct {
		name       string
		peer       fakeDropCandidate
		numDialed  int
		numInbound int
		want       bool
	}{
		{"inbound at capacity", fakeDropCandidate{inbound: true, lifetime: old}, 10, 20, false},
		{"inbound static at capacity", fakeDropCandidate{inbound: true, static: true, lifetime: old}, 10, 20, true},
		{"inbound trusted at capacity", fakeDropCandidate{inbound: true, trusted: true, lifetime: old}, 10, 20, true},
		{"inbound below capacity", fakeDropCandidate{inbound: true, lifetime: old}, 10, 19, true},
		{"recent inbound at capacity", fakeDropCandidate{inbound: true}, 10, 20, true},
		{"inbound at the age boundary", fakeDropCandidate{inbound: true, lifetime: mclock.AbsTime(doNotDropBefore)}, 10, 20, false},
		{"dialed at capacity", fakeDropCandidate{dynDialed: true, lifetime: old}, 10, 20, false},
		{"dialed below capacity", fakeDropCandidate{dynDialed: true, lifetime: old}, 9, 20, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cm.doNotDrop(tt.peer, tt.numDialed, tt.numInbound); got != tt.want {
				t.Errorf("doNotDrop = %v, want %v", got, tt.want)
			}
		})
	}
}
