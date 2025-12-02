# BLS 12-381 util

BLS 12-381 util (BLSU, "bless you") is a collection of utils to work with BLS 12-381 in Go.

*Warning: these wrapper utils have not been audited.*

This package wraps [`github.com/kilic/bls12-381`](https://github.com/kilic/bls12-381), 
a pure Go implementation of BLS, no CGO involved, no special dependencies.
Instead, this BLS implementation uses Go-assembly to optimize the lower level computations.
[audit info](https://github.com/kilic/bls12-381/issues/19).

This package implements the `BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_` ciphersuite.

TODO: **not safe for 32 bit usage**: kilic BLS Fr.FromBytes->Fr.fromBytes->Fr.fromBig assumes word size is 64 bits.

## Utils

- Eth2 Typing
  - Pubkeys: `PointG1` wrapper
  - Signatures: `PointG2` wrapper
  - Secret keys: `Fr` wrapper
  - Signatures sets: see below
- [Draft 4](https://datatracker.ietf.org/doc/html/draft-irtf-cfrg-bls-signature-04) for signatures
  - Hash to curve, from `kilic/bls12-381`: `BLS12381G2_XMD:SHA-256_SSWU_RO_`
  - Schemes:
    - Core operations:
      - `KeyGen` (TODO)
      - `SkToPk`
      - ~~`KeyValidate`~~, implemented as part of Pubkey deserialization,
        except identity-pubkey check (checked in verify functions instead).
      - `CoreSign`
      - `CoreVerify`
      - `Aggregate`
      - `CoreAggregateVerify`
    - ~~Basic scheme~~, not supported
    - ~~Message Augmentation scheme~~, not supported
    - `POP`, Proof of Possession scheme (used in Eth2):
      - ~~PopProve~~, not supported, assumed through application-specific implementation
      - ~~PopVerify~~, not supported, assumed through application-specific implementation
      - `FastAggregateVerify`
- Eth2 additions
  - [`eth2_aggregate_pubkeys`](https://github.com/ethereum/eth2.0-specs/blob/dev/specs/altair/bls.md#eth2_aggregate_pubkeys): `AggregatePubkeys`
  - [`eth2_fast_aggregate_verify`](https://github.com/ethereum/eth2.0-specs/blob/dev/specs/altair/bls.md#eth2_fast_aggregate_verify): `Eth2FastAggregateVerify`
- [Signature sets](https://ethresear.ch/t/fast-verification-of-multiple-bls-signatures/5407): verify non-singular set of signatures and its respective pubkeys and messages

## Testing

- Unit tests
  - [ ] `SecretKey` deserialization/serialization
  - [x] `Pubkey` deserialization/serialization (with KeyValidate routine, except identity-pubkey check)
  - [x] `Signature` deserialization/serialization
  - [x] `SkToPk` (TODO: expand)
  - [x] `SignatureSetVerify`
- Eth2 BLS tests
  - [x] `Sign`
  - [x] `Aggregate`
  - [x] `Verify`
  - [x] `AggregateVerify`
  - [x] `FastAggregateVerify`
  - [ ] `AggregatePubkeys`
  - [ ] `Eth2FastAggregateVerify`
- Eth2 spec tests
  - [x] Integrate into ZRNT, run full eth2 test-suite
- standard tests (if any)
  - [ ] TODO, need standard signature-scheme test vectors (Work in progress)
  - [x] Run Hash-to-curve test-vectors on `kilic/bls12-381` internals

## License

MIT, see [`LICENSE`](./LICENSE) file.
