//go:build !invariants

package state

// Invariants build tag — production builds use these zero-cost stubs.
// Build with `-tags invariants` to enable the runtime checks defined in
// invariants_on.go.

// assertSettleNotPanicked is called at the start of SettleTo. A panicked
// PDB must never reach settle (the executor + settle callback drop it
// and surface PanickedIdx) — if one slips through, finalDB would be
// corrupted with partial state. The check enforces "no settle of
// panicked PDB" as a runtime invariant.
func (s *ParallelStateDB) assertSettleNotPanicked() {}
