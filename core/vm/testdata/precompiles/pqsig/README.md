# Post-quantum verify precompile vectors

`../mldsa65Verify.json` (0x0113) and `../slhdsaSha2128sVerify.json` (0x0115) use
the standard precompile test format. Input is `pk || sig || msg`; output is 32
bytes, 1 for a valid pure-mode signature with the empty context, else 0.

## Sources

| Source | Version | Used for |
|---|---|---|
| C2SP Wycheproof `testvectors_v1/mldsa_65_verify_test.json`, `mldsa_65_sign_{seed,noseed}_test.json` | `d17cc89fa5c8f4f4d378b5385ebf75da5d0926c9` | ML-DSA-65: every verify case (non-empty context → 0), valid empty-context sign cases |
| NIST ACVP-Server `gen-val/json-files` (ML-DSA vsId 42, SLH-DSA vsId 53) | `975de31eb83d87039ec88934fdc47d8c312b892d` | The only external/pure cases with an empty context: ML-DSA sigGen tc44, tc216, sigVer tc39; SLH-DSA sigGen tc162, tc474, sigVer tc255 |
| Apple Secure Enclave (CryptoKit `SecureEnclave.MLDSA65`), ephemeral keys | generated 2026-10-22 | ML-DSA-65 real-device signatures; 1 MiB case left out for size |
| Self-generated SLH-DSA-SHA2-128s (`slh_selfgen.sh`) | circl v1.6.5, OpenSSL 3.6.1 | 76 valid signatures, 0 B to 64 KiB messages, half signed by each library; every one verified by circl, OpenSSL (non-empty messages) and trailofbits/go-slh-dsa v0.1.0 |

Wycheproof has no SLH-DSA vectors, and ACVP has only three SHA2-128s cases in
this mode, which is why SLH-DSA relies on cross-verified self-made signatures
plus the bit-flip tests in `contracts_pq_test.go`.

## Regenerating

1. Download the Wycheproof files and extract the ACVP empty-context cases at the
   commits above.
2. `./slh_selfgen.sh <dir>` (needs OpenSSL >= 3.5 as `$OPENSSL`).
3. `python3 build_vectors.py <vectors-dir> <dir> <apple-se-vectors.json> ..`

Gas values in the JSON follow the provisional prices in
`params/protocol_params.go`; regenerate when they change.
