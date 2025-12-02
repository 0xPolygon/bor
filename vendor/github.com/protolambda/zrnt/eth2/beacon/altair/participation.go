package altair

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
	"gopkg.in/yaml.v3"
)

const ParticipationFlagsType = Uint8Type

type ParticipationFlags Uint8View

func AsParticipationFlags(v View, err error) (ParticipationFlags, error) {
	i, err := AsUint8(v, err)
	return ParticipationFlags(i), err
}

func (a *ParticipationFlags) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint8View)(a).Deserialize(dr)
}

func (i ParticipationFlags) Serialize(w *codec.EncodingWriter) error {
	return w.WriteByte(uint8(i))
}

func (ParticipationFlags) ByteLength() uint64 {
	return 8
}

func (ParticipationFlags) FixedLength() uint64 {
	return 8
}

func (t ParticipationFlags) HashTreeRoot(hFn tree.HashFn) common.Root {
	return Uint8View(t).HashTreeRoot(hFn)
}

func (e ParticipationFlags) MarshalJSON() ([]byte, error) {
	return Uint8View(e).MarshalJSON()
}

func (e *ParticipationFlags) UnmarshalJSON(b []byte) error {
	return ((*Uint8View)(e)).UnmarshalJSON(b)
}

func (e ParticipationFlags) String() string {
	return Uint8View(e).String()
}

// Participation flag indices
const (
	TIMELY_SOURCE_FLAG_INDEX uint8 = 0
	TIMELY_TARGET_FLAG_INDEX uint8 = 1
	TIMELY_HEAD_FLAG_INDEX   uint8 = 2
)

const (
	TIMELY_SOURCE_FLAG ParticipationFlags = 1 << TIMELY_SOURCE_FLAG_INDEX
	TIMELY_TARGET_FLAG ParticipationFlags = 1 << TIMELY_TARGET_FLAG_INDEX
	TIMELY_HEAD_FLAG   ParticipationFlags = 1 << TIMELY_HEAD_FLAG_INDEX
)

// Participation flag fractions
const (
	TIMELY_SOURCE_WEIGHT common.Gwei = 14
	TIMELY_TARGET_WEIGHT common.Gwei = 26
	TIMELY_HEAD_WEIGHT   common.Gwei = 14
	SYNC_REWARD_WEIGHT   common.Gwei = 2
	PROPOSER_WEIGHT      common.Gwei = 8
	WEIGHT_DENOMINATOR   common.Gwei = 64
)

type ParticipationRegistry []ParticipationFlags

func (r ParticipationRegistry) String() string {
	out, _ := json.Marshal([]ParticipationFlags(r))
	return string(out)
}

func (r *ParticipationRegistry) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*[]ParticipationFlags)(r))
}

func (r ParticipationRegistry) MarshalJSON() ([]byte, error) {
	return json.Marshal([]ParticipationFlags(r))
}

func (r *ParticipationRegistry) UnmarshalYAML(value *yaml.Node) error {
	return value.Decode((*[]ParticipationFlags)(r))
}

func (r ParticipationRegistry) MarshalYAML() (interface{}, error) {
	return ([]ParticipationFlags)(r), nil
}

func (r *ParticipationRegistry) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*r)
		*r = append(*r, ParticipationFlags(0))
		return &(*r)[i]
	}, ParticipationFlagsType.TypeByteLength(), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (r ParticipationRegistry) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &r[i]
	}, ParticipationFlagsType.TypeByteLength(), uint64(len(r)))
}

func (r ParticipationRegistry) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(r)) * ParticipationFlagsType.TypeByteLength()
}

func (r *ParticipationRegistry) FixedLength(spec *common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (r ParticipationRegistry) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.Uint8ListHTR(func(i uint64) uint8 {
		return uint8(r[i])
	}, uint64(len(r)), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (r ParticipationRegistry) View(spec *common.Spec) (*ParticipationRegistryView, error) {
	typ := ParticipationRegistryType(spec)
	var buf bytes.Buffer
	if err := r.Serialize(spec, codec.NewEncodingWriter(&buf)); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	dec := codec.NewDecodingReader(bytes.NewReader(data), uint64(len(data)))
	return AsParticipationRegistry(typ.Deserialize(dec))
}

func ParticipationRegistryType(spec *common.Spec) *BasicListTypeDef {
	return BasicListType(ParticipationFlagsType, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

type ParticipationRegistryView struct {
	*BasicListView
}

func AsParticipationRegistry(v View, err error) (*ParticipationRegistryView, error) {
	c, err := AsBasicList(v, err)
	return &ParticipationRegistryView{c}, err
}

func (v *ParticipationRegistryView) GetFlags(index common.ValidatorIndex) (ParticipationFlags, error) {
	return AsParticipationFlags(v.Get(uint64(index)))
}

func (v *ParticipationRegistryView) SetFlags(index common.ValidatorIndex, score ParticipationFlags) error {
	return v.Set(uint64(index), Uint8View(score))
}

func (v *ParticipationRegistryView) Raw() (ParticipationRegistry, error) {
	length, err := v.Length()
	if err != nil {
		return nil, err
	}
	out := make(ParticipationRegistry, 0, length)
	iter := v.ReadonlyIter()
	for {
		el, ok, err := iter.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out = append(out, ParticipationFlags(el.(Uint8View)))
	}
	return out, nil
}

func (v *ParticipationRegistryView) FillZeroes(length uint64) error {
	// 32 flags (uint8) per node (bytes32)
	nodesLen := (length + 31) / 32
	depth := tree.CoverDepth(v.BottomNodeLimit())
	zero := &tree.Root{}
	contents, err := tree.SubtreeFillToLength(zero, depth, nodesLen)
	if err != nil {
		return err
	}
	lengthNode := &tree.Root{}
	binary.LittleEndian.PutUint64(lengthNode[:8], length)
	return v.SetBacking(tree.NewPairNode(contents, lengthNode))
}

func ProcessParticipationFlagUpdates(ctx context.Context, spec *common.Spec, state AltairLikeBeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	currentEp, err := state.CurrentEpochParticipation()
	if err != nil {
		return err
	}
	previousEp, err := state.PreviousEpochParticipation()
	if err != nil {
		return err
	}
	if err := previousEp.SetBacking(currentEp.Backing()); err != nil {
		return err
	}
	length, err := currentEp.Length()
	if err != nil {
		return err
	}
	return currentEp.FillZeroes(length)
}
