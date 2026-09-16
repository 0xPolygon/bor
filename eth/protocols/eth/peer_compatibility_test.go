package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type legacyTransactionSender interface {
	AsyncSendTransactions([]common.Hash)
	AsyncSendPooledTransactionHashes([]common.Hash)
}

var _ legacyTransactionSender = (*Peer)(nil)

func TestTransactionSendAPICompatibility(t *testing.T) {
	for _, announce := range []bool{false, true} {
		queue := make(chan []common.Hash, 1)
		peer := &Peer{term: make(chan struct{}), knownTxs: newKnownCache(10), txBroadcast: queue, txAnnounce: queue}
		var send func([]common.Hash) = peer.AsyncSendTransactions
		if announce {
			send = peer.AsyncSendPooledTransactionHashes
		}
		hash := common.Hash{1}
		send([]common.Hash{hash})
		select {
		case hashes := <-queue:
			if len(hashes) != 1 || hashes[0] != hash || !peer.KnownTransaction(hash) {
				t.Fatal("legacy send must queue and mark its hashes")
			}
		default:
			t.Fatal("legacy send did not queue its hashes")
		}
	}
}
