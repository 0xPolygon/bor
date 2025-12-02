package common

import (
	"bytes"

	blsu "github.com/protolambda/bls12-381-util"
)

type BeaconBlockEnvelope struct {
	ForkDigest ForkDigest

	// Block header details
	BeaconBlockHeader

	// Fork-specific block body
	Body SpecObj

	// Cached block root (hash-tree-root of Message)
	BlockRoot Root

	// Block signature
	Signature BLSSignature
}

func (b *BeaconBlockEnvelope) VerifySignature(spec *Spec, genesisValidatorsRoot Root, proposer ValidatorIndex, pub *CachedPubkey) bool {
	version := spec.ForkVersion(b.Slot)
	return b.VerifySignatureVersioned(spec, version, genesisValidatorsRoot, proposer, pub)
}

func (b *BeaconBlockEnvelope) VerifySignatureVersioned(spec *Spec, version Version, genesisValidatorsRoot Root, proposer ValidatorIndex, cachedPub *CachedPubkey) bool {
	if b.ProposerIndex != proposer {
		return false
	}
	forkRoot := ComputeForkDataRoot(version, genesisValidatorsRoot)
	// Sanity check fork digest
	if !bytes.Equal(forkRoot[0:4], b.ForkDigest[:]) {
		return false
	}
	pub, err := cachedPub.Pubkey()
	if err != nil {
		return false
	}
	dom := ComputeDomain(DOMAIN_BEACON_PROPOSER, version, genesisValidatorsRoot)
	signingRoot := ComputeSigningRoot(b.BlockRoot, dom)
	sig, err := b.Signature.Signature()
	if err != nil {
		return false
	}
	return blsu.Verify(pub, signingRoot[:], sig)
}

type EnvelopeBuilder interface {
	Envelope(spec *Spec, digest ForkDigest) *BeaconBlockEnvelope
}
