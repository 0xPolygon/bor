package phase0

import (
	"context"
	"fmt"
	"sort"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type RegistryIndices []common.ValidatorIndex

func (p *RegistryIndices) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*p)
		*p = append(*p, common.ValidatorIndex(0))
		return &((*p)[i])
	}, common.ValidatorIndexType.TypeByteLength(), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (a RegistryIndices) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return a[i]
	}, common.ValidatorIndexType.TypeByteLength(), uint64(len(a)))
}

func (a RegistryIndices) ByteLength(spec *common.Spec) uint64 {
	return common.ValidatorIndexType.TypeByteLength() * uint64(len(a))
}

func (*RegistryIndices) FixedLength() uint64 {
	return 0
}

func (p RegistryIndices) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.Uint64ListHTR(func(i uint64) uint64 {
		return uint64(p[i])
	}, uint64(len(p)), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

type ValidatorRegistry []*Validator

func (a *ValidatorRegistry) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, &Validator{})
		return (*a)[i]
	}, ValidatorType.TypeByteLength(), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (a ValidatorRegistry) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return a[i]
	}, ValidatorType.TypeByteLength(), uint64(len(a)))
}

func (a ValidatorRegistry) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(a)) * ValidatorType.TypeByteLength()
}

func (a *ValidatorRegistry) FixedLength(spec *common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li ValidatorRegistry) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return li[i]
		}
		return nil
	}, length, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func ValidatorsRegistryType(spec *common.Spec) ListTypeDef {
	return ComplexListType(ValidatorType, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

type ValidatorsRegistryView struct{ *ComplexListView }

func AsValidatorsRegistry(v View, err error) (*ValidatorsRegistryView, error) {
	c, err := AsComplexList(v, err)
	return &ValidatorsRegistryView{c}, err
}

func (registry *ValidatorsRegistryView) ValidatorCount() (uint64, error) {
	return registry.Length()
}

func (registry *ValidatorsRegistryView) Validator(index common.ValidatorIndex) (common.Validator, error) {
	return AsValidator(registry.Get(uint64(index)))
}

func (registry *ValidatorsRegistryView) Iter() (next func() (val common.Validator, ok bool, err error)) {
	iter := registry.ReadonlyIter()
	return func() (val common.Validator, ok bool, err error) {
		elem, ok, err := iter.Next()
		if err != nil || !ok {
			return nil, ok, err
		}
		v, err := AsValidator(elem, nil)
		return v, true, err
	}
}

func (registry *ValidatorsRegistryView) IsValidIndex(index common.ValidatorIndex) (valid bool, err error) {
	count, err := registry.Length()
	if err != nil {
		return false, err
	}
	return uint64(index) < count, nil
}

type RegistryProcessData struct {
	IndicesToSetActivationEligibility []common.ValidatorIndex
	// Ignores churn. Apply churn-limit manually.
	// Maybe, because finality affects it still.
	IndicesToMaybeActivate []common.ValidatorIndex

	IndicesToEject []common.ValidatorIndex

	ExitQueueEnd      common.Epoch
	ExitQueueEndChurn uint64
	ChurnLimit        uint64
}

func ComputeRegistryProcessData(spec *common.Spec, flats []common.FlatValidator, currentEpoch common.Epoch) (*RegistryProcessData, error) {
	var out RegistryProcessData

	count := common.ValidatorIndex(len(flats))

	// Thanks to exit delay, this does not change within the epoch processing.
	activeCount := uint64(0)

	for i := common.ValidatorIndex(0); i < count; i++ {
		flat := &flats[i]
		active := flat.IsActive(currentEpoch)
		if active {
			activeCount++
		}
		if flat.ActivationEligibilityEpoch == common.FAR_FUTURE_EPOCH && flat.EffectiveBalance == spec.MAX_EFFECTIVE_BALANCE {
			out.IndicesToSetActivationEligibility = append(out.IndicesToSetActivationEligibility, i)
		}

		if flat.ActivationEpoch == common.FAR_FUTURE_EPOCH && flat.ActivationEligibilityEpoch <= currentEpoch {
			out.IndicesToMaybeActivate = append(out.IndicesToMaybeActivate, i)
		}

		if active && flat.EffectiveBalance <= spec.EJECTION_BALANCE && flat.ExitEpoch == common.FAR_FUTURE_EPOCH {
			out.IndicesToEject = append(out.IndicesToEject, i)
		}
	}

	// Order by the sequence of activation_eligibility_epoch setting and then index
	sort.Slice(out.IndicesToMaybeActivate, func(i int, j int) bool {
		valIndexA := out.IndicesToMaybeActivate[i]
		valIndexB := out.IndicesToMaybeActivate[j]
		a := flats[valIndexA].ActivationEligibilityEpoch
		b := flats[valIndexB].ActivationEligibilityEpoch
		if a == b { // Order by the sequence of activation_eligibility_epoch setting and then index
			return valIndexA < valIndexB
		}
		return a < b
	})

	exitQueueEnd := spec.ComputeActivationExitEpoch(currentEpoch)
	exitQueueEndChurn := uint64(0)
	for i := common.ValidatorIndex(0); i < count; i++ {
		exit := flats[i].ExitEpoch
		if exit == common.FAR_FUTURE_EPOCH {
			continue
		}
		if exit > exitQueueEnd {
			exitQueueEnd = exit
		}
		if exit == exitQueueEnd {
			exitQueueEndChurn++
		}
	}
	churnLimit := spec.GetChurnLimit(activeCount)
	if exitQueueEndChurn >= churnLimit {
		if exitQueueEnd == ^common.Epoch(0) { // practically impossible, but here for spec test introduced in consensus-specs#2887
			return nil, fmt.Errorf("exitQueueEnd overflowing: %d", exitQueueEnd)
		}
		exitQueueEnd++
		exitQueueEndChurn = 0
	}
	out.ExitQueueEndChurn = exitQueueEndChurn
	out.ExitQueueEnd = exitQueueEnd
	out.ChurnLimit = churnLimit

	return &out, nil
}

func ProcessEpochRegistryUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	vals, err := state.Validators()
	if err != nil {
		return err
	}

	registerData, err := ComputeRegistryProcessData(spec, flats, epc.CurrentEpoch.Epoch)
	if err != nil {
		return fmt.Errorf("invalid ProcessEpochRegistryUpdates: %v", err)
	}

	// process ejections
	{
		exitEnd := registerData.ExitQueueEnd
		endChurn := registerData.ExitQueueEndChurn
		for _, index := range registerData.IndicesToEject {
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetExitEpoch(exitEnd); err != nil {
				return err
			}
			withdrawEpoch := exitEnd + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY
			if withdrawEpoch < exitEnd { // practically impossible, but here for spec test introduced in consensus-specs#2887
				return fmt.Errorf("exit epoch overflow: %d + %d = %d", exitEnd, spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY, withdrawEpoch)
			}
			if err := val.SetWithdrawableEpoch(withdrawEpoch); err != nil {
				return err
			}
			endChurn += 1
			if endChurn >= registerData.ChurnLimit {
				endChurn = 0
				exitEnd += 1
			}
		}
	}

	// Process activation eligibility
	{
		eligibilityEpoch := epc.CurrentEpoch.Epoch + 1
		for _, index := range registerData.IndicesToSetActivationEligibility {
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetActivationEligibilityEpoch(eligibilityEpoch); err != nil {
				return err
			}
		}
	}

	// Process activations
	{
		finality, err := state.FinalizedCheckpoint()
		if err != nil {
			return err
		}
		dequeued := registerData.IndicesToMaybeActivate
		if uint64(len(dequeued)) > registerData.ChurnLimit {
			dequeued = dequeued[:registerData.ChurnLimit]
		}
		activationEpoch := spec.ComputeActivationExitEpoch(epc.CurrentEpoch.Epoch)
		for _, index := range dequeued {
			if flats[index].ActivationEligibilityEpoch > finality.Epoch {
				// remaining validators all have an activation_eligibility_epoch that is higher anyway, break early
				// The tie-breaks were already sorted correctly in the IndicesToMaybeActivate queue.
				break
			}
			val, err := vals.Validator(index)
			if err != nil {
				return err
			}
			if err := val.SetActivationEpoch(activationEpoch); err != nil {
				return err
			}
		}
	}
	return nil
}
