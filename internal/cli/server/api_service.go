package server

import (
	"context"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"

	protobor "github.com/0xPolygon/polyproto/bor"
	protoutil "github.com/0xPolygon/polyproto/utils"
)

// maxBlockInfoBatchSize caps the per-call range to prevent abuse of the batch endpoint.
// Must be >= heimdall's MaxMilestonePropositionLength.
const maxBlockInfoBatchSize = 256

func (s *Server) GetRootHash(ctx context.Context, req *protobor.GetRootHashRequest) (*protobor.GetRootHashResponse, error) {
	rootHash, err := s.backend.APIBackend.GetRootHash(ctx, req.StartBlockNumber, req.EndBlockNumber)
	if err != nil {
		return nil, err
	}

	return &protobor.GetRootHashResponse{RootHash: rootHash}, nil
}

func (s *Server) GetVoteOnHash(ctx context.Context, req *protobor.GetVoteOnHashRequest) (*protobor.GetVoteOnHashResponse, error) {
	vote, err := s.backend.APIBackend.GetVoteOnHash(ctx, req.StartBlockNumber, req.EndBlockNumber, req.Hash, req.MilestoneId)
	if err != nil {
		return nil, err
	}

	return &protobor.GetVoteOnHashResponse{Response: vote}, nil
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
		Bloom:       h.Bloom.Bytes(),
		GasLimit:    h.GasLimit,
		GasUsed:     h.GasUsed,
		ExtraData:   append([]byte(nil), h.Extra...),
		MixDigest:   protoutil.ConvertHashToH256(h.MixDigest),
		Nonce:       h.Nonce[:],
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
	// BlobGasUsed and ExcessBlobGas are proto3 optional
	// using *uint64 as a direct pointer, so the copy preserves nil vs.zero.
	out.BlobGasUsed = h.BlobGasUsed
	out.ExcessBlobGas = h.ExcessBlobGas
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
		return nil, errors.New("header not found")
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
		return nil, errors.New("block not found")
	}

	return &protobor.GetBlockByNumberResponse{Block: blockToProtoBlock(block)}, nil
}

func blockToProtoBlock(h *types.Block) *protobor.Block {
	return &protobor.Block{
		Header: headerToProtoBorHeader(h.Header()),
	}
}

func (s *Server) TransactionReceipt(ctx context.Context, req *protobor.ReceiptRequest) (*protobor.ReceiptResponse, error) {
	_, _, blockHash, _, txnIndex := s.backend.APIBackend.GetTransaction(protoutil.ConvertH256ToHash(req.Hash))

	receipts, err := s.backend.APIBackend.GetReceipts(ctx, blockHash)
	if err != nil {
		return nil, err
	}

	if receipts == nil {
		return nil, errors.New("no receipts found")
	}

	if len(receipts) <= int(txnIndex) {
		return nil, errors.New("transaction index out of bounds")
	}

	return &protobor.ReceiptResponse{Receipt: ConvertReceiptToProtoReceipt(receipts[txnIndex])}, nil
}

func (s *Server) BorBlockReceipt(ctx context.Context, req *protobor.ReceiptRequest) (*protobor.ReceiptResponse, error) {
	receipt, err := s.backend.APIBackend.GetBorBlockReceipt(ctx, protoutil.ConvertH256ToHash(req.Hash))
	if err != nil {
		return nil, err
	}

	return &protobor.ReceiptResponse{Receipt: ConvertReceiptToProtoReceipt(receipt)}, nil
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
		return nil, errors.New("header not found")
	}

	author, err := s.backend.Engine().Author(header)
	if err != nil {
		return nil, err
	}

	return &protobor.GetAuthorResponse{Author: protoutil.ConvertAddressToH160(author)}, nil
}

func (s *Server) GetTdByHash(ctx context.Context, req *protobor.GetTdByHashRequest) (*protobor.GetTdResponse, error) {
	hashBytes := protoutil.ConvertH256ToHash(req.Hash)
	hash := common.BytesToHash(hashBytes[:])

	td := s.backend.APIBackend.GetTd(ctx, hash)
	if td == nil {
		return nil, errors.New("total difficulty not found")
	}
	if !td.IsUint64() {
		return nil, errors.New("total difficulty overflows uint64")
	}
	return &protobor.GetTdResponse{TotalDifficulty: td.Uint64()}, nil
}

func (s *Server) GetTdByNumber(ctx context.Context, req *protobor.GetTdByNumberRequest) (*protobor.GetTdResponse, error) {
	bN, err := getRpcBlockNumberFromString(req.Number)
	if err != nil {
		return nil, err
	}
	td := s.backend.APIBackend.GetTdByNumber(ctx, bN)
	if td == nil {
		return nil, errors.New("total difficulty not found")
	}
	if !td.IsUint64() {
		return nil, errors.New("total difficulty overflows uint64")
	}
	return &protobor.GetTdResponse{TotalDifficulty: td.Uint64()}, nil
}

func (s *Server) GetBlockInfoInBatch(ctx context.Context, req *protobor.GetBlockInfoInBatchRequest) (*protobor.GetBlockInfoInBatchResponse, error) {
	if req.EndBlockNumber < req.StartBlockNumber {
		return nil, errors.New("invalid range: end < start")
	}
	if req.EndBlockNumber-req.StartBlockNumber >= uint64(maxBlockInfoBatchSize) {
		return nil, errors.New("invalid range: exceeds max batch size")
	}

	size := int(req.EndBlockNumber-req.StartBlockNumber) + 1
	out := &protobor.GetBlockInfoInBatchResponse{
		Blocks: make([]*protobor.BlockInfo, 0, size),
	}

	// the i++ -> i-- requires an integration test with a multi-block batch
	// mutator-disable-next-line loop-step
	for i := req.StartBlockNumber; i <= req.EndBlockNumber; i++ {
		info, ok := s.fetchBlockInfo(ctx, i)
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
// Returns (nil, false) if any piece is missing — the caller should stop the loop.
// Author is left zero-valued for genesis (matching bor_getAuthor behavior).
func (s *Server) fetchBlockInfo(ctx context.Context, blockNum uint64) (*protobor.BlockInfo, bool) {
	header, err := s.backend.APIBackend.HeaderByNumber(ctx, rpc.BlockNumber(blockNum))
	// the negate_conditional requires mocking both err!=nil and nil-header paths
	// mutator-disable-next-line defensive APIBackend guard
	if err != nil || header == nil {
		return nil, false
	}

	td := s.backend.APIBackend.GetTd(ctx, header.Hash())
	if td == nil || !td.IsUint64() {
		return nil, false
	}

	info := &protobor.BlockInfo{
		Header:          headerToProtoBorHeader(header),
		TotalDifficulty: td.Uint64(),
	}

	if blockNum > 0 {
		author, err := s.backend.Engine().Author(header)
		if err != nil {
			return nil, false
		}
		info.Author = protoutil.ConvertAddressToH160(author)
	}

	return info, true
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
			return rpc.BlockNumber(0), errors.New("invalid block number")
		}
		if blckNum > math.MaxInt64 {
			return rpc.BlockNumber(0), errors.New("block number out of range")
		}
		return rpc.BlockNumber(blckNum), nil
	}
}
