# Decision ledger: go-ethereum v1.17.4 sync

Append-only. One line per non-trivial resolution: file — class — upstream PR — decision — why.

## v1.16.9

- `crypto/ecies/ecies.go` — class 3 — #33669 — took upstream's `IsOnCurve` validation in `GenerateShared` — genuine invalid-curve guard absent from Bor; expect the duplicate commit (`c974722dc`, v1.17.0 batch 10) to now merge clean.
- `crypto/secp256k1/curve.go` — class 3 — upstream `895a8597c` — kept Bor's text — Bor pre-applied the identical coordinate check via `d9fac7a4c`; only delta was Bor's comment.
- `crypto/secp256k1/ext.h` — class 3 — upstream `895a8597c` — took upstream's combined `if (!a || !b)` form over Bor's equivalent two-if variant — zero behavior delta; converges text with upstream to de-conflict future syncs.
