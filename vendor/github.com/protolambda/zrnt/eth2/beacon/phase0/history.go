package phase0

import (
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

// HistoricalBatchRoots stores roots: a batch of state or block roots.
// It represents a Vector[Root, SLOTS_PER_HISTORICAL_ROOT]
type HistoricalBatchRoots []common.Root

func (a *HistoricalBatchRoots) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return tree.ReadRoots(dr, (*[]common.Root)(a), uint64(spec.SLOTS_PER_HISTORICAL_ROOT))
}

func (a HistoricalBatchRoots) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return tree.WriteRoots(w, a)
}

func (a HistoricalBatchRoots) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(spec.SLOTS_PER_HISTORICAL_ROOT) * 32
}

func (a *HistoricalBatchRoots) FixedLength(spec *common.Spec) uint64 {
	return uint64(spec.SLOTS_PER_HISTORICAL_ROOT) * 32
}

func (li HistoricalBatchRoots) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexVectorHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length)
}

type HistoricalBatch struct {
	BlockRoots HistoricalBatchRoots `json:"block_roots" yaml:"block_roots"`
	StateRoots HistoricalBatchRoots `json:"state_roots" yaml:"state_roots"`
}

func (a *HistoricalBatch) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(spec.Wrap(&a.BlockRoots), spec.Wrap(&a.StateRoots))
}

func (a *HistoricalBatch) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.FixedLenContainer(spec.Wrap(&a.BlockRoots), spec.Wrap(&a.StateRoots))
}

func (a *HistoricalBatch) ByteLength(spec *common.Spec) uint64 {
	return uint64(spec.SLOTS_PER_HISTORICAL_ROOT) * 32 * 2
}

func (*HistoricalBatch) FixedLength(spec *common.Spec) uint64 {
	return uint64(spec.SLOTS_PER_HISTORICAL_ROOT) * 32 * 2
}

func (p *HistoricalBatch) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.BlockRoots), spec.Wrap(&p.StateRoots))
}

func BatchRootsType(spec *common.Spec) VectorTypeDef {
	return VectorType(RootType, uint64(spec.SLOTS_PER_HISTORICAL_ROOT))
}

type BatchRootsView struct{ *ComplexVectorView }

// Return the root at the given slot. Only valid to SLOTS_PER_HISTORICAL_ROOT slots ago.
func (v *BatchRootsView) GetRoot(slot common.Slot) (common.Root, error) {
	i := uint64(slot) % v.VectorLength
	return AsRoot(v.Get(i))
}

func (v *BatchRootsView) SetRoot(slot common.Slot, r common.Root) error {
	i := uint64(slot) % v.VectorLength
	rv := RootView(r)
	return v.Set(i, &rv)
}

func AsBatchRoots(v View, err error) (*BatchRootsView, error) {
	c, err := AsComplexVector(v, err)
	return &BatchRootsView{c}, err
}

func HistoricalBatchType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("HistoricalBatch", []FieldDef{
		{"block_roots", BatchRootsType(spec)},
		{"state_roots", BatchRootsType(spec)},
	})
}

type HistoricalBatchView struct{ *ContainerView }

func (v *HistoricalBatchView) BlockRoots() (*BatchRootsView, error) {
	return AsBatchRoots(v.Get(0))
}

func (v *HistoricalBatchView) StateRoots() (*BatchRootsView, error) {
	return AsBatchRoots(v.Get(1))
}

func AsHistoricalBatch(v View, err error) (*HistoricalBatchView, error) {
	c, err := AsContainer(v, err)
	return &HistoricalBatchView{c}, err
}

// roots of HistoricalBatch
type HistoricalRoots []common.Root

func (a *HistoricalRoots) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return tree.ReadRootsLimited(dr, (*[]common.Root)(a), uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

func (a HistoricalRoots) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return tree.WriteRoots(w, a)
}

func (a HistoricalRoots) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(a)) * 32
}

func (a *HistoricalRoots) FixedLength(spec *common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li HistoricalRoots) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

func HistoricalRootsType(spec *common.Spec) ListTypeDef {
	return ListType(RootType, uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

// roots of HistoricalBatch
type HistoricalRootsView struct{ *ComplexListView }

func AsHistoricalRoots(v View, err error) (*HistoricalRootsView, error) {
	c, err := AsComplexList(v, err)
	return &HistoricalRootsView{c}, err
}

func (h *HistoricalRootsView) Append(root common.Root) error {
	v := RootView(root)
	return h.ComplexListView.Append(&v)
}
