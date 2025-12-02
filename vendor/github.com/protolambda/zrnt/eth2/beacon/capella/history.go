package capella

import (
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

// HistoricalSummary is a summary of HistoricalBatch and was introduced in Capella
type HistoricalSummary struct {
	BlockSummaryRoot common.Root
	StateSummaryRoot common.Root
}

func (hs *HistoricalSummary) View() *ContainerView {
	a, b := RootView(hs.BlockSummaryRoot), RootView(hs.StateSummaryRoot)
	c, _ := HistoricalSummaryType.FromFields(&a, &b)
	return c
}

func (hs *HistoricalSummary) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(&hs.BlockSummaryRoot, &hs.StateSummaryRoot)
}

func (hs *HistoricalSummary) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(&hs.BlockSummaryRoot, &hs.StateSummaryRoot)
}

func (*HistoricalSummary) ByteLength() uint64 {
	return 32 * 2
}

func (*HistoricalSummary) FixedLength() uint64 {
	return 32 * 2
}

func (hs *HistoricalSummary) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&hs.BlockSummaryRoot, &hs.StateSummaryRoot)
}

var HistoricalSummaryType = ContainerType("HistoricalSummary", []FieldDef{
	{"block_summary_root", RootType},
	{"state_summary_root", RootType},
})

// HistoricalSummaries are the summaries of historical batches
type HistoricalSummaries []HistoricalSummary

func (a *HistoricalSummaries) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, HistoricalSummary{})
		return &((*a)[i])
	}, HistoricalSummaryType.TypeByteLength(), uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

func (a HistoricalSummaries) Serialize(_ *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, HistoricalSummaryType.TypeByteLength(), uint64(len(a)))
}

func (a HistoricalSummaries) ByteLength(_ *common.Spec) (out uint64) {
	return HistoricalSummaryType.TypeByteLength() * uint64(len(a))
}

func (*HistoricalSummaries) FixedLength(*common.Spec) uint64 {
	return 0
}

func (li HistoricalSummaries) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

func HistoricalSummariesType(spec *common.Spec) ListTypeDef {
	return ListType(HistoricalSummaryType, uint64(spec.HISTORICAL_ROOTS_LIMIT))
}

type HistoricalSummariesView struct{ *ComplexListView }

func AsHistoricalSummaries(v View, err error) (*HistoricalSummariesView, error) {
	c, err := AsComplexList(v, err)
	return &HistoricalSummariesView{c}, err
}

func (h *HistoricalSummariesView) Append(summary HistoricalSummary) error {
	v := summary.View()
	return h.ComplexListView.Append(v)
}
