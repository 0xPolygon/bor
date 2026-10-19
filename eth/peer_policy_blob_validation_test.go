package eth

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestPeerPolicyBlobValidationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blob    types.BlobTx
		pooled  bool
		wantErr string
	}{
		{name: "broadcast", wantErr: "disallowed broadcast blob transaction"},
		{name: "missing sidecar", pooled: true, wantErr: "received sidecar-less blob transaction"},
		{name: "commitment count", pooled: true, blob: types.BlobTx{BlobHashes: []common.Hash{{1}}, Sidecar: &types.BlobTxSidecar{}}, wantErr: "invalid number"},
		{name: "commitment hash", pooled: true, blob: types.BlobTx{BlobHashes: []common.Hash{{1}}, Sidecar: &types.BlobTxSidecar{Commitments: []kzg4844.Commitment{{1}}}}, wantErr: "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			txs := []*types.Transaction{types.NewTx(&tc.blob)}
			var packet eth.Packet = (*eth.TransactionsPacket)(&txs)
			if tc.pooled {
				packet = &eth.PooledTransactionsPacket{RequestId: 42, PooledTransactionsResponse: txs}
			}
			id, err := handlePolicyTransaction(t, h, packet)
			if err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected rejection: %v", err)
			}
			got := h.peerPolicy.Snapshot(id)
			if got.Risk != 60 || got.Windows[peerpolicy.InvalidTransaction.String()] != 1 {
				t.Fatalf("missing invalid transaction evidence: %+v", got)
			}
		})
	}
}
