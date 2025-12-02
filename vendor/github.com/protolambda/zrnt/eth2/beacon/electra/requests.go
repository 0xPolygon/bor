package electra

import (
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type ExecutionRequests struct {
	// [New in Electra:EIP6110]
	Deposits common.DepositRequests `json:"deposits" yaml:"deposits"`
	// [New in Electra:EIP7002:EIP7251]
	Withdrawals common.WithdrawalRequests `json:"withdrawals" yaml:"withdrawals"`
	// [New in Electra:EIP7251]
	Consolidations common.ConsolidationRequests `json:"consolidations" yaml:"consolidations"`
}

func ExecutionRequestsType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("ExecutionRequests", []FieldDef{
		{"deposits", common.DepositRequestsType(spec)},
		{"withdrawals", common.WithdrawalRequestsType(spec)},
		{"consolidations", common.ConsolidationRequestsType(spec)},
	})
}

func (p *ExecutionRequests) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (p *ExecutionRequests) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (p *ExecutionRequests) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (*ExecutionRequests) FixedLength(*common.Spec) uint64 {
	return 0
}

func (p *ExecutionRequests) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}
