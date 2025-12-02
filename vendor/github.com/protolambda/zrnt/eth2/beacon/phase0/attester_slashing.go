package phase0

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

func ProcessAttesterSlashings(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []AttesterSlashing) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessAttesterSlashing(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

type AttesterSlashing struct {
	Attestation1 IndexedAttestation `json:"attestation_1" yaml:"attestation_1"`
	Attestation2 IndexedAttestation `json:"attestation_2" yaml:"attestation_2"`
}

func (a *AttesterSlashing) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func (a *AttesterSlashing) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *AttesterSlashing) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.Attestation1), spec.Wrap(&a.Attestation2))
}

func BlockAttesterSlashingsType(spec *common.Spec) ListTypeDef {
	return ListType(AttesterSlashingType(spec), uint64(spec.MAX_ATTESTER_SLASHINGS))
}

func AttesterSlashingType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("AttesterSlashing", []FieldDef{
		{"attestation_1", IndexedAttestationType(spec)},
		{"attestation_2", IndexedAttestationType(spec)},
	})
}

type AttesterSlashings []AttesterSlashing

func (a *AttesterSlashings) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, AttesterSlashing{})
		return spec.Wrap(&((*a)[i]))
	}, 0, uint64(spec.MAX_ATTESTER_SLASHINGS))
}

func (a AttesterSlashings) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(&a[i])
	}, 0, uint64(len(a)))
}

func (a AttesterSlashings) ByteLength(spec *common.Spec) (out uint64) {
	for _, v := range a {
		out += v.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (a *AttesterSlashings) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li AttesterSlashings) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(&li[i])
		}
		return nil
	}, length, uint64(spec.MAX_ATTESTER_SLASHINGS))
}

func (li AttesterSlashings) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]AttesterSlashing{}) // encode as empty list, not null
	}
	return json.Marshal([]AttesterSlashing(li))
}

func ProcessAttesterSlashing(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, attesterSlashing *AttesterSlashing) error {
	sa1 := &attesterSlashing.Attestation1
	sa2 := &attesterSlashing.Attestation2

	if !IsSlashableAttestationData(&sa1.Data, &sa2.Data) {
		return errors.New("attester slashing has no valid reasoning")
	}

	if err := ValidateIndexedAttestation(spec, epc, state, sa1); err != nil {
		return errors.New("attestation 1 of attester slashing cannot be verified")
	}
	if err := ValidateIndexedAttestation(spec, epc, state, sa2); err != nil {
		return errors.New("attestation 2 of attester slashing cannot be verified")
	}

	currentEpoch := epc.CurrentEpoch.Epoch

	// keep track of effectiveness
	slashedAny := false
	var errorAny error

	validators, err := state.Validators()
	if err != nil {
		return err
	}
	// run slashings where applicable
	// use ZigZagJoin for efficient intersection: the indicies are already sorted (as validated above)
	common.ValidatorSet(sa1.AttestingIndices).ZigZagJoin(common.ValidatorSet(sa2.AttestingIndices), func(i common.ValidatorIndex) {
		if errorAny != nil {
			return
		}
		validator, err := validators.Validator(i)
		if err != nil {
			errorAny = err
			return
		}
		if slashable, err := IsSlashable(validator, currentEpoch); err != nil {
			errorAny = err
		} else if slashable {
			if err := SlashValidator(spec, epc, state, i, nil); err != nil {
				errorAny = err
			} else {
				slashedAny = true
			}
		}
	}, nil)
	if errorAny != nil {
		return fmt.Errorf("error during attester-slashing validators slashable check: %v", errorAny)
	}
	if !slashedAny {
		return errors.New("attester slashing %d is not effective, hence invalid")
	}
	return nil
}

func IsSlashableAttestationData(a *AttestationData, b *AttestationData) bool {
	return IsSurroundVote(a, b) || IsDoubleVote(a, b)
}

// Check if a and b have the same target epoch.
func IsDoubleVote(a *AttestationData, b *AttestationData) bool {
	return *a != *b && a.Target.Epoch == b.Target.Epoch
}

// Check if a surrounds b, i.E. source(a) < source(b) and target(a) > target(b)
func IsSurroundVote(a *AttestationData, b *AttestationData) bool {
	return a.Source.Epoch < b.Source.Epoch && a.Target.Epoch > b.Target.Epoch
}
