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

type ProposerSlashing struct {
	SignedHeader1 common.SignedBeaconBlockHeader `json:"signed_header_1" yaml:"signed_header_1"`
	SignedHeader2 common.SignedBeaconBlockHeader `json:"signed_header_2" yaml:"signed_header_2"`
}

func (a *ProposerSlashing) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&a.SignedHeader1, &a.SignedHeader2)
}

func (a *ProposerSlashing) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&a.SignedHeader1, &a.SignedHeader2)
}

func (a *ProposerSlashing) ByteLength() uint64 {
	return common.SignedBeaconBlockHeaderType.TypeByteLength() * 2
}

func (*ProposerSlashing) FixedLength() uint64 {
	return common.SignedBeaconBlockHeaderType.TypeByteLength() * 2
}

func (p *ProposerSlashing) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&p.SignedHeader1, &p.SignedHeader2)
}

var ProposerSlashingType = ContainerType("ProposerSlashing", []FieldDef{
	{"header_1", common.SignedBeaconBlockHeaderType},
	{"header_2", common.SignedBeaconBlockHeaderType},
})

func BlockProposerSlashingsType(spec *common.Spec) ListTypeDef {
	return ListType(ProposerSlashingType, uint64(spec.MAX_PROPOSER_SLASHINGS))
}

type ProposerSlashings []ProposerSlashing

func (a *ProposerSlashings) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, ProposerSlashing{})
		return &((*a)[i])
	}, ProposerSlashingType.TypeByteLength(), uint64(spec.MAX_PROPOSER_SLASHINGS))
}

func (a ProposerSlashings) Serialize(_ *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, ProposerSlashingType.TypeByteLength(), uint64(len(a)))
}

func (a ProposerSlashings) ByteLength(_ *common.Spec) (out uint64) {
	return ProposerSlashingType.TypeByteLength() * uint64(len(a))
}

func (*ProposerSlashings) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li ProposerSlashings) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_PROPOSER_SLASHINGS))
}

func (li ProposerSlashings) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]ProposerSlashing{}) // encode as empty list, not null
	}
	return json.Marshal([]ProposerSlashing(li))
}

func ProcessProposerSlashings(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []ProposerSlashing) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessProposerSlashing(spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ValidateProposerSlashingNoSignature(spec *common.Spec, ps *ProposerSlashing) error {
	// Verify header slots match
	if a, b := ps.SignedHeader1.Message.Slot, ps.SignedHeader2.Message.Slot; a != b {
		return fmt.Errorf("proposer slashing requires slashing headers to have the same slot: %d <> %d", a, b)
	}
	// Verify header proposer indices match
	if a, b := ps.SignedHeader1.Message.ProposerIndex, ps.SignedHeader2.Message.ProposerIndex; a != b {
		return fmt.Errorf("proposer slashing headers proposer-indices do not match: %d <> %d", a, b)
	}
	// Verify the headers are different
	if ps.SignedHeader1.Message == ps.SignedHeader2.Message {
		return errors.New("proposer slashing requires two different headers")
	}
	return nil
}

func ValidateProposerSlashing(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ps *ProposerSlashing) error {
	if err := ValidateProposerSlashingNoSignature(spec, ps); err != nil {
		return err
	}
	proposerIndex := ps.SignedHeader1.Message.ProposerIndex
	// Verify header proposer index is valid
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	if valid, err := vals.IsValidIndex(proposerIndex); err != nil {
		return err
	} else if !valid {
		return errors.New("invalid proposer index")
	}
	currentEpoch := epc.CurrentEpoch.Epoch
	// Verify the proposer is slashable
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	validator, err := validators.Validator(proposerIndex)
	if err != nil {
		return err
	}
	if slashable, err := IsSlashable(validator, currentEpoch); err != nil {
		return err
	} else if !slashable {
		return errors.New("proposer slashing requires proposer to be slashable")
	}
	domain, err := common.GetDomain(state, common.DOMAIN_BEACON_PROPOSER, spec.SlotToEpoch(ps.SignedHeader1.Message.Slot))
	if err != nil {
		return err
	}
	pubkey, ok := epc.ValidatorPubkeyCache.Pubkey(proposerIndex)
	if !ok {
		return errors.New("could not find pubkey of proposer")
	}
	blsPub, err := pubkey.Pubkey()
	if err != nil {
		return err
	}
	sigRoot1 := common.ComputeSigningRoot(ps.SignedHeader1.Message.HashTreeRoot(tree.GetHashFn()), domain)
	sigRoot2 := common.ComputeSigningRoot(ps.SignedHeader2.Message.HashTreeRoot(tree.GetHashFn()), domain)
	sig1, err := ps.SignedHeader1.Signature.Signature()
	if err != nil {
		return err
	}
	sig2, err := ps.SignedHeader2.Signature.Signature()
	if err != nil {
		return err
	}
	// Verify signatures
	if !blsu.Verify(blsPub, sigRoot1[:], sig1) {
		return errors.New("proposer slashing header 1 has invalid BLS signature")
	}
	if !blsu.Verify(blsPub, sigRoot2[:], sig2) {
		return errors.New("proposer slashing header 2 has invalid BLS signature")
	}
	return nil
}

func ProcessProposerSlashing(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ps *ProposerSlashing) error {
	if err := ValidateProposerSlashing(spec, epc, state, ps); err != nil {
		return err
	}
	return SlashValidator(spec, epc, state, ps.SignedHeader1.Message.ProposerIndex, nil)
}
