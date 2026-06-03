package tracers_test

// Blank import registers native tracers (callTracer, etc.) so they are available
// to internal (package tracers) tests. This file must use the external test package
// to avoid a cyclic dependency: eth/tracers/native imports eth/tracers.
import _ "github.com/ethereum/go-ethereum/eth/tracers/native"
