package state

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// This file pins the contract that every journal entry type in
// journal.go (the serial journaling system) is consciously handled by
// the parallel V2 path — either via a matching parallelJournalEntry
// kind (jk*) or via a documented "implicit" mechanism.
//
// Background: when StateDB writes through journaled setters
// (SetNonce, SetState, …) it appends a typed entry to the journal so
// RevertToSnapshot can undo the change. ParallelStateDB has its own
// flat journal (parallelJournalEntry with a kind field) that mirrors
// each of these. Drift between the two means a serial revert undoes
// a write that the parallel revert leaves dangling, or vice versa —
// producing different state roots.
//
// When upstream go-ethereum adds a new journalEntry implementer (e.g.,
// because an EIP introduces a new state mutation), this test fails
// until the V2 author either:
//   (a) adds a matching jk* kind plus revertX in
//       parallel_statedb_journal.go (preferred), or
//   (b) adds the entry name to journalEntryImplicitInV2 with a
//       comment explaining how V2 handles the same effect without
//       a journaled entry.

// journalEntryToParallelKind maps a serial journalEntry type name to
// the parallelJournalEntry kind that mirrors it. The string-valued
// kind here is purely a label for failure messages — the real link
// is the existence of a matching revert in parallel_statedb_journal.go.
var journalEntryToParallelKind = map[string]string{
	"createObjectChange":         "jkCreate",
	"createContractChange":       "jkCreate", // CreateContract reuses CreateAccount's journal entry
	"selfDestructChange":         "jkDestruct",
	"balanceChange":              "jkBalance",
	"nonceChange":                "jkNonce",
	"codeChange":                 "jkCode",
	"storageChange":              "jkStorage",
	"transientStorageChange":     "jkTransient",
	"refundChange":               "jkRefund",
	"addLogChange":               "jkLog",
	"accessListAddAccountChange": "jkAccessAddr",
	"accessListAddSlotChange":    "jkAccessSlot",
}

// journalEntryImplicitInV2 lists serial journalEntry types whose effect
// is reproduced by V2 through means other than a journaled parallel
// entry. Each comment must describe the mechanism so a future maintainer
// can verify the equivalence on an upstream merge that touches the same
// effect.
var journalEntryImplicitInV2 = map[string]string{
	"touchChange": "V2 captures EIP-161 touch via BalanceOps[].Amount==0; settle's AddBalanceDirect calls obj.touch() when the account is empty (statedb.go:2554-2558).",
}

// TestV2JournalEntryCoverage parses journal.go to enumerate all types
// implementing the journalEntry interface (i.e., any type with a
// `revert(*StateDB)` method) and asserts each is either mapped to a
// parallelJournalEntry kind or listed in journalEntryImplicitInV2.
func TestV2JournalEntryCoverage(t *testing.T) {
	entries := journalEntryTypes(t, "journal.go")
	if len(entries) == 0 {
		t.Fatal("AST scan found 0 journal entry types — parser regression?")
	}

	var unmapped []string
	seenKindMap := make(map[string]bool)
	seenImplicit := make(map[string]bool)
	for _, name := range entries {
		if _, ok := journalEntryToParallelKind[name]; ok {
			seenKindMap[name] = true
			continue
		}
		if _, ok := journalEntryImplicitInV2[name]; ok {
			seenImplicit[name] = true
			continue
		}
		unmapped = append(unmapped, name)
	}

	if len(unmapped) > 0 {
		sort.Strings(unmapped)
		t.Errorf(`journal.go contains entry types with no V2 handling:

  %s

For each, do ONE of:
  (a) Add a matching jk* kind in parallel_statedb_journal.go and a
      revertX implementation, then map the type in
      journalEntryToParallelKind.
  (b) If V2 reproduces the effect implicitly, add to
      journalEntryImplicitInV2 with a comment explaining how.`,
			strings.Join(unmapped, "\n  "))
	}

	// Stale-entry check: tables list types that no longer exist.
	staleMapping := []string{}
	for name := range journalEntryToParallelKind {
		if !seenKindMap[name] {
			staleMapping = append(staleMapping, name)
		}
	}
	staleImplicit := []string{}
	for name := range journalEntryImplicitInV2 {
		if !seenImplicit[name] {
			staleImplicit = append(staleImplicit, name)
		}
	}
	if len(staleMapping) > 0 {
		sort.Strings(staleMapping)
		t.Errorf("journalEntryToParallelKind has stale entries (no longer in journal.go): %v", staleMapping)
	}
	if len(staleImplicit) > 0 {
		sort.Strings(staleImplicit)
		t.Errorf("journalEntryImplicitInV2 has stale entries (no longer in journal.go): %v", staleImplicit)
	}
}

// journalEntryTypes parses path (relative to this test file's package)
// and returns the names of every type with a `revert(*StateDB)` method.
// That predicate is the journalEntry interface contract in journal.go.
func journalEntryTypes(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "revert" {
			continue
		}
		// Receiver must take a *StateDB (interface contract).
		if !revertReceivesStateDB(fn) {
			continue
		}
		// Extract the receiver type name (strip pointer if present).
		recvType := fn.Recv.List[0].Type
		if star, ok := recvType.(*ast.StarExpr); ok {
			recvType = star.X
		}
		ident, ok := recvType.(*ast.Ident)
		if !ok {
			continue
		}
		names = append(names, ident.Name)
	}
	sort.Strings(names)
	return names
}

// revertReceivesStateDB reports whether fn's first parameter is a
// *StateDB (matches the journalEntry interface signature).
func revertReceivesStateDB(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "StateDB"
}
