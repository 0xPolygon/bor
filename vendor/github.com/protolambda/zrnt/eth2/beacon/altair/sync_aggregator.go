package altair

import (
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

var SyncAggregatorSelectionDataType = ContainerType("SyncAggregatorSelectionData", []FieldDef{
	{"slot", common.SlotType},
	{"subcommittee_index", Uint64Type},
})

type SyncAggregatorSelectionData struct {
	Slot              common.Slot `yaml:"slot" json:"slot"`
	SubcommitteeIndex Uint64View  `yaml:"subcommittee_index" json:"subcommittee_index"`
}

func (sd *SyncAggregatorSelectionData) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(
		&sd.Slot,
		&sd.SubcommitteeIndex,
	)
}

func (sd *SyncAggregatorSelectionData) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(
		&sd.Slot,
		&sd.SubcommitteeIndex,
	)
}

func (sd *SyncAggregatorSelectionData) ByteLength() uint64 {
	return 8 + 8
}

func (sd *SyncAggregatorSelectionData) FixedLength() uint64 {
	return 8 + 8
}

func (sd *SyncAggregatorSelectionData) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		&sd.Slot,
		&sd.SubcommitteeIndex,
	)
}
