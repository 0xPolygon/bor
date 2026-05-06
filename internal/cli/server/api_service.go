package server

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	borconsensus "github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	protobor "github.com/0xPolygon/polyproto/bor"
	commonproto "github.com/0xPolygon/polyproto/common"
	protoutil "github.com/0xPolygon/polyproto/utils"
)

// protoHashToCommon safely converts a proto H256 to a common.Hash. Returns
// codes.InvalidArgument if the outer pointer or any inner H128 sub-message is nil
func protoHashToCommon(h *commonproto.H256) (common.Hash, error) {
	if h == nil || h.Hi == nil || h.Lo == nil {
		return common.Hash{}, status.Error(codes.InvalidArgument, "hash is required with non-nil Hi/Lo")
	}
	return common.Hash(protoutil.ConvertH256ToHash(h)), nil
}

// maxBlockInfoBatchSize caps the per-call range to prevent abuse of the batch endpoint.
// Must be >= heimdall's MaxMilestonePropositionLength.
const maxBlockInfoBatchSize = 256

func (s *Server) GetRootHash(ctx context.Context, req *protobor.GetRootHashRequest) (*protobor.GetRootHashResponse, error) {
	// Pre-validate the request so malformed inputs surface as
	// codes.InvalidArgument instead of being passed to the backend (which
	// would return them as the default codes.Unknown).
	if req.EndBlockNumber < req.StartBlockNumber {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end < start")
	}
	if req.EndBlockNumber > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end exceeds max int64")
	}

	rootHash, err := s.backend.APIBackend.GetRootHash(ctx, req.StartBlockNumber, req.EndBlockNumber)
	if err != nil {
		return nil, mapBorAPIError(err)
	}

	return &protobor.GetRootHashResponse{RootHash: rootHash}, nil
}

func (s *Server) GetVoteOnHash(ctx context.Context, req *protobor.GetVoteOnHashRequest) (*protobor.GetVoteOnHashResponse, error) {
	if req.EndBlockNumber < req.StartBlockNumber {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end < start")
	}
	if req.EndBlockNumber > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end exceeds max int64")
	}
	if req.Hash == "" {
		return nil, status.Error(codes.InvalidArgument, "hash is required")
	}
	if req.MilestoneId == "" {
		return nil, status.Error(codes.InvalidArgument, "milestone id is required")
	}

	vote, err := s.backend.APIBackend.GetVoteOnHash(ctx, req.StartBlockNumber, req.EndBlockNumber, req.Hash, req.MilestoneId)
	if err != nil {
		return nil, mapBorAPIError(err)
	}

	return &protobor.GetVoteOnHashResponse{Response: vote}, nil
}

// mapBorAPIError translates the most common backend error sentinels emitted by
// EthAPIBackend.GetRootHash / GetVoteOnHash into canonical gRPC status codes.
// Unknown errors are wrapped as codes.Internal instead of default codes.Unknown,
// so clients can distinguish what went wrong.
func mapBorAPIError(err error) error {
	if err == nil {
		return nil
	}
	// Already a gRPC status (e.g., from a nested handler) — pass through.
	if _, ok := status.FromError(err); ok {
		return err
	}
	// Struct-typed errors come first: their messages contain dynamic %d
	// fields, so equality matching against the message string can never hit
	// them. Walk wrapped chains via errors.As.
	var invalidRange *valset.InvalidStartEndBlockError
	if errors.As(err, &invalidRange) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	var rangeTooLong *borconsensus.MaxCheckpointLengthExceededError
	if errors.As(err, &rangeTooLong) {
		return status.Error(codes.OutOfRange, err.Error())
	}
	msg := err.Error()
	switch {
	case msg == "Only available in Bor engine":
		// Server is misconfigured / not running bor consensus.
		return status.Error(codes.FailedPrecondition, msg)
	case msg == "unknown block":
		return status.Error(codes.NotFound, msg)
	case msg == "end block number is out of safe range":
		return status.Error(codes.OutOfRange, msg)
	case msg == "failed to get end block", msg == "failed to get tip confirmation block":
		return status.Error(codes.NotFound, msg)
	case strings.HasPrefix(msg, "hash mismatch"):
		return status.Error(codes.InvalidArgument, msg)
	// Reorg during checkpoint root computation: transient/retriable. Aborted
	// signals "the operation was cancelled, typically due to a concurrency
	// issue" — the heimdall caller can branch on this and retry once the
	// chain settles instead of treating it as a server bug.
	case msg == "reorg occurred while computing checkpoint root":
		return status.Error(codes.Aborted, msg)
	// Non-contiguous header range = ancient pruning / DB inconsistency.
	// DataLoss conveys "unrecoverable state on the server" without the
	// "this is a bug" implication of Internal.
	case msg == "non-contiguous headers in checkpoint range":
		return status.Error(codes.DataLoss, msg)
	default:
		return status.Error(codes.Internal, msg)
	}
}

func headerToProtoBorHeader(h *types.Header) *protobor.Header {
	out := &protobor.Header{
		Number:      h.Number.Uint64(),
		ParentHash:  protoutil.ConvertHashToH256(h.ParentHash),
		Time:        h.Time,
		UncleHash:   protoutil.ConvertHashToH256(h.UncleHash),
		Coinbase:    protoutil.ConvertAddressToH160(h.Coinbase),
		StateRoot:   protoutil.ConvertHashToH256(h.Root),
		TxRoot:      protoutil.ConvertHashToH256(h.TxHash),
		ReceiptRoot: protoutil.ConvertHashToH256(h.ReceiptHash),
		Bloom:       append([]byte(nil), h.Bloom.Bytes()...),
		GasLimit:    h.GasLimit,
		GasUsed:     h.GasUsed,
		ExtraData:   append([]byte(nil), h.Extra...),
		MixDigest:   protoutil.ConvertHashToH256(h.MixDigest),
		Nonce:       append([]byte(nil), h.Nonce[:]...),
	}
	if h.Difficulty != nil {
		out.Difficulty = h.Difficulty.Bytes()
	}
	if h.BaseFee != nil {
		out.BaseFee = h.BaseFee.Bytes()
	}
	if h.WithdrawalsHash != nil {
		out.WithdrawalsHash = protoutil.ConvertHashToH256(*h.WithdrawalsHash)
	}
	// BlobGasUsed and ExcessBlobGas are proto3 optional. *uint64 preserves
	// nil-vs-zero; we copy through a fresh variable so the proto doesn't alias
	// the source header's pointers (consistent with how ExtraData/Bloom/Nonce
	// are handled above).
	if h.BlobGasUsed != nil {
		v := *h.BlobGasUsed
		out.BlobGasUsed = &v
	}
	if h.ExcessBlobGas != nil {
		v := *h.ExcessBlobGas
		out.ExcessBlobGas = &v
	}
	if h.ParentBeaconRoot != nil {
		out.ParentBeaconBlockRoot = protoutil.ConvertHashToH256(*h.ParentBeaconRoot)
	}
	if h.RequestsHash != nil {
		out.RequestsHash = protoutil.ConvertHashToH256(*h.RequestsHash)
	}
	return out
}

func (s *Server) HeaderByNumber(ctx context.Context, req *protobor.GetHeaderByNumberRequest) (*protobor.GetHeaderByNumberResponse, error) {
	bN, err := getRpcBlockNumberFromString(req.Number)
	if err != nil {
		return nil, err
	}
	header, err := s.backend.APIBackend.HeaderByNumber(ctx, bN)
	if err != nil {
		return nil, err
	}

	if header == nil {
		return nil, status.Error(codes.NotFound, "header not found")
	}

	return &protobor.GetHeaderByNumberResponse{Header: headerToProtoBorHeader(header)}, nil
}

func (s *Server) BlockByNumber(ctx context.Context, req *protobor.GetBlockByNumberRequest) (*protobor.GetBlockByNumberResponse, error) {
	bN, err := getRpcBlockNumberFromString(req.Number)
	if err != nil {
		return nil, err
	}
	block, err := s.backend.APIBackend.BlockByNumber(ctx, bN)
	if err != nil {
		return nil, err
	}

	if block == nil {
		return nil, status.Error(codes.NotFound, "block not found")
	}

	return &protobor.GetBlockByNumberResponse{Block: blockToProtoBlock(block)}, nil
}

func blockToProtoBlock(h *types.Block) *protobor.Block {
	return &protobor.Block{
		Header: headerToProtoBorHeader(h.Header()),
	}
}

func (s *Server) TransactionReceipt(ctx context.Context, req *protobor.ReceiptRequest) (*protobor.ReceiptResponse, error) {
	txHash, err := protoHashToCommon(req.Hash)
	if err != nil {
		return nil, err
	}
	found, _, blockHash, _, txnIndex := s.backend.APIBackend.GetTransaction(txHash)
	if !found {
		return nil, status.Error(codes.NotFound, "transaction not found")
	}

	receipts, err := s.backend.APIBackend.GetReceipts(ctx, blockHash)
	if err != nil {
		return nil, err
	}

	if receipts == nil {
		return nil, status.Error(codes.NotFound, "no receipts found")
	}

	if len(receipts) <= int(txnIndex) {
		return nil, status.Error(codes.OutOfRange, "transaction index out of bounds")
	}

	pr, err := ConvertReceiptToProtoReceipt(receipts[txnIndex])
	if err != nil {
		return nil, err
	}
	return &protobor.ReceiptResponse{Receipt: pr}, nil
}

func (s *Server) BorBlockReceipt(ctx context.Context, req *protobor.ReceiptRequest) (*protobor.ReceiptResponse, error) {
	txHash, err := protoHashToCommon(req.Hash)
	if err != nil {
		return nil, err
	}
	receipt, err := s.backend.APIBackend.GetBorBlockReceipt(ctx, txHash)
	// EthAPIBackend returns ethereum.NotFound when the receipt is missing;
	// other backends may return (nil, nil). Map both to codes.NotFound so
	// callers can branch on canonical gRPC codes instead of seeing Unknown
	// or panicking on a nil dereference inside the converter.
	if errors.Is(err, ethereum.NotFound) {
		return nil, status.Error(codes.NotFound, "bor block receipt not found")
	}
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, status.Error(codes.NotFound, "bor block receipt not found")
	}

	pr, err := ConvertReceiptToProtoReceipt(receipt)
	if err != nil {
		return nil, err
	}
	return &protobor.ReceiptResponse{Receipt: pr}, nil
}

func (s *Server) GetAuthor(ctx context.Context, req *protobor.GetAuthorRequest) (*protobor.GetAuthorResponse, error) {
	bN, err := getRpcBlockNumberFromString(req.Number)
	if err != nil {
		return nil, err
	}

	header, err := s.backend.APIBackend.HeaderByNumber(ctx, bN)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, status.Error(codes.NotFound, "header not found")
	}

	author, err := s.backend.Engine().Author(header)
	if err != nil {
		return nil, err
	}

	return &protobor.GetAuthorResponse{Author: protoutil.ConvertAddressToH160(author)}, nil
}

func (s *Server) GetTdByHash(ctx context.Context, req *protobor.GetTdByHashRequest) (*protobor.GetTdResponse, error) {
	hash, err := protoHashToCommon(req.Hash)
	if err != nil {
		return nil, err
	}

	td := s.backend.APIBackend.GetTd(ctx, hash)
	if td == nil {
		return nil, status.Error(codes.NotFound, "total difficulty not found")
	}
	if !td.IsUint64() {
		return nil, status.Error(codes.OutOfRange, "total difficulty overflows uint64")
	}
	return &protobor.GetTdResponse{TotalDifficulty: td.Uint64()}, nil
}

func (s *Server) GetTdByNumber(ctx context.Context, req *protobor.GetTdByNumberRequest) (*protobor.GetTdResponse, error) {
	bN, err := getRpcBlockNumberFromString(req.Number)
	if err != nil {
		return nil, err
	}
	// Resolve the block number (including special tags) to a concrete header
	// before looking up TD by hash.
	header, err := s.backend.APIBackend.HeaderByNumber(ctx, bN)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, status.Error(codes.NotFound, "header not found")
	}
	td := s.backend.APIBackend.GetTd(ctx, header.Hash())
	if td == nil {
		return nil, status.Error(codes.NotFound, "total difficulty not found")
	}
	if !td.IsUint64() {
		return nil, status.Error(codes.OutOfRange, "total difficulty overflows uint64")
	}
	return &protobor.GetTdResponse{TotalDifficulty: td.Uint64()}, nil
}

func (s *Server) GetBlockInfoInBatch(ctx context.Context, req *protobor.GetBlockInfoInBatchRequest) (*protobor.GetBlockInfoInBatchResponse, error) {
	// Input validation returns codes.InvalidArgument so clients can
	// distinguish malformed requests from internal failures
	if req.EndBlockNumber < req.StartBlockNumber {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end < start")
	}
	if req.EndBlockNumber-req.StartBlockNumber >= uint64(maxBlockInfoBatchSize) {
		return nil, status.Error(codes.InvalidArgument, "invalid range: exceeds max batch size")
	}
	if req.EndBlockNumber > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "invalid range: end exceeds max int64")
	}

	count := req.EndBlockNumber - req.StartBlockNumber + 1
	out := &protobor.GetBlockInfoInBatchResponse{
		Blocks: make([]*protobor.BlockInfo, 0, count),
	}

	for j := uint64(0); j < count; j++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, ok, err := s.fetchBlockInfo(ctx, req.StartBlockNumber+j)
		if err != nil {
			return nil, err
		}
		// this requires APIBackend mock returning a missing block mid-range
		// mutator-disable-next-line gap-stop semantics
		if !ok {
			// Match HTTP batch semantics: stop at the first gap, return what we have.
			break
		}
		out.Blocks = append(out.Blocks, info)
	}

	return out, nil
}

// fetchBlockInfo loads header, total difficulty, and author for blockNum.
// Return semantics:
//   - (info, true, nil): success, append to the batch
//   - (nil, false, nil): legitimate gap (header not yet on chain / TD missing); caller should break the loop and return the partial prefix, matching HTTP side
//   - (nil, false, err): real failure (backend error, ecrecover failure, overflow); caller should propagate the error
//
// Author is left as a nil *H160 for genesis; callers must nil-check before decoding.
func (s *Server) fetchBlockInfo(ctx context.Context, blockNum uint64) (*protobor.BlockInfo, bool, error) {
	if blockNum > math.MaxInt64 {
		return nil, false, status.Error(codes.InvalidArgument, "block number exceeds max int64")
	}
	header, err := s.backend.APIBackend.HeaderByNumber(ctx, rpc.BlockNumber(blockNum))
	if err != nil {
		return nil, false, err
	}
	if header == nil {
		return nil, false, nil
	}

	td := s.backend.APIBackend.GetTd(ctx, header.Hash())
	if td == nil {
		// TD not yet indexed — treat as a gap
		return nil, false, nil
	}
	if !td.IsUint64() {
		return nil, false, status.Error(codes.OutOfRange, "total difficulty overflows uint64")
	}

	info := &protobor.BlockInfo{
		Header:          headerToProtoBorHeader(header),
		TotalDifficulty: td.Uint64(),
	}

	if blockNum > 0 {
		author, err := s.backend.Engine().Author(header)
		if err != nil {
			// Author() failure on a validated header indicates a corrupted
			// seal — propagate the error.
			return nil, false, err
		}
		info.Author = protoutil.ConvertAddressToH160(author)
	}

	return info, true, nil
}

func getRpcBlockNumberFromString(blockNumber string) (rpc.BlockNumber, error) {
	switch blockNumber {
	case "latest":
		return rpc.LatestBlockNumber, nil
	case "earliest":
		return rpc.EarliestBlockNumber, nil
	case "pending":
		return rpc.PendingBlockNumber, nil
	case "finalized":
		return rpc.FinalizedBlockNumber, nil
	case "safe":
		return rpc.SafeBlockNumber, nil
	default:
		blckNum, err := hexutil.DecodeUint64(blockNumber)
		if err != nil {
			return rpc.BlockNumber(0), status.Error(codes.InvalidArgument, "invalid block number")
		}
		if blckNum > math.MaxInt64 {
			return rpc.BlockNumber(0), status.Error(codes.InvalidArgument, "block number out of range")
		}
		return rpc.BlockNumber(blckNum), nil
	}
}
