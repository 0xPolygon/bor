//go:build integration
// +build integration

package bor

import (
	"math/big"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

// malleateSealedBlock returns a sibling of b whose seal is re-encoded via the
// secp256k1 malleability transform (r, s, v) -> (r, n-s, v^1). The sibling has
// the same body, state, parent and total difficulty as b, recovers the same
// producer, but has a different block hash.
func malleateSealedBlock(b *types.Block) *types.Block {
	header := types.CopyHeader(b.Header())
	seal := header.Extra[len(header.Extra)-extraSeal:]

	n := crypto.S256().Params().N
	sComplement := new(big.Int).Sub(n, new(big.Int).SetBytes(seal[32:64]))
	var buf [32]byte
	sComplement.FillBytes(buf[:])
	copy(seal[32:64], buf[:])
	seal[64] ^= 1

	return b.WithSeal(header)
}

// TestNonCanonicalSealRejectedOnImport is the end-to-end regression for block
// seal malleability. It builds a genuine, producer-signed block, imports it
// through the real chain.InsertChain pipeline (the same path a NewBlockMsg
// takes), then derives the malleated sibling and asserts that InsertChain
// rejects it and never persists it. Before the fix the sibling verified and was
// stored under its own hash, enabling a hash-distinct same-height sibling.
func TestNonCanonicalSealRejectedOnImport(t *testing.T) {
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	_, raiseErr := fdlimit.Raise(2048)
	require.NoError(t, raiseErr)

	init := buildEthereumInstance(t, rawdb.NewMemoryDatabase())
	chain := init.ethereum.BlockChain()
	engine := init.ethereum.Engine()
	_bor := engine.(*bor.Bor)
	defer _bor.Close()

	span0 := createMockSpan(addr, chain.Config().ChainID.String())
	res := loadSpanFromFile(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	h := createMockHeimdall(ctrl, &span0, res)
	h.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*clerk.EventRecordWithTime{getSampleEventRecord(t)}, nil).AnyTimes()
	h.EXPECT().GetLatestSpan(gomock.Any()).Return(&span0, nil).AnyTimes()
	_bor.SetHeimdallClient(h)

	vals := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet).Validators
	_bor.SetSpanner(getMockedSpanner(t, vals))

	// Build a genuine, in-turn block at height 1 signed by the span-0 validator.
	setDifficulty := func(header *types.Header) {
		if IsSprintStart(header.Number.Uint64()) {
			header.Difficulty = big.NewInt(int64(len(vals)))
		}
	}
	parent := init.genesis.ToBlock()
	original := buildNextBlock(t, _bor, chain, parent, nil, init.genesis.Config.Bor, nil, vals, false, []modifyHeaderFunc{setDifficulty}, nil)

	// The genuine block imports and becomes the canonical head.
	_, err := chain.InsertChain([]*types.Block{original}, false)
	require.NoError(t, err)
	require.Equal(t, original.Hash(), chain.CurrentBlock().Hash())

	// Derive the malleated sibling.
	sibling := malleateSealedBlock(original)
	require.Equal(t, original.NumberU64(), sibling.NumberU64(), "sibling is at the same height")
	require.Equal(t, original.ParentHash(), sibling.ParentHash(), "sibling has the same parent")
	require.NotEqual(t, original.Hash(), sibling.Hash(), "sibling has a distinct block hash")

	// The sibling must be rejected on import and must never be stored.
	_, err = chain.InsertChain([]*types.Block{sibling}, false)
	require.Error(t, err, "malleated sibling must be rejected on import")
	require.ErrorContains(t, err, "non-canonical seal")
	require.Nil(t, chain.GetBlockByHash(sibling.Hash()), "malleated sibling must not be persisted")
	require.Equal(t, original.Hash(), chain.CurrentBlock().Hash(), "canonical head must be unchanged")
}
