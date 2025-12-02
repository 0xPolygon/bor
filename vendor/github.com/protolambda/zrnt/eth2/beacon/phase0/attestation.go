package phase0

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sort"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

func BlockAttestationsType(spec *common.Spec) ListTypeDef {
	return ListType(AttestationType(spec), uint64(spec.MAX_ATTESTATIONS))
}

func AttestationType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("Attestation", []FieldDef{
		{"aggregation_bits", AttestationBitsType(spec)},
		{"data", AttestationDataType},
		{"signature", common.BLSSignatureType},
	})
}

type Attestation struct {
	AggregationBits AttestationBits     `json:"aggregation_bits" yaml:"aggregation_bits"`
	Data            AttestationData     `json:"data" yaml:"data"`
	Signature       common.BLSSignature `json:"signature" yaml:"signature"`
}

func (a *Attestation) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature)
}

func (a *Attestation) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature)
}

func (a *Attestation) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.AggregationBits), &a.Data, &a.Signature)
}

func (a *Attestation) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *Attestation) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.AggregationBits), &a.Data, a.Signature)
}

type Attestations []Attestation

func (a *Attestations) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, Attestation{})
		return spec.Wrap(&((*a)[i]))
	}, 0, uint64(spec.MAX_ATTESTATIONS))
}

func (a Attestations) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return spec.Wrap(&a[i])
	}, 0, uint64(len(a)))
}

func (a Attestations) ByteLength(spec *common.Spec) (out uint64) {
	for _, v := range a {
		out += v.ByteLength(spec) + codec.OFFSET_SIZE
	}
	return
}

func (a *Attestations) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li Attestations) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return spec.Wrap(&li[i])
		}
		return nil
	}, length, uint64(spec.MAX_ATTESTATIONS))
}

func (li Attestations) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]Attestation{}) // encode as empty list, not null
	}
	return json.Marshal([]Attestation(li))
}

func ProcessAttestations(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state Phase0PendingAttestationsBeaconState, ops []Attestation) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessAttestation(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ProcessAttestation(spec *common.Spec, epc *common.EpochsContext, state Phase0PendingAttestationsBeaconState, attestation *Attestation) error {
	data := &attestation.Data

	// Check slot
	currentSlot, err := state.Slot()
	if err != nil {
		return err
	}

	currentEpoch := spec.SlotToEpoch(currentSlot)
	previousEpoch := currentEpoch.Previous()

	// Check target
	if data.Target.Epoch < previousEpoch {
		return errors.New("attestation data is invalid, target is too far in past")
	} else if data.Target.Epoch > currentEpoch {
		return errors.New("attestation data is invalid, target is in future")
	}
	// And if it matches the slot
	if data.Target.Epoch != spec.SlotToEpoch(data.Slot) {
		return errors.New("attestation data is invalid, slot epoch does not match target epoch")
	}

	if !(currentSlot <= data.Slot+spec.SLOTS_PER_EPOCH) {
		return errors.New("attestation slot is too old")
	}
	if !(data.Slot+spec.MIN_ATTESTATION_INCLUSION_DELAY <= currentSlot) {
		return errors.New("attestation is too new")
	}

	// Check committee index
	if commCount, err := epc.GetCommitteeCountPerSlot(data.Target.Epoch); err != nil {
		return err
	} else if uint64(data.Index) >= commCount {
		return errors.New("attestation data is invalid, committee index out of range")
	}

	// Check source
	if data.Target.Epoch == currentEpoch {
		currentJustified, err := state.CurrentJustifiedCheckpoint()
		if err != nil {
			return err
		}
		if data.Source != currentJustified {
			return errors.New("attestation source does not match current justified checkpoint")
		}
	} else {
		previousJustified, err := state.PreviousJustifiedCheckpoint()
		if err != nil {
			return err
		}
		if data.Source != previousJustified {
			return errors.New("attestation source does not match previous justified checkpoint")
		}
	}

	// Check signature and bitfields
	committee, err := epc.GetBeaconCommittee(data.Slot, data.Index)
	if err != nil {
		return err
	}
	if indexedAtt, err := attestation.ConvertToIndexed(spec, committee); err != nil {
		return fmt.Errorf("attestation could not be converted to an indexed attestation: %v", err)
	} else if err := ValidateIndexedAttestation(spec, epc, state, indexedAtt); err != nil {
		return fmt.Errorf("attestation could not be verified in its indexed form: %v", err)
	}

	proposerIndex, err := epc.GetBeaconProposer(currentSlot)
	if err != nil {
		return err
	}
	// Cache pending attestation
	pendingAttestationRaw := PendingAttestation{
		Data:            *data,
		AggregationBits: attestation.AggregationBits,
		InclusionDelay:  currentSlot - data.Slot,
		ProposerIndex:   proposerIndex,
	}
	pendingAttestation := pendingAttestationRaw.View(spec)

	if data.Target.Epoch == currentEpoch {
		atts, err := state.CurrentEpochAttestations()
		if err != nil {
			return err
		}
		if err := atts.Append(pendingAttestation); err != nil {
			return err
		}
	} else {
		atts, err := state.PreviousEpochAttestations()
		if err != nil {
			return err
		}
		if err := atts.Append(pendingAttestation); err != nil {
			return err
		}
	}
	return nil
}

// Convert attestation to (almost) indexed-verifiable form
func (attestation *Attestation) ConvertToIndexed(spec *common.Spec, committee []common.ValidatorIndex) (*IndexedAttestation, error) {
	bitLen := attestation.AggregationBits.BitLen()
	if uint64(len(committee)) != bitLen {
		return nil, fmt.Errorf("committee size does not match bits size: %d <> %d", len(committee), bitLen)
	}

	participants := make([]common.ValidatorIndex, 0, len(committee))
	for i := uint64(0); i < bitLen; i++ {
		if attestation.AggregationBits.GetBit(i) {
			participants = append(participants, committee[i])
		}
	}
	sort.Slice(participants, func(i int, j int) bool {
		return participants[i] < participants[j]
	})

	return &IndexedAttestation{
		AttestingIndices: participants,
		Data:             attestation.Data,
		Signature:        attestation.Signature,
	}, nil
}

func ComputeSubnetForAttestation(spec *common.Spec, committeesPerSlot uint64, slot common.Slot, committeeIndex common.CommitteeIndex) (uint64, error) {
	maxCommitteeIndex := common.CommitteeIndex(committeesPerSlot * uint64(spec.SLOTS_PER_EPOCH))
	if committeeIndex >= maxCommitteeIndex {
		return 0, fmt.Errorf("committee index %d >= maximum %d", committeeIndex, maxCommitteeIndex)
	}
	slotsSinceEpochStart := uint64(slot % spec.SLOTS_PER_EPOCH)
	committeesSinceEpochStart := committeesPerSlot * slotsSinceEpochStart

	return (committeesSinceEpochStart + uint64(committeeIndex)) % common.ATTESTATION_SUBNET_COUNT, nil
}
