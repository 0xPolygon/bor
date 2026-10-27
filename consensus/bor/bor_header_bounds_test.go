package bor

import (
	"crypto/ecdsa"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const boundsGenesisTime = 1600000000

func newBoundsTestChain(t *testing.T, genesisBaseFee *big.Int) (*core.BlockChain, *Bor, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: crypto.PubkeyToAddress(key.PublicKey), VotingPower: 1}}}
	borCfg := &params.BorConfig{
		Sprint:      map[string]uint64{"0": 64},
		Period:      map[string]uint64{"0": 2},
		LisovoBlock: big.NewInt(0),
	}
	chain, b := newChainAndBorForTestWithConfig(t, sp, newAllForksChainConfig(borCfg), false, common.Address{}, boundsGenesisTime,
		func(g *core.Genesis) { g.BaseFee = genesisBaseFee })
	t.Cleanup(chain.Stop)

	return chain, b, key
}

func sealedChildHeader(t *testing.T, b *Bor, key *ecdsa.PrivateKey, parent *types.Header, number, baseFee *big.Int) *types.Header {
	t.Helper()

	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     number,
		Time:       parent.Time + 2,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, types.ExtraVanityLength+types.ExtraSealLength),
		UncleHash:  types.EmptyUncleHash,
		GasLimit:   parent.GasLimit,
		BaseFee:    baseFee,
	}
	sig, err := crypto.Sign(SealHash(header, b.config).Bytes(), key)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], sig)

	return header
}

func TestVerifyHeaderBaseFeeBitLen(t *testing.T) {
	t.Parallel()

	two256 := new(big.Int).Lsh(big.NewInt(1), 256)
	maxUint256 := new(big.Int).Sub(two256, big.NewInt(1))
	// Both child fees below are within 5% of this parent.
	parentBaseFee := new(big.Int).Div(new(big.Int).Mul(two256, big.NewInt(100)), big.NewInt(104))

	tests := []struct {
		name    string
		baseFee *big.Int
		wantErr bool
	}{
		{"256 bits accepted", maxUint256, false},
		{"257 bits rejected", two256, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain, b, key := newBoundsTestChain(t, parentBaseFee)
			genesis := chain.HeaderChain().GetHeaderByNumber(0)
			require.NotNil(t, genesis)

			header := sealedChildHeader(t, b, key, genesis, big.NewInt(1), tt.baseFee)
			err := b.VerifyHeader(chain.HeaderChain(), header)
			if tt.wantErr {
				require.ErrorContains(t, err, "bitlen 257 exceeds 256")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVerifyHeaderNumberBounds(t *testing.T) {
	t.Parallel()

	two64 := new(big.Int).Lsh(big.NewInt(1), 64)

	tests := []struct {
		name    string
		number  *big.Int
		parent  bool
		wantErr error
	}{
		{"2^64 rejected", two64, true, consensus.ErrInvalidNumber},
		{"2^64+1 rejected", new(big.Int).Add(two64, big.NewInt(1)), true, consensus.ErrInvalidNumber},
		// Fits in 64 bits, so it reaches the ancestor lookup as before.
		{"2^64-1 reaches ancestor check", new(big.Int).SetUint64(math.MaxUint64), false, consensus.ErrUnknownAncestor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain, b, key := newBoundsTestChain(t, big.NewInt(params.InitialBaseFee))
			genesis := chain.HeaderChain().GetHeaderByNumber(0)
			require.NotNil(t, genesis)

			parent := genesis
			if !tt.parent {
				parent = &types.Header{Number: big.NewInt(1), Time: genesis.Time, Extra: []byte("unknown")}
			}
			header := sealedChildHeader(t, b, key, parent, tt.number, genesis.BaseFee)
			require.ErrorIs(t, b.VerifyHeader(chain.HeaderChain(), header), tt.wantErr)
		})
	}
}

func TestVerifySealNumberBounds(t *testing.T) {
	t.Parallel()

	chain, b, key := newBoundsTestChain(t, big.NewInt(params.InitialBaseFee))
	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)

	number := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	header := sealedChildHeader(t, b, key, genesis, number, genesis.BaseFee)
	require.ErrorIs(t, b.VerifySeal(chain.HeaderChain(), header), consensus.ErrInvalidNumber)

	header.Number = nil
	require.ErrorIs(t, b.VerifySeal(chain.HeaderChain(), header), consensus.ErrInvalidNumber)
}
