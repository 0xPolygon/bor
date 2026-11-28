package core

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestReportBlockSkipsOnlyProvenEngineShutdown(t *testing.T) {
	_, _, chain, err := newCanonical(ethash.NewFaker(), 0, true, rawdb.HashScheme)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})

	chain.reportBlock(block, nil, consensus.NewEngineStoppedError(context.Canceled))
	if rawdb.ReadBadBlock(chain.db, block.Hash()) != nil {
		t.Fatal("engine-owned shutdown abort was persisted as a bad block")
	}

	// A genuine validation failure remains reportable even after the global
	// insertion stop has been requested.
	chain.StopInsert()
	chain.reportBlock(block, nil, ErrReceiptRootMismatch)
	if rawdb.ReadBadBlock(chain.db, block.Hash()) == nil {
		t.Fatal("validation failure racing shutdown was not persisted")
	}
}
