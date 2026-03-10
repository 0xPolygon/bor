---
paths:
  - "crypto/**/*.go"
  - "accounts/keystore/**/*.go"
  - "signer/**/*.go"
---
# Cryptographic Security — crypto/, accounts/keystore/, signer/

Cryptographic code protects private keys, validates signatures, and ensures data integrity. Bugs here directly enable fund theft or signature forgery.

## Critical Rules

1. **Never roll custom crypto** — use `crypto/ecdsa`, `golang.org/x/crypto`, or the vendored `secp256k1` library. No hand-written elliptic curve math.

2. **Constant-time operations for all secret comparisons** — use `crypto/subtle.ConstantTimeCompare`. This applies to: private keys, signatures, MACs, password hashes, JWT tokens.

3. **Zeroize secrets after use** — overwrite private key bytes in memory when no longer needed. Go's GC does not guarantee prompt cleanup.

```go
// After using a private key
defer func() {
    for i := range keyBytes {
        keyBytes[i] = 0
    }
}()
```

4. **Validate all public keys and signatures before use** — reject points at infinity, out-of-range scalars, malleable signatures (high-S values).

5. **Use crypto/rand for all randomness** — never `math/rand` for key generation, nonces, or any security-relevant random values.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| `math/rand` used for key/nonce generation | CRITICAL | Predictable randomness → key recovery |
| Private key logged or included in error message | CRITICAL | Key leak |
| `bytes.Equal` comparing secrets or signatures | HIGH | Timing side-channel |
| Missing signature malleability check (high-S) | HIGH | Transaction replay with modified sig |
| Keccak hash used as MAC without HMAC construction | MEDIUM | Length extension attacks |
| Public key not validated before ECDH | HIGH | Invalid curve attacks |
| Keystore file permissions > 0600 | MEDIUM | Other users can read keys |
| Hardcoded test keys in non-test files | CRITICAL | Accidental use in production |

## Signer Security

- Signing operations must go through `accounts.Wallet` or `signer/core` — never raw `ecdsa.Sign` in application code
- Audit logging (`signer/core/auditlog.go`) must capture all signing requests
- Rule-based signing (`signer/rules/`) must be evaluated before every sign operation
- Clef integration must validate the 4-byte method selector before signing transactions

## Review Checklist

- [ ] Is randomness sourced from `crypto/rand`?
- [ ] Are secrets zeroized after use?
- [ ] Are all comparisons of secret material constant-time?
- [ ] Are public keys validated before use?
- [ ] Are signature malleability checks in place (EIP-2)?
- [ ] Does the change affect key derivation or storage? If yes, verify backward compatibility with existing keystores.
- [ ] Are error messages free of secret material (keys, passwords, mnemonics)?
