package tracers_test

// Blank import registers the JS tracer evaluator so internal tests in the
// `tracers` package can route through DefaultDirectory.New with a JS source
// string. This makes the JS-driven parallel trace path in api.go reachable
// from tests. External test package mirrors register_native_test.go to avoid
// the cyclic dependency: eth/tracers/js imports eth/tracers.
//
// File name note: this file is intentionally NOT named `register_js_test.go`.
// Go's build system treats the `_js` suffix as a GOOS=js (WebAssembly) build
// tag, which would silently exclude the file from regular builds and leave
// the JS evaluator unregistered.
import _ "github.com/ethereum/go-ethereum/eth/tracers/js"
