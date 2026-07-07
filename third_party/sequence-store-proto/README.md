# sequence-store-proto

Public contract for the Polygon PoS sequence store: the gRPC API and the
commitment-chain definition shared by the block producer (Bor), the store,
and every stream consumer.

This module deliberately contains no production implementation — only what
all parties must agree on, plus `devstore`, an in-memory reference used by
tests and devnets:

| Path                | What it is                                                                 |
| ------------------- | -------------------------------------------------------------------------- |
| `sequencestore/v1/` | Protobuf schema (`sequencestore.proto`) and committed generated Go bindings |
| `commitment/`       | The normative commitment fold: genesis seed, domain tags, canonical encodings, `C_open`/`C_block`/`C_seal` |
| `devstore/`         | In-memory reference store for tests and devnets: real head check, folds, resume, and block reads — no persistence, auth, replication, or election verification |
| `cmd/devstore/`     | Standalone plaintext-gRPC devstore binary (`--addr`, `--chain-id`) |

## The contract in one paragraph

The store holds one append-only chain of entries mirroring a block's
lifecycle: `BlockOpen` (header context, fixed at open), `Record` (ordered raw
signed transactions), `BlockSeal` (the full sealed header). Every entry
carries a `prefix_commitment` — the commitment-chain head it extends — and
the store appends an entry only if that prefix equals its current head.
The chain is advanced by a per-item fold `head' = keccak256(head ‖ tag ‖ item)`
over canonical byte encodings defined in `commitment/` — never over
serialized protobuf, which is not canonical across implementations.
Entries are delivered to consumers exactly as published; pre-seal content is
attributed by trusting the store's authenticated ingress.

## Regenerating bindings

Codegen tools are pinned outside the root module so they never touch its
dependency surface: buf, gofumpt, and govulncheck in `tools/go.mod`, and the
generator plugins (protoc-gen-go, protoc-gen-go-grpc) in `tools/gen/go.mod` —
separate from buf so the generators stay pinned at exactly the protobuf
version the root module ships:

```bash
make generate   # buf generate via the tools modules
make lint       # buf lint + go vet + gofumpt on hand-written code
make test       # go test -race ./...
make vuln       # govulncheck ./...
make breaking   # buf breaking vs the default branch — run before tagging
```

Generated code is committed; consumers only ever need `go get`.

## Versioning

- Changes within `sequencestore.v1` must be additive (buf breaking-checked).
- Any change to the wire format or to the canonical fold encodings bumps
  `commitment.FormatVersion`, which changes the genesis seed — streams from
  different format revisions fail structurally at the first head check.
- The pinned test vectors in `commitment/commitment_test.go` are the
  cross-client known-answer reference; a vector update is a format change.

## Dependency policy

The root module stays minimal (`grpc`, `protobuf`, `x/crypto`) and pins at or
below the versions Bor already carries, so importing this module never forces
a dependency bump on consumers.
