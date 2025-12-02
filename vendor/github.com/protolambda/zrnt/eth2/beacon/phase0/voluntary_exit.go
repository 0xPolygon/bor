package phase0

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

func BlockVoluntaryExitsType(spec *common.Spec) ListTypeDef {
	return ListType(SignedVoluntaryExitType, uint64(spec.MAX_VOLUNTARY_EXITS))
}

type VoluntaryExits []SignedVoluntaryExit

func (a *VoluntaryExits) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, SignedVoluntaryExit{})
		return &(*a)[i]
	}, SignedVoluntaryExitType.TypeByteLength(), uint64(spec.MAX_VOLUNTARY_EXITS))
}

func (a VoluntaryExits) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, SignedVoluntaryExitType.TypeByteLength(), uint64(len(a)))
}

func (a VoluntaryExits) ByteLength(spec *common.Spec) (out uint64) {
	return SignedVoluntaryExitType.TypeByteLength() * uint64(len(a))
}

func (*VoluntaryExits) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li VoluntaryExits) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_VOLUNTARY_EXITS))
}

func (li VoluntaryExits) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]SignedVoluntaryExit{}) // encode as empty list, not null
	}
	return json.Marshal([]SignedVoluntaryExit(li))
}

func ProcessVoluntaryExits(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []SignedVoluntaryExit) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessVoluntaryExit(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

type VoluntaryExit struct {
	// Earliest epoch when voluntary exit can be processed
	Epoch          common.Epoch          `json:"epoch" yaml:"epoch"`
	ValidatorIndex common.ValidatorIndex `json:"validator_index" yaml:"validator_index"`
}

var VoluntaryExitType = ContainerType("VoluntaryExit", []FieldDef{
	{"epoch", common.EpochType},
	{"validator_index", common.ValidatorIndexType},
})

func (v *VoluntaryExit) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&v.Epoch, &v.ValidatorIndex)
}

func (v *VoluntaryExit) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&v.Epoch, &v.ValidatorIndex)
}

func (v *VoluntaryExit) ByteLength() uint64 {
	return VoluntaryExitType.TypeByteLength()
}

func (*VoluntaryExit) FixedLength() uint64 {
	return VoluntaryExitType.TypeByteLength()
}

func (v *VoluntaryExit) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(v.Epoch, v.ValidatorIndex)
}

type SignedVoluntaryExit struct {
	Message   VoluntaryExit       `json:"message" yaml:"message"`
	Signature common.BLSSignature `json:"signature" yaml:"signature"`
}

func (v *SignedVoluntaryExit) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&v.Message, &v.Signature)
}

func (v *SignedVoluntaryExit) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&v.Message, &v.Signature)
}

func (v *SignedVoluntaryExit) ByteLength() uint64 {
	return SignedVoluntaryExitType.TypeByteLength()
}

func (*SignedVoluntaryExit) FixedLength() uint64 {
	return SignedVoluntaryExitType.TypeByteLength()
}

func (v *SignedVoluntaryExit) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&v.Message, v.Signature)
}

var SignedVoluntaryExitType = ContainerType("SignedVoluntaryExit", []FieldDef{
	{"message", VoluntaryExitType},
	{"signature", common.BLSSignatureType},
})

func ValidateVoluntaryExit(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, signedExit *SignedVoluntaryExit) error {
	exit := &signedExit.Message
	currentEpoch := epc.CurrentEpoch.Epoch
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	if valid, err := vals.IsValidIndex(exit.ValidatorIndex); err != nil {
		return err
	} else if !valid {
		return errors.New("invalid exit validator index")
	}
	validator, err := vals.Validator(exit.ValidatorIndex)
	if err != nil {
		return err
	}
	// Verify that the validator is active
	if isActive, err := IsActive(validator, currentEpoch); err != nil {
		return err
	} else if !isActive {
		return errors.New("validator must be active to be able to voluntarily exit")
	}
	scheduledExitEpoch, err := validator.ExitEpoch()
	if err != nil {
		return err
	}
	// Verify exit has not been initiated
	if scheduledExitEpoch != common.FAR_FUTURE_EPOCH {
		return errors.New("validator already exited")
	}
	// Exits must specify an epoch when they become valid; they are not valid before then
	if currentEpoch < exit.Epoch {
		return errors.New("invalid exit epoch")
	}
	registeredActivationEpoch, err := validator.ActivationEpoch()
	if err != nil {
		return err
	}
	// Verify the validator has been active long enough
	if currentEpoch < registeredActivationEpoch+spec.SHARD_COMMITTEE_PERIOD {
		return errors.New("exit is too soon")
	}
	pubkey, ok := epc.ValidatorPubkeyCache.Pubkey(exit.ValidatorIndex)
	if !ok {
		return errors.New("could not find index of exiting validator")
	}
	domain, err := common.GetDomain(state, common.DOMAIN_VOLUNTARY_EXIT, exit.Epoch)
	if err != nil {
		return err
	}
	sigRoot := common.ComputeSigningRoot(signedExit.Message.HashTreeRoot(tree.GetHashFn()), domain)
	blsPub, err := pubkey.Pubkey()
	if err != nil {
		return fmt.Errorf("failed to deserialize cached pubkey: %v", err)
	}
	sig, err := signedExit.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check exit signature: %v", err)
	}
	// Verify signature
	if !blsu.Verify(blsPub, sigRoot[:], sig) {
		return errors.New("voluntary exit signature could not be verified")
	}
	return nil
}

func ProcessVoluntaryExit(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, signedExit *SignedVoluntaryExit) error {
	if err := ValidateVoluntaryExit(spec, epc, state, signedExit); err != nil {
		return err
	}
	return InitiateValidatorExit(spec, epc, state, signedExit.Message.ValidatorIndex)
}

// Initiate the exit of the validator of the given index
func InitiateValidatorExit(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, index common.ValidatorIndex) error {
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	v, err := validators.Validator(index)
	if err != nil {
		return err
	}
	exitEp, err := v.ExitEpoch()
	if err != nil {
		return err
	}
	// Return if validator already initiated exit
	if exitEp != common.FAR_FUTURE_EPOCH {
		return nil
	}
	currentEpoch := epc.CurrentEpoch.Epoch

	// Set validator exit epoch and withdrawable epoch
	valIterNext := validators.Iter()

	exitQueueEnd := spec.ComputeActivationExitEpoch(currentEpoch)
	exitQueueEndChurn := uint64(0)
	for {
		val, ok, err := valIterNext()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		valExit, err := val.ExitEpoch()
		if err != nil {
			return err
		}
		if valExit == common.FAR_FUTURE_EPOCH {
			continue
		}
		if valExit == exitQueueEnd {
			exitQueueEndChurn++
		} else if valExit > exitQueueEnd {
			exitQueueEnd = valExit
			exitQueueEndChurn = 1
		}
	}
	churnLimit := spec.GetChurnLimit(uint64(len(epc.CurrentEpoch.ActiveIndices)))
	if exitQueueEndChurn >= churnLimit {
		exitQueueEnd++
	}

	exitEp = exitQueueEnd
	if err := v.SetExitEpoch(exitEp); err != nil {
		return err
	}
	if err := v.SetWithdrawableEpoch(exitEp + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY); err != nil {
		return err
	}
	return nil
}
