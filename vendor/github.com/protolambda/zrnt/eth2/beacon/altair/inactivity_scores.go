package altair

import (
	"bytes"
	"context"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type InactivityScores []Uint64View

func (a *InactivityScores) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, Uint64View(0))
		return &(*a)[i]
	}, Uint64Type.TypeByteLength(), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (a InactivityScores) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, Uint64Type.TypeByteLength(), uint64(len(a)))
}

func (a InactivityScores) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(a)) * Uint64Type.TypeByteLength()
}

func (a *InactivityScores) FixedLength(spec *common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li InactivityScores) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.Uint64ListHTR(func(i uint64) uint64 {
		return uint64(li[i])
	}, length, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (li InactivityScores) View(spec *common.Spec) (*ParticipationRegistryView, error) {
	typ := InactivityScoresType(spec)
	var buf bytes.Buffer
	if err := li.Serialize(spec, codec.NewEncodingWriter(&buf)); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	dec := codec.NewDecodingReader(bytes.NewReader(data), uint64(len(data)))
	return AsParticipationRegistry(typ.Deserialize(dec))
}

func InactivityScoresType(spec *common.Spec) *BasicListTypeDef {
	return BasicListType(Uint64Type, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

type InactivityScoresView struct {
	*BasicListView
}

func AsInactivityScores(v View, err error) (*InactivityScoresView, error) {
	c, err := AsBasicList(v, err)
	return &InactivityScoresView{c}, err
}

func (v *InactivityScoresView) GetScore(index common.ValidatorIndex) (uint64, error) {
	s, err := AsUint64(v.Get(uint64(index)))
	return uint64(s), err
}

func (v *InactivityScoresView) SetScore(index common.ValidatorIndex, score uint64) error {
	return v.Set(uint64(index), Uint64View(score))
}

func ProcessInactivityUpdates(ctx context.Context, spec *common.Spec, attesterData *EpochAttesterData, state AltairLikeBeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Skip the genesis epoch as score updates are based on the previous epoch participation
	if attesterData.CurrEpoch == common.GENESIS_EPOCH {
		return nil
	}
	inactivityScores, err := state.InactivityScores()
	if err != nil {
		return err
	}
	finalized, err := state.FinalizedCheckpoint()
	if err != nil {
		return err
	}
	finalityDelay := attesterData.PrevEpoch - finalized.Epoch
	isInactivityLeak := finalityDelay > spec.MIN_EPOCHS_TO_INACTIVITY_PENALTY

	for _, vi := range attesterData.EligibleIndices {
		score, err := inactivityScores.GetScore(vi)
		if err != nil {
			return err
		}
		newScore := score

		// Increase the inactivity score of inactive validators
		if !attesterData.Flats[vi].Slashed && (attesterData.PrevParticipation[vi]&TIMELY_TARGET_FLAG != 0) {
			if newScore > 0 {
				newScore -= 1
			}
		} else {
			newScore += uint64(spec.INACTIVITY_SCORE_BIAS)
		}

		// Decrease the inactivity score of all eligible validators during a leak-free epoch
		if !isInactivityLeak {
			if newScore < uint64(spec.INACTIVITY_SCORE_RECOVERY_RATE) {
				newScore = 0
			} else {
				newScore -= uint64(spec.INACTIVITY_SCORE_RECOVERY_RATE)
			}
		}

		// if there was any change, update the state.
		if newScore != score {
			if err := inactivityScores.SetScore(vi, newScore); err != nil {
				return err
			}
		}
	}
	return nil
}
