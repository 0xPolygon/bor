# Bor Security Review — Common Rules

These rules apply to ALL code in the Bor repository. Bor is a blockchain execution client managing real user funds on Polygon PoS. Security bugs can cause chain halts, fund loss, or consensus splits.

## Severity Classification

| Severity | Impact | Examples |
|----------|--------|----------|
| CRITICAL | Fund loss, consensus split, chain halt | Validator bypass, double-spend, signature forgery |
| HIGH | Node crash, state corruption, DoS | Unbounded allocation, panic in consensus path, data race |
| MEDIUM | Information leak, degraded operation | RPC data leak, peer table poisoning, metric manipulation |
| LOW | Code quality risk | Missing error check, unused mutex, unclear invariant |

## Mandatory Checks Before Any Commit

- [ ] No panics in consensus, sync, or block production paths — return errors instead
- [ ] No unbounded allocations from untrusted input (peer messages, RPC params, block data)
- [ ] All cryptographic operations use constant-time comparison where applicable
- [ ] Shared mutable state protected by mutex or atomic operations — verify with `go test -race`
- [ ] No hardcoded private keys, mnemonics, or secrets — use env vars or keystore
- [ ] Error values checked — never discard errors with `_` in security-sensitive paths
- [ ] Context propagation for all operations that can block or involve external calls
- [ ] Integer overflow/underflow checked for arithmetic on untrusted values (gas, balances, block numbers)

## Go-Specific Security Patterns

### Input Bounds
```go
// WRONG — unbounded allocation from peer
data := make([]byte, msg.Size)

// RIGHT — enforce maximum before allocating
if msg.Size > maxMessageSize {
    return errOversizedMessage
}
data := make([]byte, msg.Size)
```

### Safe Comparison
```go
// WRONG — timing side-channel on secret comparison (private keys, MACs, JWT tokens)
if bytes.Equal(secret, expected) { ... }

// RIGHT — constant-time for secrets
if subtle.ConstantTimeCompare(secret, expected) == 1 { ... }

// OK — bytes.Equal is fine for public data like block hashes, addresses, state roots
if bytes.Equal(hash[:], other[:]) { ... }
```

### Error Handling
```go
// WRONG — silent discard
_ = db.Put(key, value)

// RIGHT — propagate or log
if err := db.Put(key, value); err != nil {
    return fmt.Errorf("failed to persist block: %w", err)
}
```

## Security Scanning

Run before submitting PRs touching security-sensitive code:

```bash
# Static security analysis
gosec ./...

# Race detector
go test -race ./...

# Vet for suspicious constructs
go vet ./...
```

## Security Response Protocol

If a CRITICAL vulnerability is found:
1. **STOP** — do not commit or push
2. **Document** the issue with reproduction steps
3. **Assess blast radius** — what chains/nodes are affected?
4. **Fix in private** — do not disclose in public PR descriptions
5. **Verify** the fix with targeted tests including the original attack vector
6. **Check for variants** — search the codebase for similar patterns
