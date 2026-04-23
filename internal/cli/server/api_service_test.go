package server

import (
	"context"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	protobor "github.com/0xPolygon/polyproto/bor"
	commonproto "github.com/0xPolygon/polyproto/common"
	protoutil "github.com/0xPolygon/polyproto/utils"
)

// Compile-time check that Server implements the proto interface.
var _ protobor.BorApiServer = (*Server)(nil)

func TestGetAuthor_InvalidBlockNumber(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetAuthor(context.Background(), &protobor.GetAuthorRequest{Number: "not-a-number"})
	if err == nil {
		t.Fatalf("expected error on invalid block number, got nil")
	}
}

func TestHeaderToProtoBorHeader_RoundTrip_Cancun(t *testing.T) {
	src := &types.Header{
		ParentHash:       common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
		UncleHash:        types.EmptyUncleHash,
		Coinbase:         common.HexToAddress("0x0123456789abcdef0123456789abcdef01234567"),
		Root:             common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444"),
		TxHash:           common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555"),
		ReceiptHash:      common.HexToHash("0x6666666666666666666666666666666666666666666666666666666666666666"),
		Bloom:            types.Bloom{0x01, 0x02, 0x03},
		Difficulty:       big.NewInt(17),
		Number:           big.NewInt(1234567),
		GasLimit:         30_000_000,
		GasUsed:          21_000,
		Time:             1_700_000_000,
		Extra:            []byte{0xde, 0xad, 0xbe, 0xef},
		MixDigest:        common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777"),
		Nonce:            types.BlockNonce{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8},
		BaseFee:          big.NewInt(1_000_000_000),
		WithdrawalsHash:  new(common.HexToHash("0xaabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")),
		BlobGasUsed:      new(uint64(131072)),
		ExcessBlobGas:    new(uint64(262144)),
		ParentBeaconRoot: new(common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")),
		RequestsHash:     new(common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")),
	}

	pb := headerToProtoBorHeader(src)
	got := protoHeaderToEthHeaderLocal(t, pb)
	if got.Hash() != src.Hash() {
		t.Fatalf("hash mismatch: got %x want %x", got.Hash(), src.Hash())
	}
}

// TestHeaderToProtoBorHeader_RoundTrip_CancunZeroBlobGas guards against the nil vs. zero trap on blobGasUsed / excessBlobGas.
// A header with BlobGasUsed=&0 must round-trip to BlobGasUsed=&0 (not nil),
// otherwise Hash() changes and milestone propositions break.
func TestHeaderToProtoBorHeader_RoundTrip_CancunZeroBlobGas(t *testing.T) {
	zeroHash := common.Hash{}

	src := &types.Header{
		ParentHash:       common.HexToHash("0x01"),
		UncleHash:        types.EmptyUncleHash,
		Coinbase:         common.HexToAddress("0x0123456789abcdef0123456789abcdef01234567"),
		Root:             common.HexToHash("0x02"),
		TxHash:           common.HexToHash("0x03"),
		ReceiptHash:      common.HexToHash("0x04"),
		Difficulty:       big.NewInt(1),
		Number:           big.NewInt(100),
		GasLimit:         30_000_000,
		Time:             1_700_000_000,
		BaseFee:          big.NewInt(1_000_000_000),
		BlobGasUsed:      new(uint64(0)),
		ExcessBlobGas:    new(uint64(0)),
		ParentBeaconRoot: &zeroHash,
	}

	pb := headerToProtoBorHeader(src)
	got := protoHeaderToEthHeaderLocal(t, pb)
	if got.Hash() != src.Hash() {
		t.Fatalf("hash mismatch (zero-blob-gas): got %x want %x", got.Hash(), src.Hash())
	}
	if got.BlobGasUsed == nil {
		t.Fatalf("BlobGasUsed must round-trip to &0, not nil")
	}
	if got.ExcessBlobGas == nil {
		t.Fatalf("ExcessBlobGas must round-trip to &0, not nil")
	}
}

func TestHeaderToProtoBorHeader_RoundTrip_PreShanghai(t *testing.T) {
	src := &types.Header{
		ParentHash:  common.HexToHash("0x01"),
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.HexToAddress("0x0123456789abcdef0123456789abcdef01234567"),
		Root:        common.HexToHash("0x02"),
		TxHash:      common.HexToHash("0x03"),
		ReceiptHash: common.HexToHash("0x04"),
		Difficulty:  big.NewInt(1),
		Number:      big.NewInt(100),
		GasLimit:    30_000_000,
		GasUsed:     0,
		Time:        1_600_000_000,
		Extra:       []byte{},
		MixDigest:   common.Hash{},
		Nonce:       types.BlockNonce{},
	}
	pb := headerToProtoBorHeader(src)
	got := protoHeaderToEthHeaderLocal(t, pb)
	if got.Hash() != src.Hash() {
		t.Fatalf("hash mismatch pre-shanghai: got %x want %x", got.Hash(), src.Hash())
	}
}

func TestGetTdByNumber_InvalidNumber(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetTdByNumber(context.Background(), &protobor.GetTdByNumberRequest{Number: "not-a-number"})
	if err == nil {
		t.Fatalf("expected error on invalid block number")
	}
}

func TestGetBlockInfoInBatch_RangeBound(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
		StartBlockNumber: 0, EndBlockNumber: 2_000, // exceeds maxBlockInfoBatchSize
	})
	if err == nil {
		t.Fatalf("expected error when batch range exceeds limit")
	}
}

func TestGetBlockInfoInBatch_StartGreaterThanEnd(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
		StartBlockNumber: 10, EndBlockNumber: 5,
	})
	if err == nil {
		t.Fatalf("expected error when start > end")
	}
}

// TestGetBlockInfoInBatch_RangeOverflow guards against the uint64 overflow that allowed `end - start + 1` to wrap
// past the batch-size limit and drive the server into a non-terminating loop.
func TestGetBlockInfoInBatch_RangeOverflow(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
		StartBlockNumber: 0, EndBlockNumber: math.MaxUint64,
	})
	if err == nil {
		t.Fatalf("expected error on overflowing range, got nil (would non-terminate)")
	}
}

// TestGetBlockInfoInBatch_NearMaxUint64 guards against a narrow range near MaxUint64.
func TestGetBlockInfoInBatch_NearMaxUint64(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
		StartBlockNumber: math.MaxUint64 - 3,
		EndBlockNumber:   math.MaxUint64,
	})
	if err == nil {
		t.Fatalf("expected error on near-MaxUint64 range, got nil (would wrap and walk chain)")
	}
	if !strings.Contains(err.Error(), "exceeds max int64") {
		t.Fatalf("expected int64-overflow error, got: %v", err)
	}
}

// TestGetBlockInfoInBatch_SizeGateBoundary tests the boundaries.
// A range of exactly maxBlockInfoBatchSize must pass the size gate,
// and a range of maxBlockInfoBatchSize+1 must fail it with the specific error.
// We distinguish the size gate from downstream failures by checking the error
// message. A panic from the nil backend is fine for the at-limit case since
// we only care that the gate itself didn't reject.
func TestGetBlockInfoInBatch_SizeGateBoundary(t *testing.T) {
	t.Run("at limit passes the size gate", func(t *testing.T) {
		srv := &Server{}
		// size = end - start + 1 = 256 = maxBlockInfoBatchSize (allowed)
		var err error
		func() {
			defer func() {
				// Backend is nil; handler panics calling APIBackend.HeaderByNumber.
				// A panic here means we *passed* the gate, which is what we want.
				_ = recover()
			}()
			_, err = srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
				StartBlockNumber: 0, EndBlockNumber: uint64(maxBlockInfoBatchSize - 1),
			})
		}()
		if err != nil && strings.Contains(err.Error(), "exceeds max batch size") {
			t.Fatalf("size gate rejected a size-of-%d request; should accept: %v", maxBlockInfoBatchSize, err)
		}
	})

	t.Run("just over limit fails the size gate", func(t *testing.T) {
		srv := &Server{}
		// size = end - start + 1 = 257 > maxBlockInfoBatchSize
		_, err := srv.GetBlockInfoInBatch(context.Background(), &protobor.GetBlockInfoInBatchRequest{
			StartBlockNumber: 0, EndBlockNumber: uint64(maxBlockInfoBatchSize),
		})
		if err == nil {
			t.Fatalf("expected size-gate error for range size %d (>%d), got nil", maxBlockInfoBatchSize+1, maxBlockInfoBatchSize)
		}
		if !strings.Contains(err.Error(), "exceeds max batch size") {
			t.Fatalf("expected size-gate error message, got: %v", err)
		}
	})
}

// protoHeaderToEthHeaderLocal is the test-side inverse of headerToProtoBorHeader.
// It mirrors the decoder that heimdall's x/bor/grpc package will ship.
func protoHeaderToEthHeaderLocal(t *testing.T, p *protobor.Header) *types.Header {
	t.Helper()
	if p == nil {
		return nil
	}
	convH := func(h *commonproto.H256) common.Hash {
		if h == nil {
			return common.Hash{}
		}
		b := protoutil.ConvertH256ToHash(h)
		return common.BytesToHash(b[:])
	}
	convA := func(a *commonproto.H160) common.Address {
		if a == nil {
			return common.Address{}
		}
		arr := protoutil.ConvertH160toAddress(a)
		return common.BytesToAddress(arr[:])
	}

	h := &types.Header{
		ParentHash:  convH(p.ParentHash),
		UncleHash:   convH(p.UncleHash),
		Coinbase:    convA(p.Coinbase),
		Root:        convH(p.StateRoot),
		TxHash:      convH(p.TxRoot),
		ReceiptHash: convH(p.ReceiptRoot),
		Difficulty:  new(big.Int).SetBytes(p.Difficulty),
		Number:      new(big.Int).SetUint64(p.Number),
		GasLimit:    p.GasLimit,
		GasUsed:     p.GasUsed,
		Time:        p.Time,
		Extra:       append([]byte(nil), p.ExtraData...),
		MixDigest:   convH(p.MixDigest),
	}
	h.Bloom.SetBytes(p.Bloom)
	copy(h.Nonce[:], p.Nonce)

	if len(p.BaseFee) > 0 {
		h.BaseFee = new(big.Int).SetBytes(p.BaseFee)
	}
	if p.WithdrawalsHash != nil {
		h.WithdrawalsHash = new(convH(p.WithdrawalsHash))
	}
	// BlobGasUsed / ExcessBlobGas are proto3 `optional` on the wire → *uint64 on the Go side.
	h.BlobGasUsed = p.BlobGasUsed
	h.ExcessBlobGas = p.ExcessBlobGas
	if p.ParentBeaconBlockRoot != nil {
		h.ParentBeaconRoot = new(convH(p.ParentBeaconBlockRoot))
	}
	if p.RequestsHash != nil {
		h.RequestsHash = new(convH(p.RequestsHash))
	}
	return h
}
