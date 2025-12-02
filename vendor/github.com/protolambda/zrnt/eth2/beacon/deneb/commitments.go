package deneb

import (
	"encoding/json"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	"github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

type KZGCommitments []common.KZGCommitment

func (li *KZGCommitments) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*li)
		*li = append(*li, common.KZGCommitment{})
		return &((*li)[i])
	}, common.KZGCommitmentSize, uint64(spec.MAX_BLOB_COMMITMENTS_PER_BLOCK))
}

func (li KZGCommitments) Serialize(_ *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &li[i]
	}, common.KZGCommitmentSize, uint64(len(li)))
}

func (li KZGCommitments) ByteLength(_ *common.Spec) (out uint64) {
	return common.KZGCommitmentSize * uint64(len(li))
}

func (*KZGCommitments) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li KZGCommitments) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.MAX_BLOB_COMMITMENTS_PER_BLOCK))
}

func (li KZGCommitments) MarshalJSON() ([]byte, error) {
	if li == nil {
		return json.Marshal([]common.KZGCommitment{}) // encode as empty list, not null
	}
	return json.Marshal([]common.KZGCommitment(li))
}

func KZGCommitmentsType(spec *common.Spec) *view.ComplexListTypeDef {
	return view.ComplexListType(common.KZGCommitmentType, uint64(spec.MAX_BLOB_COMMITMENTS_PER_BLOCK))
}
