package vm

import (
	"github.com/ethereum/go-ethereum/core/state"
)

// Compile-time assertions that the two in-tree StateDB implementations
// satisfy the vm.StateDB interface. If upstream go-ethereum adds or changes
// a method on vm.StateDB, the build fails here until both implementations
// are updated — preventing silent drift between the serial and parallel
// execution paths.
var (
	_ StateDB = (*state.StateDB)(nil)
	_ StateDB = (*state.ParallelStateDB)(nil)
)
