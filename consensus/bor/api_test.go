package bor

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// newAPIForTest creates a Bor API backed by rawdbChain with signed headers so that
// snapshot() can recover the signer via ecrecover.
func newAPIForTest(t *testing.T) (*API, *rawdbChain, common.Address) {
	t.Helper()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	borCfg := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 2},
	}
	chainCfg := &params.ChainConfig{ChainID: big.NewInt(1), Bor: borCfg}

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: signerAddr, VotingPower: 1}}}
	_, b := newChainAndBorForTest(t, sp, borCfg, true, signerAddr, uint64(time.Now().Unix()))

	// Build a rawdbChain with signed headers
	db := rawdb.NewMemoryDatabase()

	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       uint64(time.Now().Unix()),
		GasLimit:   8_000_000,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65),
	}
	// Sign the genesis header
	sigHash := SealHash(genesis, borCfg)
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	copy(genesis.Extra[len(genesis.Extra)-65:], sig)

	rawdb.WriteHeader(db, genesis)
	rawdb.WriteCanonicalHash(db, genesis.Hash(), 0)
	rawdb.WriteHeadHeaderHash(db, genesis.Hash())
	rawdb.WriteTd(db, genesis.Hash(), 0, big.NewInt(1))

	// Build a few blocks
	prevHash := genesis.Hash()
	for i := uint64(1); i <= 4; i++ {
		h := &types.Header{
			ParentHash: prevHash,
			Number:     new(big.Int).SetUint64(i),
			Time:       genesis.Time + i*borCfg.Period["0"],
			GasLimit:   8_000_000,
			Difficulty: big.NewInt(1),
			Extra:      make([]byte, 32+65),
		}

		sigHash := SealHash(h, borCfg)
		sig, err := crypto.Sign(sigHash.Bytes(), privKey)
		require.NoError(t, err)
		copy(h.Extra[len(h.Extra)-65:], sig)

		rawdb.WriteHeader(db, h)
		rawdb.WriteCanonicalHash(db, h.Hash(), i)
		rawdb.WriteTd(db, h.Hash(), i, big.NewInt(int64(i+1)))
		prevHash = h.Hash()
	}

	current := rawdb.ReadHeader(db, prevHash, 4)
	require.NotNil(t, current)

	chain := newRawDBChain(db, chainCfg, current, nil, nil)

	api := &API{chain: chain, bor: b}

	return api, chain, signerAddr
}

func TestAPI_GetSnapshot_Nil(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	snap, err := api.GetSnapshot(nil)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, uint64(4), snap.Number)
}

func TestAPI_GetSnapshot_Latest(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	latest := rpc.LatestBlockNumber
	snap, err := api.GetSnapshot(&latest)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, uint64(4), snap.Number)
}

func TestAPI_GetSnapshot_SpecificBlock(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	blockNum := rpc.BlockNumber(2)
	snap, err := api.GetSnapshot(&blockNum)
	require.NoError(t, err)
	require.NotNil(t, snap)
}

func TestAPI_GetSnapshot_UnknownBlock(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	blockNum := rpc.BlockNumber(999)
	_, err := api.GetSnapshot(&blockNum)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetSnapshotAtHash_Valid(t *testing.T) {
	t.Parallel()
	api, chain, _ := newAPIForTest(t)

	hash := chain.CurrentHeader().Hash()
	snap, err := api.GetSnapshotAtHash(hash)
	require.NoError(t, err)
	require.NotNil(t, snap)
}

func TestAPI_GetSnapshotAtHash_Unknown(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	_, err := api.GetSnapshotAtHash(common.Hash{0xde, 0xad})
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetSigners_Nil(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	signers, err := api.GetSigners(nil)
	require.NoError(t, err)
	require.NotEmpty(t, signers)
	require.Contains(t, signers, signerAddr)
}

func TestAPI_GetSigners_Latest(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	latest := rpc.LatestBlockNumber
	signers, err := api.GetSigners(&latest)
	require.NoError(t, err)
	require.Contains(t, signers, signerAddr)
}

func TestAPI_GetSigners_SpecificBlock(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	blockNum := rpc.BlockNumber(2)
	signers, err := api.GetSigners(&blockNum)
	require.NoError(t, err)
	require.NotEmpty(t, signers)
}

func TestAPI_GetSigners_UnknownBlock(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	blockNum := rpc.BlockNumber(999)
	_, err := api.GetSigners(&blockNum)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetSignersAtHash_Valid(t *testing.T) {
	t.Parallel()
	api, chain, signerAddr := newAPIForTest(t)

	hash := chain.CurrentHeader().Hash()
	signers, err := api.GetSignersAtHash(hash)
	require.NoError(t, err)
	require.Contains(t, signers, signerAddr)
}

func TestAPI_GetSignersAtHash_Unknown(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	_, err := api.GetSignersAtHash(common.Hash{0xba, 0xad})
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetCurrentProposer(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	proposer, err := api.GetCurrentProposer()
	require.NoError(t, err)
	require.NotEqual(t, common.Address{}, proposer)
}

func TestAPI_GetCurrentValidators(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	validators, err := api.GetCurrentValidators()
	require.NoError(t, err)
	require.NotEmpty(t, validators)

	found := false
	for _, v := range validators {
		if v.Address == signerAddr {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestAPI_GetRootHash_StableWhenTipAdvancesPastEnd(t *testing.T) {
	t.Parallel()
	api, chain, _ := newAPIForTest(t)

	root1, err := api.GetRootHash(0, 3)
	require.NoError(t, err)
	require.NotEmpty(t, root1)

	chain.mu.Lock()
	readsAfterFirst := chain.readCount
	chain.mu.Unlock()

	head := chain.CurrentHeader()
	require.NotNil(t, head)

	next := &types.Header{
		ParentHash:  head.Hash(),
		Number:      new(big.Int).SetUint64(5),
		Time:        head.Time + 2,
		GasLimit:    head.GasLimit,
		Difficulty:  head.Difficulty,
		Extra:       make([]byte, 32+65),
		TxHash:      crypto.Keccak256Hash([]byte("tip-advance-tx")),
		ReceiptHash: crypto.Keccak256Hash([]byte("tip-advance-receipt")),
	}
	rawdb.WriteHeader(chain.db, next)
	rawdb.WriteCanonicalHash(chain.db, next.Hash(), 5)
	rawdb.WriteTd(chain.db, next.Hash(), 5, big.NewInt(6))
	rawdb.WriteHeadHeaderHash(chain.db, next.Hash())

	chain.mu.Lock()
	chain.current = next
	chain.mu.Unlock()

	root2, err := api.GetRootHash(0, 3)
	require.NoError(t, err)
	require.Equal(t, root1, root2)

	chain.mu.Lock()
	readsAfterSecond := chain.readCount
	chain.mu.Unlock()

	// Cache hit path should only read the end header (0-3 range must not be recomputed).
	require.Equal(t, readsAfterFirst+1, readsAfterSecond)
}

func newAPIWithUnknownCurrentHeader() *API {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1), Bor: &params.BorConfig{Sprint: map[string]uint64{"0": 64}}}
	chain := newRawDBChain(rawdb.NewMemoryDatabase(), cfg, nil, nil, nil)

	return &API{chain: chain, bor: &Bor{}}
}

func TestAPI_GetCurrentProposer_UnknownBlock(t *testing.T) {
	t.Parallel()

	api := newAPIWithUnknownCurrentHeader()

	proposer, err := api.GetCurrentProposer()
	require.ErrorIs(t, err, errUnknownBlock)
	require.Equal(t, common.Address{}, proposer)
}

func TestAPI_GetCurrentValidators_UnknownBlock(t *testing.T) {
	t.Parallel()

	api := newAPIWithUnknownCurrentHeader()

	validators, err := api.GetCurrentValidators()
	require.ErrorIs(t, err, errUnknownBlock)
	require.Empty(t, validators)
}

func TestAPI_GetAuthor_Nil(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	author, err := api.GetAuthor(nil)
	require.NoError(t, err)
	require.NotNil(t, author)
	require.Equal(t, signerAddr, *author)
}

func TestAPI_GetAuthor_ByNumber(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	blockNum := rpc.BlockNumber(2)
	bnOrHash := rpc.BlockNumberOrHashWithNumber(blockNum)
	author, err := api.GetAuthor(&bnOrHash)
	require.NoError(t, err)
	require.NotNil(t, author)
	require.Equal(t, signerAddr, *author)
}

func TestAPI_GetAuthor_ByLatestNumber(t *testing.T) {
	t.Parallel()
	api, _, signerAddr := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	author, err := api.GetAuthor(&bnOrHash)
	require.NoError(t, err)
	require.NotNil(t, author)
	require.Equal(t, signerAddr, *author)
}

func TestAPI_GetAuthor_ByHash(t *testing.T) {
	t.Parallel()
	api, chain, signerAddr := newAPIForTest(t)

	hash := chain.CurrentHeader().Hash()
	bnOrHash := rpc.BlockNumberOrHashWithHash(hash, false)
	author, err := api.GetAuthor(&bnOrHash)
	require.NoError(t, err)
	require.NotNil(t, author)
	require.Equal(t, signerAddr, *author)
}

func TestAPI_GetAuthor_UnknownNumber(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(999))
	_, err := api.GetAuthor(&bnOrHash)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetAuthor_UnknownHash(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithHash(common.Hash{0xde, 0xad}, false)
	_, err := api.GetAuthor(&bnOrHash)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetSnapshotProposer_Nil(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	proposer, err := api.GetSnapshotProposer(nil)
	require.NoError(t, err)
	require.NotEqual(t, common.Address{}, proposer)
}

func TestAPI_GetSnapshotProposer_ByNumber(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(2))
	proposer, err := api.GetSnapshotProposer(&bnOrHash)
	require.NoError(t, err)
	require.NotEqual(t, common.Address{}, proposer)
}

func TestAPI_GetSnapshotProposer_ByLatest(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	proposer, err := api.GetSnapshotProposer(&bnOrHash)
	require.NoError(t, err)
	require.NotEqual(t, common.Address{}, proposer)
}

func TestAPI_GetSnapshotProposer_ByHash(t *testing.T) {
	t.Parallel()
	api, chain, _ := newAPIForTest(t)

	hash := chain.CurrentHeader().Hash()
	bnOrHash := rpc.BlockNumberOrHashWithHash(hash, false)
	proposer, err := api.GetSnapshotProposer(&bnOrHash)
	require.NoError(t, err)
	require.NotEqual(t, common.Address{}, proposer)
}

func TestAPI_GetSnapshotProposer_Unknown(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(999))
	_, err := api.GetSnapshotProposer(&bnOrHash)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestAPI_GetSnapshotProposerSequence_Nil(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bs, err := api.GetSnapshotProposerSequence(nil)
	require.NoError(t, err)
	require.NotEmpty(t, bs.Signers)
	require.NotEqual(t, common.Address{}, bs.Author)
}

func TestAPI_GetSnapshotProposerSequence_ByNumber(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(2))
	bs, err := api.GetSnapshotProposerSequence(&bnOrHash)
	require.NoError(t, err)
	require.NotEmpty(t, bs.Signers)
}

func TestAPI_GetSnapshotProposerSequence_ByLatest(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	bs, err := api.GetSnapshotProposerSequence(&bnOrHash)
	require.NoError(t, err)
	require.NotEmpty(t, bs.Signers)
}

func TestAPI_GetSnapshotProposerSequence_ByHash(t *testing.T) {
	t.Parallel()
	api, chain, _ := newAPIForTest(t)

	hash := chain.CurrentHeader().Hash()
	bnOrHash := rpc.BlockNumberOrHashWithHash(hash, false)
	bs, err := api.GetSnapshotProposerSequence(&bnOrHash)
	require.NoError(t, err)
	require.NotEmpty(t, bs.Signers)
}

func TestAPI_GetSnapshotProposerSequence_Unknown(t *testing.T) {
	t.Parallel()
	api, _, _ := newAPIForTest(t)

	bnOrHash := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(999))
	_, err := api.GetSnapshotProposerSequence(&bnOrHash)
	require.Error(t, err)
	require.Equal(t, errUnknownBlock, err)
}

func TestRankMapDifficulties(t *testing.T) {
	t.Parallel()

	t.Run("empty map", func(t *testing.T) {
		result := rankMapDifficulties(map[common.Address]uint64{})
		require.Empty(t, result)
	})

	t.Run("single entry", func(t *testing.T) {
		addr := common.HexToAddress("0x1")
		result := rankMapDifficulties(map[common.Address]uint64{addr: 5})
		require.Len(t, result, 1)
		require.Equal(t, addr, result[0].Signer)
		require.Equal(t, uint64(5), result[0].Difficulty)
	})

	t.Run("sorted by difficulty descending", func(t *testing.T) {
		addr1 := common.HexToAddress("0x1")
		addr2 := common.HexToAddress("0x2")
		addr3 := common.HexToAddress("0x3")
		result := rankMapDifficulties(map[common.Address]uint64{
			addr1: 3,
			addr2: 7,
			addr3: 5,
		})
		require.Len(t, result, 3)
		require.Equal(t, uint64(7), result[0].Difficulty)
		require.Equal(t, uint64(5), result[1].Difficulty)
		require.Equal(t, uint64(3), result[2].Difficulty)
	})
}
