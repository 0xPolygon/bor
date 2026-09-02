//go:build invariants

package state

// assertSettleNotPanicked is the runtime check for "no settle of a
// panicked PDB". See invariants_off.go for the contract.
func (s *ParallelStateDB) assertSettleNotPanicked() {
	if s.Panicked {
		panic("v2 settle invariant violated: SettleTo called on a panicked ParallelStateDB — PanickedIdx propagation broken")
	}
}
