package sequencer

import (
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	"github.com/0xPolygon/sequence-store-proto/devstore"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

const testChainID = 1337

func startDevstore(t *testing.T) (*devstore.Store, string) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	store := devstore.New(testChainID)
	srv := grpc.NewServer()
	pb.RegisterPublisherServiceServer(srv, store)
	pb.RegisterConsumerServiceServer(srv, store)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	return store, lis.Addr().String()
}

func testTx(t *testing.T, nonce uint64) *types.Transaction {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(testChainID)), &types.DynamicFeeTx{
		ChainID:   big.NewInt(testChainID),
		Nonce:     nonce,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(30_000_000_000),
		Gas:       21000,
		To:        &common.Address{0x01},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	return tx
}

// The publisher's folds must match the store's: after a full block lifecycle
// the store head equals an independently computed chain.
func TestPublisherLifecycle(t *testing.T) {
	store, addr := startDevstore(t)

	publisher, err := NewPublisher(addr, testChainID, 0)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	defer publisher.Close()

	parent := common.Hash{0xef}
	baseFee := big.NewInt(25_000_000_000)
	tx1, tx2 := testTx(t, 0), testTx(t, 1)
	sealed := &types.Header{
		ParentHash: parent,
		Number:     big.NewInt(101),
		GasLimit:   45_000_000,
		Time:       1_750_000_000,
		BaseFee:    baseFee,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 97),
	}

	publisher.OpenBlock(101, sealed.Time, parent, sealed.GasLimit, baseFee)
	publisher.PublishTx(tx1)
	publisher.PublishTx(tx2)
	publisher.SealBlock(sealed)

	// Independently recompute the expected head.
	head, err := commitment.FoldOpen(commitment.Seed(testChainID), commitment.OpenContext{
		Number:     101,
		Timestamp:  sealed.Time,
		ParentHash: parent,
		GasLimit:   sealed.GasLimit,
		BaseFee:    baseFee,
	})
	if err != nil {
		t.Fatalf("FoldOpen: %v", err)
	}

	for _, tx := range []*types.Transaction{tx1, tx2} {
		raw, merr := tx.MarshalBinary()
		if merr != nil {
			t.Fatalf("marshal: %v", merr)
		}

		head = commitment.FoldTx(head, raw)
	}

	rawHeader, err := rlp.EncodeToBytes(sealed)
	if err != nil {
		t.Fatalf("rlp: %v", err)
	}

	want := commitment.FoldSeal(head, commitment.SealedHash(rawHeader))

	// The publisher is asynchronous; wait for the store head to converge.
	deadline := time.Now().Add(5 * time.Second)
	for store.Head() != want {
		if time.Now().After(deadline) {
			t.Fatalf("store head %x never reached %x", store.Head(), want)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if publisher.failed.Load() {
		t.Fatal("publisher entered failed state")
	}
}

func TestIndexLifecycle(t *testing.T) {
	ix := NewIndex()
	tx := testTx(t, 0)
	receipt := &types.Receipt{
		TxHash:      tx.Hash(),
		BlockNumber: big.NewInt(101),
		Logs:        []*types.Log{{}},
	}

	ix.Add(tx, receipt)

	got, _, ok := ix.Lookup(tx.Hash())
	if !ok || got.BlockHash != (common.Hash{}) {
		t.Fatalf("pre-seal lookup: ok=%v blockHash=%v", ok, got.BlockHash)
	}

	sealedHash := common.Hash{0xaa}
	ix.Seal(101, sealedHash)

	if got, _, _ := ix.Lookup(tx.Hash()); got.BlockHash != sealedHash || got.Logs[0].BlockHash != sealedHash {
		t.Fatal("seal did not fill block hash into receipt and logs")
	}

	ix.EvictThrough(101)

	if _, _, ok := ix.Lookup(tx.Hash()); ok {
		t.Fatal("evicted entry still served")
	}
}

func TestIndexClearFrom(t *testing.T) {
	ix := NewIndex()
	txLow, txHigh := testTx(t, 0), testTx(t, 1)

	ix.Add(txLow, &types.Receipt{TxHash: txLow.Hash(), BlockNumber: big.NewInt(100)})
	ix.Add(txHigh, &types.Receipt{TxHash: txHigh.Hash(), BlockNumber: big.NewInt(101)})
	ix.ClearFrom(101)

	if _, _, ok := ix.Lookup(txLow.Hash()); !ok {
		t.Fatal("entry below the re-anchor was dropped")
	}

	if _, _, ok := ix.Lookup(txHigh.Hash()); ok {
		t.Fatal("voided entry still served")
	}
}
