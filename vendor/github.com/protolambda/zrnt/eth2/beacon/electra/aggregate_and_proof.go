package electra

import (
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
)

type SignedAggregateAndProof struct {
	Message   AggregateAndProof   `json:"message"`
	Signature common.BLSSignature `json:"signature"`
}

func (a *SignedAggregateAndProof) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.Message), &a.Signature)
}

func (a *SignedAggregateAndProof) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.Message), &a.Signature)
}

func (a *SignedAggregateAndProof) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&a.Message), &a.Signature)
}

func (a *SignedAggregateAndProof) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *SignedAggregateAndProof) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.Message), &a.Signature)
}

type AggregateAndProof struct {
	AggregatorIndex common.ValidatorIndex `json:"aggregator_index"`
	Aggregate       Attestation           `json:"aggregate"`
	SelectionProof  common.BLSSignature   `json:"selection_proof"`
}

func (a *AggregateAndProof) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(&a.AggregatorIndex, spec.Wrap(&a.Aggregate), &a.SelectionProof)
}

func (a *AggregateAndProof) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(&a.AggregatorIndex, spec.Wrap(&a.Aggregate), &a.SelectionProof)
}

func (a *AggregateAndProof) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(&a.AggregatorIndex, spec.Wrap(&a.Aggregate), &a.SelectionProof)
}

func (a *AggregateAndProof) FixedLength(*common.Spec) uint64 {
	return 0
}

func (a *AggregateAndProof) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&a.AggregatorIndex, spec.Wrap(&a.Aggregate), &a.SelectionProof)
}
