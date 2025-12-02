package phase0

import (
	"errors"
	"fmt"
	"sort"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type IndexedAttestation struct {
	AttestingIndices common.CommitteeIndices `json:"attesting_indices" yaml:"attesting_indices"`
	Data             AttestationData         `json:"data" yaml:"data"`
	Signature        common.BLSSignature     `json:"signature" yaml:"signature"`
}

func (p *IndexedAttestation) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&p.AttestingIndices), &p.Data, &p.Signature)
}

func (a *IndexedAttestation) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.AttestingIndices), &a.Data, &a.Signature)
}

func (a *IndexedAttestation) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.AttestingIndices), &a.Data, &a.Signature)
}

func (*IndexedAttestation) FixedLength(*common.Spec) uint64 {
	return 0
}

func (p *IndexedAttestation) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.AttestingIndices), &p.Data, p.Signature)
}

func IndexedAttestationType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("IndexedAttestation", []FieldDef{
		{"attesting_indices", spec.CommitteeIndices()},
		{"data", AttestationDataType},
		{"signature", common.BLSSignatureType},
	})
}

func ValidateIndexedAttestationIndicesSet(spec *common.Spec, indexedAttestation *IndexedAttestation) (common.ValidatorSet, error) {
	// wrap it in validator-sets. Does not sort it, but does make checking if it is a lot easier.
	indices := common.ValidatorSet(indexedAttestation.AttestingIndices)

	// Verify max number of indices
	if count := uint64(len(indices)); count > uint64(spec.MAX_VALIDATORS_PER_COMMITTEE) {
		return nil, fmt.Errorf("invalid indices count in indexed attestation: %d", count)
	}

	// empty attestation
	if len(indices) <= 0 {
		return nil, errors.New("in phase 0 no empty attestation signatures are allowed")
	}

	// The indices must be sorted
	if !sort.IsSorted(indices) {
		return nil, errors.New("attestation indices are not sorted")
	}

	// Verify if the indices are unique. Simple O(n) check, since they are already sorted.
	for i := 1; i < len(indices); i++ {
		if indices[i-1] == indices[i] {
			return nil, fmt.Errorf("attestation indices at %d and %d are duplicate, both: %d", i-1, i, indices[i])
		}
	}
	return indices, nil
}

func ValidateIndexedAttestationNoSignature(spec *common.Spec, state common.BeaconState, indexedAttestation *IndexedAttestation) error {
	indices, err := ValidateIndexedAttestationIndicesSet(spec, indexedAttestation)
	if err != nil {
		return err
	}

	// Check the last item of the sorted list to be a valid index,
	// if this one is valid, the others are as well, since they are lower.
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	valid, err := vals.IsValidIndex(indices[len(indices)-1])
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("attestation indices contain out of range index")
	}
	return nil
}

func ValidateIndexedAttestationSignature(spec *common.Spec, dom common.BLSDomain, pubCache *common.PubkeyCache, indexedAttestation *IndexedAttestation) error {
	pubkeys := make([]*blsu.Pubkey, 0, len(indexedAttestation.AttestingIndices))
	for _, i := range indexedAttestation.AttestingIndices {
		pub, ok := pubCache.Pubkey(i)
		if !ok {
			return fmt.Errorf("could not find pubkey for index %d", i)
		}
		blsPub, err := pub.Pubkey()
		if err != nil {
			return fmt.Errorf("failed to deserialize pubkey in cache: %v", err)
		}
		pubkeys = append(pubkeys, blsPub)
	}
	// empty attestation. (Double check, since this function is public, the user might not have validated if it's empty or not)
	if len(pubkeys) <= 0 {
		return errors.New("in phase 0 no empty attestation signatures are allowed")
	}

	signingRoot := common.ComputeSigningRoot(indexedAttestation.Data.HashTreeRoot(tree.GetHashFn()), dom)
	sig, err := indexedAttestation.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check indexed attestation signature: %v", err)
	}
	if !blsu.Eth2FastAggregateVerify(pubkeys, signingRoot[:], sig) {
		return errors.New("could not verify BLS signature for indexed attestation")
	}
	return nil
}

// Verify validity of slashable_attestation fields.
func ValidateIndexedAttestation(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, indexedAttestation *IndexedAttestation) error {
	if err := ValidateIndexedAttestationNoSignature(spec, state, indexedAttestation); err != nil {
		return err
	}
	dom, err := common.GetDomain(state, common.DOMAIN_BEACON_ATTESTER, indexedAttestation.Data.Target.Epoch)
	if err != nil {
		return err
	}
	return ValidateIndexedAttestationSignature(spec, dom, epc.ValidatorPubkeyCache, indexedAttestation)
}
