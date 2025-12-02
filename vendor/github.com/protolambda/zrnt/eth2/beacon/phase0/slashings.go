package phase0

import (
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

// SlashingsHistory is a EPOCHS_PER_SLASHINGS_VECTOR vector
type SlashingsHistory []common.Gwei

func (a *SlashingsHistory) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	if common.Epoch(len(*a)) != spec.EPOCHS_PER_SLASHINGS_VECTOR {
		// re-use space if available (for recycling old state objects)
		if common.Epoch(cap(*a)) >= spec.EPOCHS_PER_SLASHINGS_VECTOR {
			*a = (*a)[:spec.EPOCHS_PER_SLASHINGS_VECTOR]
		} else {
			*a = make([]common.Gwei, spec.EPOCHS_PER_SLASHINGS_VECTOR, spec.EPOCHS_PER_SLASHINGS_VECTOR)
		}
	}
	return dr.Vector(func(i uint64) codec.Deserializable {
		return &(*a)[i]
	}, common.GweiType.TypeByteLength(), uint64(spec.EPOCHS_PER_SLASHINGS_VECTOR))
}

func (a SlashingsHistory) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Vector(func(i uint64) codec.Serializable {
		return &a[i]
	}, common.GweiType.TypeByteLength(), uint64(len(a)))
}

func (a SlashingsHistory) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(a)) * common.GweiType.TypeByteLength()
}

func (a *SlashingsHistory) FixedLength(spec *common.Spec) uint64 {
	return uint64(spec.EPOCHS_PER_SLASHINGS_VECTOR) * common.GweiType.TypeByteLength()
}

func (li SlashingsHistory) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.Uint64VectorHTR(func(i uint64) uint64 {
		return uint64(li[i])
	}, uint64(len(li)))
}

// Balances slashed at every withdrawal period
func SlashingsType(spec *common.Spec) VectorTypeDef {
	return VectorType(common.GweiType, uint64(spec.EPOCHS_PER_SLASHINGS_VECTOR))
}

type SlashingsView struct{ *BasicVectorView }

var _ common.Slashings = (*SlashingsView)(nil)

func AsSlashings(v View, err error) (*SlashingsView, error) {
	c, err := AsBasicVector(v, err)
	return &SlashingsView{c}, err
}

func (sl *SlashingsView) GetSlashingsValue(epoch common.Epoch) (common.Gwei, error) {
	i := uint64(epoch) % sl.VectorLength
	return common.AsGwei(sl.Get(i))
}

func (sl *SlashingsView) ResetSlashings(epoch common.Epoch) error {
	i := uint64(epoch) % sl.VectorLength
	return sl.Set(i, Uint64View(0))
}

func (sl *SlashingsView) AddSlashing(epoch common.Epoch, add common.Gwei) error {
	prev, err := sl.GetSlashingsValue(epoch)
	if err != nil {
		return err
	}
	i := uint64(epoch) % sl.VectorLength
	return sl.Set(i, Uint64View(prev+add))
}

func (sl *SlashingsView) Total() (sum common.Gwei, err error) {
	iter := sl.ReadonlyIter()
	for {
		el, ok, err := iter.Next()
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		value, err := common.AsGwei(el, nil)
		if err != nil {
			return 0, err
		}
		sum += value
	}
	return
}

// Slash the validator with the given index.
func SlashValidator(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState,
	slashedIndex common.ValidatorIndex, whistleblowerIndex *common.ValidatorIndex) error {

	currentEpoch := epc.CurrentEpoch.Epoch
	if err := InitiateValidatorExit(spec, epc, state, slashedIndex); err != nil {
		return err
	}
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	v, err := vals.Validator(slashedIndex)
	if err != nil {
		return err
	}
	if err := v.MakeSlashed(); err != nil {
		return err
	}
	prevWithdrawalEpoch, err := v.WithdrawableEpoch()
	if err != nil {
		return err
	}
	withdrawalEpoch := currentEpoch + spec.EPOCHS_PER_SLASHINGS_VECTOR
	if withdrawalEpoch > prevWithdrawalEpoch {
		if err := v.SetWithdrawableEpoch(withdrawalEpoch); err != nil {
			return err
		}
	}

	effectiveBalance, err := v.EffectiveBalance()
	if err != nil {
		return err
	}

	slashings, err := state.Slashings()
	if err != nil {
		return err
	}
	if err := slashings.AddSlashing(currentEpoch, effectiveBalance); err != nil {
		return err
	}

	settings := state.ForkSettings(spec)
	bals, err := state.Balances()
	if err != nil {
		return err
	}
	if err := common.DecreaseBalance(bals, slashedIndex, effectiveBalance/common.Gwei(settings.MinSlashingPenaltyQuotient)); err != nil {
		return err
	}

	slot, err := state.Slot()
	if err != nil {
		return err
	}
	propIndex, err := epc.GetBeaconProposer(slot)
	if err != nil {
		return err
	}
	if whistleblowerIndex == nil {
		whistleblowerIndex = &propIndex
	}
	whistleblowerReward := effectiveBalance / common.Gwei(spec.WHISTLEBLOWER_REWARD_QUOTIENT)
	proposerReward := settings.CalcProposerShare(whistleblowerReward)
	if err := common.IncreaseBalance(bals, propIndex, proposerReward); err != nil {
		return err
	}
	if err := common.IncreaseBalance(bals, *whistleblowerIndex, whistleblowerReward-proposerReward); err != nil {
		return err
	}
	return nil
}

func ProcessEpochSlashings(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	totalActiveStake := common.Gwei(0)
	for _, v := range epc.CurrentEpoch.ActiveIndices {
		totalActiveStake += flats[v].EffectiveBalance
	}
	if totalActiveStake < spec.EFFECTIVE_BALANCE_INCREMENT {
		totalActiveStake = spec.EFFECTIVE_BALANCE_INCREMENT
	}

	settings := state.ForkSettings(spec)

	slashings, err := state.Slashings()
	if err != nil {
		return err
	}

	slashingsSum, err := slashings.Total()
	if err != nil {
		return err
	}
	slashingsWeight := slashingsSum * common.Gwei(settings.ProportionalSlashingMultiplier)
	var adjustedTotalSlashingBalance common.Gwei
	if totalActiveStake < slashingsWeight {
		adjustedTotalSlashingBalance = totalActiveStake
	} else {
		adjustedTotalSlashingBalance = slashingsWeight
	}

	bals, err := state.Balances()
	if err != nil {
		return err
	}

	slashingsEpoch := epc.CurrentEpoch.Epoch + (spec.EPOCHS_PER_SLASHINGS_VECTOR / 2)
	for i := 0; i < len(flats); i++ {
		flat := &flats[i]
		if flat.Slashed && slashingsEpoch == flat.WithdrawableEpoch {
			// Factored out from penalty numerator to avoid uint64 overflow
			slashedEffectiveBal := flat.EffectiveBalance
			penaltyNumerator := slashedEffectiveBal / spec.EFFECTIVE_BALANCE_INCREMENT
			penaltyNumerator *= adjustedTotalSlashingBalance
			penalty := penaltyNumerator / totalActiveStake * spec.EFFECTIVE_BALANCE_INCREMENT

			if err := common.DecreaseBalance(bals, common.ValidatorIndex(i), penalty); err != nil {
				return err
			}
		}
	}
	return nil
}
