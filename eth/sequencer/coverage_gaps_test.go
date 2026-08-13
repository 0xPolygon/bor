package sequencer

import (
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// A tx whose fee cap is below the base fee cannot yield a tip; the derived
// effective price must not panic and still covers the base fee term.
func TestEffectiveGasPriceUnderpricedCap(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := types.LatestSignerForChainID(big.NewInt(1))
	to := common.Address{0x01}

	starved := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID: big.NewInt(1), To: &to, Gas: 21000,
		GasFeeCap: big.NewInt(10), GasTipCap: big.NewInt(5),
	})

	if got := effectiveGasPrice(starved, big.NewInt(30)); got.Cmp(big.NewInt(30)) != 0 {
		t.Fatalf("starved cap must degrade to the base fee, got %v", got)
	}
}

func TestBigEqualNilHandling(t *testing.T) {
	if !bigEqual(nil, nil) || bigEqual(nil, big.NewInt(1)) || bigEqual(big.NewInt(1), nil) {
		t.Fatal("nil comparisons must be exact")
	}
}

// Entry helpers must reject shapes they cannot represent instead of
// misclassifying them.
func TestEntryHelperEdges(t *testing.T) {
	empty := &pb.Entry{}

	if _, err := foldEntry(commitment.Head{}, empty); err == nil {
		t.Fatal("unknown entry kind must fail the fold")
	}

	if setEntryPrefix(empty, commitment.Head{}) {
		t.Fatal("unknown entry kind must not accept a prefix")
	}

	if entryPrefix(empty) != nil {
		t.Fatal("unknown entry kind carries no prefix")
	}

	if _, _, err := refoldEntry(commitment.Head{}, journalItem{entry: empty}); err == nil {
		t.Fatal("unknown entry kind must not refold")
	}

	open := openEntry(commitment.OpenContext{Number: 1, BaseFee: big.NewInt(1)}, commitment.Head{})
	record := recordEntry([]byte{0x01}, commitment.Head{})
	seal := sealEntry([]byte{0x0a}, commitment.Head{})

	if contentEqual(open, record) || contentEqual(record, seal) || contentEqual(seal, open) {
		t.Fatal("cross-kind entries must never be content-equal")
	}

	short := recordEntry([]byte{0x01}, commitment.Head{})
	short.GetRecord().Transactions = [][]byte{{0x01}, {0x02}}

	if contentEqual(record, short) {
		t.Fatal("records with different tx counts must differ")
	}

	if !contentEqual(seal, sealEntry([]byte{0x0a}, commitment.Head{0xff})) {
		t.Fatal("seal content ignores the prefix commitment")
	}

	if contentEqual(empty, empty) {
		t.Fatal("unknown kinds must never be content-equal")
	}
}

// A consumer pointed at a chain that is not deterministic yet keeps
// retrying without touching the store, and shuts down cleanly.
func TestConsumerRunWaitsForDeterminism(t *testing.T) {
	preRio := startExecHarnessBor(t, &params.BorConfig{
		RioBlock:      big.NewInt(1_000_000),
		BurntContract: map[string]string{"0": "0x000000000000000000000000000000000000dead"},
	})

	consumer, err := NewConsumer("127.0.0.1:1", preRio.chain)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumer.Start()
	time.Sleep(50 * time.Millisecond)
	consumer.Close()
}

// A dial target grpc cannot parse fails the session and retries rather
// than crashing the loop.
func TestConsumerSurvivesAnUndialableEndpoint(t *testing.T) {
	rio := startExecHarness(t)

	consumer, err := NewConsumer("bad scheme://\x00", rio.chain)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumer.Start()
	time.Sleep(50 * time.Millisecond)
	consumer.Close()
}
