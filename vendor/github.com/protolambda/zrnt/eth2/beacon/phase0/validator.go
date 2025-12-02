package phase0

import (
	"errors"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type Validator struct {
	Pubkey                common.BLSPubkey `json:"pubkey" yaml:"pubkey"`
	WithdrawalCredentials common.Root      `json:"withdrawal_credentials" yaml:"withdrawal_credentials"`
	EffectiveBalance      common.Gwei      `json:"effective_balance" yaml:"effective_balance"`
	Slashed               bool             `json:"slashed" yaml:"slashed"`

	ActivationEligibilityEpoch common.Epoch `json:"activation_eligibility_epoch" yaml:"activation_eligibility_epoch"`
	ActivationEpoch            common.Epoch `json:"activation_epoch" yaml:"activation_epoch"`
	ExitEpoch                  common.Epoch `json:"exit_epoch" yaml:"exit_epoch"`
	WithdrawableEpoch          common.Epoch `json:"withdrawable_epoch" yaml:"withdrawable_epoch"`
}

func (v *Validator) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&v.Pubkey, &v.WithdrawalCredentials, &v.EffectiveBalance, (*BoolView)(&v.Slashed),
		&v.ActivationEligibilityEpoch, &v.ActivationEpoch, &v.ExitEpoch, &v.WithdrawableEpoch)
}

func (v *Validator) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&v.Pubkey, &v.WithdrawalCredentials, &v.EffectiveBalance, (*BoolView)(&v.Slashed),
		&v.ActivationEligibilityEpoch, &v.ActivationEpoch, &v.ExitEpoch, &v.WithdrawableEpoch)
}

func (a *Validator) ByteLength() uint64 {
	return ValidatorType.TypeByteLength()
}

func (*Validator) FixedLength() uint64 {
	return ValidatorType.TypeByteLength()
}

func (v *Validator) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(v.Pubkey, v.WithdrawalCredentials, v.EffectiveBalance, (BoolView)(v.Slashed),
		v.ActivationEligibilityEpoch, v.ActivationEpoch, v.ExitEpoch, v.WithdrawableEpoch)
}

func (v *Validator) View() *ValidatorView {
	wCred := RootView(v.WithdrawalCredentials)
	c, _ := ValidatorType.FromFields(
		common.ViewPubkey(&v.Pubkey),
		&wCred,
		Uint64View(v.EffectiveBalance),
		BoolView(v.Slashed),
		Uint64View(v.ActivationEligibilityEpoch),
		Uint64View(v.ActivationEpoch),
		Uint64View(v.ExitEpoch),
		Uint64View(v.WithdrawableEpoch),
	)
	return &ValidatorView{c}
}

var ValidatorType = ContainerType("Validator", []FieldDef{
	{"pubkey", common.BLSPubkeyType},
	{"withdrawal_credentials", common.Bytes32Type}, // Commitment to pubkey for withdrawals
	{"effective_balance", common.GweiType},         // Balance at stake
	{"slashed", BoolType},
	// Status epochs
	{"activation_eligibility_epoch", common.EpochType}, // When criteria for activation were met
	{"activation_epoch", common.EpochType},
	{"exit_epoch", common.EpochType},
	{"withdrawable_epoch", common.EpochType}, // When validator can withdraw funds
})

const (
	_validatorPubkey = iota
	_validatorWithdrawalCredentials
	_validatorEffectiveBalance
	_validatorSlashed

	_validatorActivationEligibilityEpoch
	_validatorActivationEpoch
	_validatorExitEpoch
	_validatorWithdrawableEpoch
)

type ValidatorView struct {
	*ContainerView
}

var _ common.Validator = (*ValidatorView)(nil)

func NewValidatorView() *ValidatorView {
	return &ValidatorView{ContainerView: ValidatorType.New()}
}

func AsValidator(v View, err error) (*ValidatorView, error) {
	c, err := AsContainer(v, err)
	return &ValidatorView{c}, err
}

func (v *ValidatorView) Pubkey() (common.BLSPubkey, error) {
	return common.AsBLSPubkey(v.Get(_validatorPubkey))
}
func (v *ValidatorView) WithdrawalCredentials() (out common.Root, err error) {
	return AsRoot(v.Get(_validatorWithdrawalCredentials))
}
func (v *ValidatorView) SetWithdrawalCredentials(b common.Root) (err error) {
	wCred := RootView(b)
	return v.Set(_validatorWithdrawalCredentials, &wCred)
}
func (v *ValidatorView) EffectiveBalance() (common.Gwei, error) {
	return common.AsGwei(v.Get(_validatorEffectiveBalance))
}
func (v *ValidatorView) SetEffectiveBalance(b common.Gwei) error {
	return v.Set(_validatorEffectiveBalance, Uint64View(b))
}

func (v *ValidatorView) Slashed() (bool, error) {
	b, err := AsBool(v.Get(_validatorSlashed))
	return bool(b), err
}

func (v *ValidatorView) MakeSlashed() error {
	return v.Set(_validatorSlashed, BoolView(true))
}
func (v *ValidatorView) ActivationEligibilityEpoch() (common.Epoch, error) {
	return common.AsEpoch(v.Get(_validatorActivationEligibilityEpoch))
}
func (v *ValidatorView) SetActivationEligibilityEpoch(epoch common.Epoch) error {
	return v.Set(_validatorActivationEligibilityEpoch, Uint64View(epoch))
}
func (v *ValidatorView) ActivationEpoch() (common.Epoch, error) {
	return common.AsEpoch(v.Get(_validatorActivationEpoch))
}
func (v *ValidatorView) SetActivationEpoch(epoch common.Epoch) error {
	return v.Set(_validatorActivationEpoch, Uint64View(epoch))
}
func (v *ValidatorView) ExitEpoch() (common.Epoch, error) {
	return common.AsEpoch(v.Get(_validatorExitEpoch))
}
func (v *ValidatorView) SetExitEpoch(ep common.Epoch) error {
	return v.Set(_validatorExitEpoch, Uint64View(ep))
}
func (v *ValidatorView) WithdrawableEpoch() (common.Epoch, error) {
	return common.AsEpoch(v.Get(_validatorWithdrawableEpoch))
}
func (v *ValidatorView) SetWithdrawableEpoch(epoch common.Epoch) error {
	return v.Set(_validatorWithdrawableEpoch, Uint64View(epoch))
}

func (v *ValidatorView) Flatten(dst *common.FlatValidator) error {
	if dst == nil {
		return errors.New("nil FlatValidator dst")
	}
	fields, err := v.FieldValues()
	dst.EffectiveBalance, err = common.AsGwei(fields[2], err)
	slashed, err := AsBool(fields[3], err)
	dst.Slashed = bool(slashed)
	dst.ActivationEligibilityEpoch, err = common.AsEpoch(fields[4], err)
	dst.ActivationEpoch, err = common.AsEpoch(fields[5], err)
	dst.ExitEpoch, err = common.AsEpoch(fields[6], err)
	dst.WithdrawableEpoch, err = common.AsEpoch(fields[7], err)
	if err != nil {
		return errors.New("failed to flatten validator")
	}
	return nil
}

func IsActive(v common.Validator, epoch common.Epoch) (bool, error) {
	activationEpoch, err := v.ActivationEpoch()
	if err != nil {
		return false, err
	} else if activationEpoch > epoch {
		return false, nil
	}
	exitEpoch, err := v.ExitEpoch()
	if err != nil {
		return false, err
	} else if epoch >= exitEpoch {
		return false, nil
	}
	return true, nil
}

func IsSlashable(v common.Validator, epoch common.Epoch) (bool, error) {
	slashed, err := v.Slashed()
	if err != nil {
		return false, err
	} else if slashed {
		return false, nil
	}
	activationEpoch, err := v.ActivationEpoch()
	if err != nil {
		return false, err
	} else if activationEpoch > epoch {
		return false, nil
	}
	withdrawableEpoch, err := v.WithdrawableEpoch()
	if err != nil {
		return false, err
	} else if withdrawableEpoch <= epoch {
		return false, nil
	}
	return true, nil
}

func IsEligibleForActivationQueue(v common.Validator, spec *common.Spec) (bool, error) {
	actEligEpoch, err := v.ActivationEligibilityEpoch()
	if err != nil {
		return false, err
	}
	effBalance, err := v.EffectiveBalance()
	if err != nil {
		return false, err
	}
	return actEligEpoch == common.FAR_FUTURE_EPOCH && effBalance == spec.MAX_EFFECTIVE_BALANCE, nil
}

func IsEligibleForActivation(v common.Validator, finalizedEpoch common.Epoch) (bool, error) {
	actEligEpoch, err := v.ActivationEligibilityEpoch()
	if err != nil {
		return false, err
	}
	actEpoch, err := v.ActivationEpoch()
	if err != nil {
		return false, err
	}
	return actEligEpoch <= finalizedEpoch && actEpoch == common.FAR_FUTURE_EPOCH, nil
}
