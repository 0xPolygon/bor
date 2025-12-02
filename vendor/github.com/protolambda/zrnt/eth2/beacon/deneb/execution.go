package deneb

import (
	"context"
	"fmt"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

const (
	__parentHash = iota
	__feeRecipient
	__stateRoot
	__receiptsRoot
	__logsBloom
	__prevRandao
	__blockNumber
	__gasLimit
	__gasUsed
	__timestamp
	__extraData
	__baseFeePerGas
	__blockHash
	__transactionsRoot
	__withdrawalsRoot
	__blobGasUsed
	__excessBlobGas
	__end
)

var ExecutionPayloadHeaderType = ContainerType("ExecutionPayloadHeader", []FieldDef{
	{"parent_hash", common.Hash32Type},
	{"fee_recipient", common.Eth1AddressType},
	{"state_root", common.Bytes32Type},
	{"receipts_root", common.Bytes32Type},
	{"logs_bloom", common.LogsBloomType},
	{"prev_randao", common.Bytes32Type},
	{"block_number", Uint64Type},
	{"gas_limit", Uint64Type},
	{"gas_used", Uint64Type},
	{"timestamp", common.TimestampType},
	{"extra_data", common.ExtraDataType},
	{"base_fee_per_gas", Uint256Type},
	{"block_hash", common.Hash32Type},
	{"transactions_root", RootType},
	{"withdrawals_root", RootType},
	{"blob_gas_used", Uint64Type},   // new in Deneb
	{"excess_blob_gas", Uint64Type}, // new in Deneb
})

type ExecutionPayloadHeaderView struct {
	*ContainerView
}

func (v *ExecutionPayloadHeaderView) Raw() (*ExecutionPayloadHeader, error) {
	values, err := v.FieldValues()
	if err != nil {
		return nil, err
	}
	if len(values) != __end {
		return nil, fmt.Errorf("unexpected number of execution payload header fields: %d", len(values))
	}
	parentHash, err := AsRoot(values[__parentHash], err)
	feeRecipient, err := common.AsEth1Address(values[__feeRecipient], err)
	stateRoot, err := AsRoot(values[__stateRoot], err)
	receiptsRoot, err := AsRoot(values[__receiptsRoot], err)
	logsBloomView, err := common.AsLogsBloom(values[__logsBloom], err)
	prevRandao, err := AsRoot(values[__prevRandao], err)
	blockNumber, err := AsUint64(values[__blockNumber], err)
	gasLimit, err := AsUint64(values[__gasLimit], err)
	gasUsed, err := AsUint64(values[__gasUsed], err)
	timestamp, err := common.AsTimestamp(values[__timestamp], err)
	extraDataView, err := common.AsExtraData(values[__extraData], err)
	baseFeePerGas, err := AsUint256(values[__baseFeePerGas], err)
	blockHash, err := AsRoot(values[__blockHash], err)
	transactionsRoot, err := AsRoot(values[__transactionsRoot], err)
	withdrawalsRoot, err := AsRoot(values[__withdrawalsRoot], err)
	blobGasUsed, err := AsUint64(values[__blobGasUsed], err)
	if err != nil {
		return nil, err
	}
	excessBlobGas, err := AsUint64(values[__excessBlobGas], err)
	if err != nil {
		return nil, err
	}
	logsBloom, err := logsBloomView.Raw()
	if err != nil {
		return nil, err
	}
	extraData, err := extraDataView.Raw()
	if err != nil {
		return nil, err
	}
	return &ExecutionPayloadHeader{
		ParentHash:       parentHash,
		FeeRecipient:     feeRecipient,
		StateRoot:        stateRoot,
		ReceiptsRoot:     receiptsRoot,
		LogsBloom:        *logsBloom,
		PrevRandao:       prevRandao,
		BlockNumber:      blockNumber,
		GasLimit:         gasLimit,
		GasUsed:          gasUsed,
		Timestamp:        timestamp,
		ExtraData:        extraData,
		BaseFeePerGas:    baseFeePerGas,
		BlockHash:        blockHash,
		TransactionsRoot: transactionsRoot,
		WithdrawalsRoot:  withdrawalsRoot,
		BlobGasUsed:      blobGasUsed,
		ExcessBlobGas:    excessBlobGas,
	}, nil
}

func (v *ExecutionPayloadHeaderView) ParentHash() (common.Hash32, error) {
	return AsRoot(v.Get(__parentHash))
}

func (v *ExecutionPayloadHeaderView) FeeRecipient() (common.Eth1Address, error) {
	return common.AsEth1Address(v.Get(__feeRecipient))
}

func (v *ExecutionPayloadHeaderView) StateRoot() (common.Bytes32, error) {
	return AsRoot(v.Get(__stateRoot))
}

func (v *ExecutionPayloadHeaderView) ReceiptRoot() (common.Bytes32, error) {
	return AsRoot(v.Get(__receiptsRoot))
}

func (v *ExecutionPayloadHeaderView) LogsBloom() (*common.LogsBloom, error) {
	logV, err := common.AsLogsBloom(v.Get(__logsBloom))
	if err != nil {
		return nil, err
	}
	return logV.Raw()
}

func (v *ExecutionPayloadHeaderView) Random() (common.Bytes32, error) {
	return AsRoot(v.Get(__prevRandao))
}

func (v *ExecutionPayloadHeaderView) BlockNumber() (Uint64View, error) {
	return AsUint64(v.Get(__blockNumber))
}

func (v *ExecutionPayloadHeaderView) GasLimit() (Uint64View, error) {
	return AsUint64(v.Get(__gasLimit))
}

func (v *ExecutionPayloadHeaderView) GasUsed() (Uint64View, error) {
	return AsUint64(v.Get(__gasUsed))
}

func (v *ExecutionPayloadHeaderView) Timestamp() (common.Timestamp, error) {
	return common.AsTimestamp(v.Get(__timestamp))
}

func (v *ExecutionPayloadHeaderView) BaseFeePerGas() (Uint256View, error) {
	return AsUint256(v.Get(__baseFeePerGas))
}

func (v *ExecutionPayloadHeaderView) BlockHash() (common.Hash32, error) {
	return AsRoot(v.Get(__blockHash))
}

func (v *ExecutionPayloadHeaderView) TransactionsRoot() (common.Root, error) {
	return AsRoot(v.Get(__transactionsRoot))
}

func (v *ExecutionPayloadHeaderView) BlobGasUsed() (Uint64View, error) {
	return AsUint64(v.Get(__blobGasUsed))
}

func (v *ExecutionPayloadHeaderView) ExcessBlobGas() (Uint64View, error) {
	return AsUint64(v.Get(__excessBlobGas))
}

func AsExecutionPayloadHeader(v View, err error) (*ExecutionPayloadHeaderView, error) {
	c, err := AsContainer(v, err)
	return &ExecutionPayloadHeaderView{c}, err
}

type ExecutionPayloadHeader struct {
	ParentHash       common.Hash32      `json:"parent_hash" yaml:"parent_hash"`
	FeeRecipient     common.Eth1Address `json:"fee_recipient" yaml:"fee_recipient"`
	StateRoot        common.Bytes32     `json:"state_root" yaml:"state_root"`
	ReceiptsRoot     common.Bytes32     `json:"receipts_root" yaml:"receipts_root"`
	LogsBloom        common.LogsBloom   `json:"logs_bloom" yaml:"logs_bloom"`
	PrevRandao       common.Bytes32     `json:"prev_randao" yaml:"prev_randao"`
	BlockNumber      Uint64View         `json:"block_number" yaml:"block_number"`
	GasLimit         Uint64View         `json:"gas_limit" yaml:"gas_limit"`
	GasUsed          Uint64View         `json:"gas_used" yaml:"gas_used"`
	Timestamp        common.Timestamp   `json:"timestamp" yaml:"timestamp"`
	ExtraData        common.ExtraData   `json:"extra_data" yaml:"extra_data"`
	BaseFeePerGas    Uint256View        `json:"base_fee_per_gas" yaml:"base_fee_per_gas"`
	BlockHash        common.Hash32      `json:"block_hash" yaml:"block_hash"`
	TransactionsRoot common.Root        `json:"transactions_root" yaml:"transactions_root"`
	WithdrawalsRoot  common.Root        `json:"withdrawals_root" yaml:"withdrawals_root"`
	BlobGasUsed      Uint64View         `json:"blob_gas_used" yaml:"blob_gas_used"`
	ExcessBlobGas    Uint64View         `json:"excess_blob_gas" yaml:"excess_blob_gas"`
}

func (s *ExecutionPayloadHeader) View() *ExecutionPayloadHeaderView {
	ed, err := s.ExtraData.View()
	if err != nil {
		panic(err)
	}
	pr, cb, sr, rr := (*RootView)(&s.ParentHash), s.FeeRecipient.View(), (*RootView)(&s.StateRoot), (*RootView)(&s.ReceiptsRoot)
	lb, rng, nr, gl, gu := s.LogsBloom.View(), (*RootView)(&s.PrevRandao), s.BlockNumber, s.GasLimit, s.GasUsed
	ts, bf := Uint64View(s.Timestamp), &s.BaseFeePerGas
	bh, tr := (*RootView)(&s.BlockHash), (*RootView)(&s.TransactionsRoot)
	wr := (*RootView)(&s.WithdrawalsRoot)
	bgu, ebg := &s.BlobGasUsed, &s.ExcessBlobGas

	v, err := AsExecutionPayloadHeader(ExecutionPayloadHeaderType.FromFields(pr, cb, sr, rr, lb, rng, nr, gl, gu, ts, ed, bf, bh, tr, wr, bgu, ebg))
	if err != nil {
		panic(err)
	}
	return v
}

func (s *ExecutionPayloadHeader) Deserialize(dr *codec.DecodingReader) error {
	return dr.Container(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, &s.TransactionsRoot, &s.WithdrawalsRoot,
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (s *ExecutionPayloadHeader) Serialize(w *codec.EncodingWriter) error {
	return w.Container(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, &s.TransactionsRoot, &s.WithdrawalsRoot,
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (s *ExecutionPayloadHeader) ByteLength() uint64 {
	return codec.ContainerLength(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, &s.TransactionsRoot, &s.WithdrawalsRoot,
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (b *ExecutionPayloadHeader) FixedLength() uint64 {
	return 0
}

func (s *ExecutionPayloadHeader) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, &s.TransactionsRoot, &s.WithdrawalsRoot,
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func ExecutionPayloadType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("ExecutionPayload", []FieldDef{
		{"parent_hash", common.Hash32Type},
		{"fee_recipient", common.Eth1AddressType},
		{"state_root", common.Bytes32Type},
		{"receipts_root", common.Bytes32Type},
		{"logs_bloom", common.LogsBloomType},
		{"prev_randao", common.Bytes32Type},
		{"block_number", Uint64Type},
		{"gas_limit", Uint64Type},
		{"gas_used", Uint64Type},
		{"timestamp", common.TimestampType},
		{"extra_data", common.ExtraDataType},
		{"base_fee_per_gas", Uint256Type},
		{"block_hash", common.Hash32Type},
		{"transactions", common.PayloadTransactionsType(spec)},
		{"withdrawals", common.WithdrawalsType(spec)},
		{"blob_gas_used", Uint64Type},   // new in Deneb
		{"excess_blob_gas", Uint64Type}, // new in Deneb
	})
}

type ExecutionPayloadView struct {
	*ContainerView
}

func AsExecutionPayload(v View, err error) (*ExecutionPayloadView, error) {
	c, err := AsContainer(v, err)
	return &ExecutionPayloadView{c}, err
}

type ExecutionPayload struct {
	ParentHash    common.Hash32              `json:"parent_hash" yaml:"parent_hash"`
	FeeRecipient  common.Eth1Address         `json:"fee_recipient" yaml:"fee_recipient"`
	StateRoot     common.Bytes32             `json:"state_root" yaml:"state_root"`
	ReceiptsRoot  common.Bytes32             `json:"receipts_root" yaml:"receipts_root"`
	LogsBloom     common.LogsBloom           `json:"logs_bloom" yaml:"logs_bloom"`
	PrevRandao    common.Bytes32             `json:"prev_randao" yaml:"prev_randao"`
	BlockNumber   Uint64View                 `json:"block_number" yaml:"block_number"`
	GasLimit      Uint64View                 `json:"gas_limit" yaml:"gas_limit"`
	GasUsed       Uint64View                 `json:"gas_used" yaml:"gas_used"`
	Timestamp     common.Timestamp           `json:"timestamp" yaml:"timestamp"`
	ExtraData     common.ExtraData           `json:"extra_data" yaml:"extra_data"`
	BaseFeePerGas Uint256View                `json:"base_fee_per_gas" yaml:"base_fee_per_gas"`
	BlockHash     common.Hash32              `json:"block_hash" yaml:"block_hash"`
	Transactions  common.PayloadTransactions `json:"transactions" yaml:"transactions"`
	Withdrawals   common.Withdrawals         `json:"withdrawals" yaml:"withdrawals"`
	BlobGasUsed   Uint64View                 `json:"blob_gas_used" yaml:"blob_gas_used"`
	ExcessBlobGas Uint64View                 `json:"excess_blob_gas" yaml:"excess_blob_gas"`
}

func (s *ExecutionPayload) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, spec.Wrap(&s.Transactions), spec.Wrap(&s.Withdrawals),
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (s *ExecutionPayload) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, spec.Wrap(&s.Transactions), spec.Wrap(&s.Withdrawals),
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (s *ExecutionPayload) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, spec.Wrap(&s.Transactions), spec.Wrap(&s.Withdrawals),
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (a *ExecutionPayload) FixedLength(*common.Spec) uint64 {
	// transactions list is not fixed length, so the whole thing is not fixed length.
	return 0
}

func (s *ExecutionPayload) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(&s.ParentHash, &s.FeeRecipient, &s.StateRoot,
		&s.ReceiptsRoot, &s.LogsBloom, &s.PrevRandao, &s.BlockNumber, &s.GasLimit,
		&s.GasUsed, &s.Timestamp, &s.ExtraData, &s.BaseFeePerGas,
		&s.BlockHash, spec.Wrap(&s.Transactions), spec.Wrap(&s.Withdrawals),
		&s.BlobGasUsed, &s.ExcessBlobGas,
	)
}

func (ep *ExecutionPayload) Header(spec *common.Spec) *ExecutionPayloadHeader {
	return &ExecutionPayloadHeader{
		ParentHash:       ep.ParentHash,
		FeeRecipient:     ep.FeeRecipient,
		StateRoot:        ep.StateRoot,
		ReceiptsRoot:     ep.ReceiptsRoot,
		LogsBloom:        ep.LogsBloom,
		PrevRandao:       ep.PrevRandao,
		BlockNumber:      ep.BlockNumber,
		GasLimit:         ep.GasLimit,
		GasUsed:          ep.GasUsed,
		Timestamp:        ep.Timestamp,
		ExtraData:        ep.ExtraData,
		BaseFeePerGas:    ep.BaseFeePerGas,
		BlockHash:        ep.BlockHash,
		TransactionsRoot: ep.Transactions.HashTreeRoot(spec, tree.GetHashFn()),
		WithdrawalsRoot:  ep.Withdrawals.HashTreeRoot(spec, tree.GetHashFn()),
		BlobGasUsed:      ep.BlobGasUsed,
		ExcessBlobGas:    ep.ExcessBlobGas,
	}
}

func (ep *ExecutionPayload) GetWitdrawals() []common.Withdrawal {
	return ep.Withdrawals
}

type NewPayloadRequest struct {
	ExecutionPayload      *ExecutionPayload
	VersionedHashes       []common.Hash32
	ParentBeaconBlockRoot common.Root
}

type ExecutionEngine interface {
	DenebNotifyNewPayload(ctx context.Context, executionPayload *ExecutionPayload, parentBeaconBlockRoot common.Root) (valid bool, err error)
	DenebIsValidVersionedHashes(ctx context.Context, payload *ExecutionPayload, versionedHashes []common.Hash32) (bool, error)
	DenebIsValidBlockHash(ctx context.Context, payload *ExecutionPayload, parentBeaconBlockRoot common.Root) (bool, error)
}

func VerifyAndNotifyNewPayload(ctx context.Context, eng ExecutionEngine, newPayloadRequest *NewPayloadRequest) (bool, error) {
	executionPayload := newPayloadRequest.ExecutionPayload
	parentBeaconBlockRoot := newPayloadRequest.ParentBeaconBlockRoot

	// Modified in Deneb
	if ok, err := eng.DenebIsValidBlockHash(ctx, executionPayload, parentBeaconBlockRoot); err != nil {
		return false, fmt.Errorf("failed to check block hash: %w", err)
	} else if !ok {
		return false, nil
	}

	// New in Deneb
	if ok, err := eng.DenebIsValidVersionedHashes(ctx, executionPayload, newPayloadRequest.VersionedHashes); err != nil {
		return false, fmt.Errorf("failed to check blob versioned hashes: %w", err)
	} else if !ok {
		return false, nil
	}

	return eng.DenebNotifyNewPayload(ctx, executionPayload, parentBeaconBlockRoot)
}
