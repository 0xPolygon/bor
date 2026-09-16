package txpool

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type acknowledgingSubPool struct {
	SubPool
	batch    []*types.Transaction
	callback func([]common.Hash)
}

func (p *acknowledgingSubPool) RebroadcastAcknowledgement(txs []*types.Transaction) func([]common.Hash) {
	p.batch = txs
	return p.callback
}

func TestRebroadcastAcknowledgementOptionalSubPools(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1})
	hash := tx.Hash()
	var counts [2]int
	first := &acknowledgingSubPool{callback: func(hashes []common.Hash) {
		if len(hashes) != 1 || hashes[0] != hash {
			t.Fatal("first subpool received the wrong hashes")
		}
		counts[0]++
	}}
	second := &acknowledgingSubPool{callback: func(hashes []common.Hash) {
		if len(hashes) != 1 || hashes[0] != hash {
			t.Fatal("second subpool received the wrong hashes")
		}
		counts[1]++
	}}
	pool := &TxPool{subpools: []SubPool{first, &plainTestSubPool{}, &acknowledgingSubPool{}, second}}
	acknowledge := pool.RebroadcastAcknowledgement([]*types.Transaction{tx})
	for _, subpool := range []*acknowledgingSubPool{first, second} {
		if len(subpool.batch) != 1 || subpool.batch[0] != tx {
			t.Fatal("callback must capture the original transaction identity")
		}
	}
	if counts != [2]int{} {
		t.Fatal("creating the callback must not account for a send")
	}
	acknowledge([]common.Hash{hash})
	if counts != [2]int{1, 1} {
		t.Fatalf("each capable subpool must receive acceptance: %v", counts)
	}
}

func TestRebroadcastAcknowledgementNilPool(t *testing.T) {
	var pool *TxPool
	if pool.RebroadcastAcknowledgement(nil) != nil {
		t.Fatal("nil pool should have no acknowledgment")
	}
}
