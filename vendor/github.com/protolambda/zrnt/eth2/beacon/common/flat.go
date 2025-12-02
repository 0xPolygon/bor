package common

type FlatValidator struct {
	EffectiveBalance           Gwei
	Slashed                    bool
	ActivationEligibilityEpoch Epoch
	ActivationEpoch            Epoch
	ExitEpoch                  Epoch
	WithdrawableEpoch          Epoch
}

func (v *FlatValidator) IsActive(epoch Epoch) bool {
	return v.ActivationEpoch <= epoch && epoch < v.ExitEpoch
}

func FlattenValidators(vals ValidatorRegistry) ([]FlatValidator, error) {
	count, err := vals.ValidatorCount()
	if err != nil {
		return nil, err
	}
	out := make([]FlatValidator, count, count)
	next := vals.Iter()
	for i := uint64(0); i < count; i++ {
		v, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if err := v.Flatten(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
