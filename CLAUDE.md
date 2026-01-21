# Bor Development Guide for AI Agents

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This guide provides comprehensive instructions for AI agents working on the Bor codebase. It covers the architecture, development workflows, and critical guidelines for effective contributions.

## Project Overview

Bor is the **execution client** of Polygon PoS, forked from go-ethereum. It handles block production, transaction execution, and state management. **Heimdall** is the separate **consensus client** that manages validator selection, checkpointing to Ethereum, and span/sprint coordination. Together they form the complete Polygon PoS stack.

Bor focuses on high throughput, low gas fees, and full EVM compatibility.

## Architecture Overview

### Core Components

1. **Bor Consensus** (`consensus/bor/`): Execution-side consensus logic; validates blocks using validator info from Heimdall, manages sprint-based block production
2. **Core** (`core/`): Blockchain state management, transaction validation, and block processing
3. **Storage** (`ethdb/`): Database abstraction layer supporting LevelDB and Pebble backends
4. **Networking** (`p2p/`): P2P networking stack with peer discovery, sync, and transaction propagation
5. **RPC** (`rpc/`): JSON-RPC server supporting HTTP, WebSocket, and IPC transports
6. **Execution** (`core/vm/`): EVM implementation with BlockSTM parallel execution (`core/blockstm/`)
7. **Sync** (`eth/downloader/`): Block synchronization with full and snap sync modes
8. **Node** (`node/`): Node lifecycle management, service orchestration, and RPC stack
9. **Tracing** (`eth/tracers/`): Transaction tracing with JS, native, and live tracers
10. **CLI** (`cmd/cli/`, `internal/cli/`): Command-line interface with gRPC server for node management
11. **TxPool** (`core/txpool/`): Transaction pool with subpools (legacy, blob) for pending transaction management
12. **Stateless** (`core/stateless/`): Stateless execution engine with witness tracking and verification
13. **WIT Protocol** (`eth/protocols/wit/`): Witness protocol for peer communication and witness data broadcasting

### Key Design Principles

- **Modularity**: Each package can be used as a standalone library
- **Performance**: Goroutines for concurrency, efficient data structures, memory-mapped I/O
- **Extensibility**: Interfaces allow different implementations (consensus engines, databases)
- **Go Idioms**: Explicit error handling, small interfaces, composition over inheritance

## Development Workflow

### Essential Commands

1. **Build**: Build the main node binary

   ```bash
   make bor
   ```

2. **Format**: Always format before committing

   ```bash
   make fmt
   ```

3. **Lint**: Run golangci-lint

   ```bash
   make lint-deps && make lint
   ```

4. **Test**: Run tests before submitting

   ```bash
   make test
   ```

## Testing Guidelines

1. **Unit Tests**: Test individual functions

   ```bash
   go test -v ./path/to/package
   ```

2. **Integration Tests**: Test component interactions

   ```bash
   go test -v -tags=integration ./tests/...
   ```

3. **Race Detection**: Always run before submitting

   ```bash
   go test -race ./...
   ```

4. **Benchmarks**: For performance-critical code

   ```bash
   go test -bench=. -benchmem ./path/to/package
   ```

## Performance Considerations

1. **Avoid Allocations in Hot Paths**: Use `sync.Pool`, preallocate slices
2. **Goroutines**: Use for concurrent/parallel work, but don't spawn unbounded
3. **Channels**: Use for coordination, prefer buffered for producers
4. **Context**: Always propagate for cancellation
5. **Database**: Use `ethdb` interfaces, batch writes when possible

## Before Making Changes

1. **Identify impact**: What other components depend on this code?
2. **Plan implementation**: Outline the approach before writing code
3. **Plan testing**: How will you verify correctness? What edge cases exist?
4. **Check for breaking changes**: Will this affect public APIs, configs, or stored data?

## Common Pitfalls

1. **Don't Block Goroutines**: Avoid sync operations in async contexts
2. **Handle Errors**: Never ignore errors with `_`
3. **Close Resources**: Channels, files, DB iterators, HTTP bodies
4. **Race Conditions**: Use `-race` flag, protect shared state
5. **Nil Checks**: Check interface values and pointers before use

## What to Avoid

1. **Large, sweeping changes**: Keep PRs focused and reviewable
2. **Mixing unrelated changes**: One logical change per PR
3. **Ignoring CI failures**: All checks must pass
4. **Incomplete implementations**: Finish features before submitting

## When to Comment

### DO Comment

- **Non-obvious behavior or edge cases**
- **Performance trade-offs** and why a particular approach was chosen
- **Constraints and assumptions** that aren't obvious from the code
- **Limitations or gotchas** that future developers should know
- **Why simpler alternatives don't work**

```go
// Sprint length must match Heimdall config, otherwise block production halts.
const SprintLength = 16

// Fetch validator set at sprint start, not current block, to ensure
// all nodes agree on the producer for this sprint.
func (c *Bor) GetCurrentValidators(headerHash common.Hash, blockNumber uint64) ([]*Validator, error)

// IsSprintStart returns true if block is first in a sprint.
// Note: Block 0 is not a sprint start since there's no previous sprint to end.
func IsSprintStart(blockNumber uint64, sprintLength uint64) bool

// Map keyed by validator address for O(1) signer lookup during block verification
var signerCache = make(map[common.Address]*Validator)
```

### DON'T Comment

- **Self-explanatory code** - if the code is clear, don't add noise
- **Restating code in English** - `// increment counter` above `counter++`
- **Describing what changed** - that belongs in commit messages, not code

### The Test

#### "Will this make sense in 6 months?"

Before adding a comment, ask: Would someone reading just the current code (no PR, no git history) find this helpful?

## Debugging Tips

1. **Logging**: Use `log` package with appropriate levels

   ```go
   log.Debug("Processing block", "number", block.Number(), "hash", block.Hash())
   ```

2. **Metrics**: Add prometheus metrics for monitoring

   ```go
   metrics.GetOrRegisterCounter("chain/inserts", nil).Inc(1)
   ```

3. **Profiling**: Use pprof for CPU/memory profiling

   ```bash
   go tool pprof http://localhost:6060/debug/pprof/profile
   ```

## Commit Style

Prefix with package name: `eth, rpc: make trace configs optional`

## CI Requirements

- All tests pass (`make test` + `make test-integration`)
- Linting passes (`make lint`)
- Code formatted (`make fmt`)

## Branch Strategy

- **develop** - Main development branch, PRs target here
- **master** - Stable release branch

## Maintaining This File

Update CLAUDE.md when:

- Claude makes a mistake or wrong assumption → Add clarifying context
- New patterns or conventions are established → Document them
- Frequently asked questions arise → Add answers here

This file should evolve over time to capture project-specific knowledge that helps AI agents work more effectively.
