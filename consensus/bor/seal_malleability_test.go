package bor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// These tests cover the block-seal signature malleability hardening. secp256k1
// recoverable signatures are malleable: for a seal (r, s, v) the alternate
// encoding (r, n-s, v^1) recovers the same signer but produces a different
// header hash, because Bor's SealHash excludes the 65-byte seal while
// Header.Hash() commits to it. ecrecover must reject the non-canonical (high-S)
// encoding so a signed header cannot be re-encoded into a hash-distinct sibling.

// sealTestBorConfig returns a minimal BorConfig sufficient for SealHash/ecrecover.
func sealTestBorConfig() *params.BorConfig {
	return &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 2},
	}
}

// newSealTestHeader builds a header sealed with a canonical low-S signature by key.
func newSealTestHeader(t *testing.T, key *ecdsa.PrivateKey, borCfg *params.BorConfig) *types.Header {
	t.Helper()
	h := &types.Header{
		Number:     big.NewInt(10),
		Difficulty: big.NewInt(1),
		GasLimit:   10_000_000,
		Time:       100,
	}
	signHeader(t, h, key, borCfg)
	return h
}

// applyHighSToSeal rewrites the S component of a 65-byte seal to its complement
// n-s (the malleable, non-canonical encoding). It does not touch the recovery id.
func applyHighSToSeal(seal []byte) {
	n := crypto.S256().Params().N
	sComplement := new(big.Int).Sub(n, new(big.Int).SetBytes(seal[32:64]))
	var buf [32]byte
	sComplement.FillBytes(buf[:])
	copy(seal[32:64], buf[:])
}

// malleateSeal applies the full malleability transform (r, s, v) -> (r, n-s, v^1)
// in place on a 65-byte seal. The result recovers the same signer as the input.
func malleateSeal(seal []byte) {
	applyHighSToSeal(seal)
	seal[64] ^= 1
}

func sealOf(header *types.Header) []byte {
	return header.Extra[len(header.Extra)-types.ExtraSealLength:]
}

// TestSealMalleabilityPrimitive documents the underlying primitive the fix
// defends against: the malleated seal keeps the signed payload (SealHash)
// identical, changes the header hash, and still recovers the same signer at the
// raw crypto layer.
func TestSealMalleabilityPrimitive(t *testing.T) {
	t.Parallel()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	borCfg := sealTestBorConfig()

	orig := newSealTestHeader(t, key, borCfg)
	mut := types.CopyHeader(orig)
	malleateSeal(sealOf(mut))

	// The signed payload is unchanged, but the canonical block hash is not.
	require.Equal(t, SealHash(orig, borCfg), SealHash(mut, borCfg), "SealHash must be unchanged by seal mutation")
	require.NotEqual(t, orig.Hash(), mut.Hash(), "header hash must change with the seal bytes")

	// At the raw crypto layer, the malleated seal recovers the same public key.
	pubOrig, err := crypto.Ecrecover(SealHash(orig, borCfg).Bytes(), sealOf(orig))
	require.NoError(t, err)
	pubMut, err := crypto.Ecrecover(SealHash(mut, borCfg).Bytes(), sealOf(mut))
	require.NoError(t, err)
	require.Equal(t, pubOrig, pubMut, "malleated seal must recover the same signer")
}

// TestEcrecoverRejectsNonCanonicalSeal is the core regression: Bor's ecrecover
// accepts the canonical low-S seal but rejects the malleated high-S encoding,
// and neither half of the transform on its own reproduces the authorized signer.
func TestEcrecoverRejectsNonCanonicalSeal(t *testing.T) {
	t.Parallel()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	borCfg := sealTestBorConfig()

	cache, err := lru.NewARC(16)
	require.NoError(t, err)

	orig := newSealTestHeader(t, key, borCfg)

	// The genuine low-S seal recovers the authorized signer.
	got, err := ecrecover(orig, cache, borCfg)
	require.NoError(t, err)
	require.Equal(t, addr, got)

	// The full malleability transform is rejected before recovery.
	mut := types.CopyHeader(orig)
	malleateSeal(sealOf(mut))
	_, err = ecrecover(mut, cache, borCfg)
	require.ErrorIs(t, err, errNonCanonicalSeal, "malleated (r, n-s, v^1) seal must be rejected")

	// Negative control: high-S alone (no recovery-id flip) is also rejected.
	onlyHighS := types.CopyHeader(orig)
	applyHighSToSeal(sealOf(onlyHighS))
	_, err = ecrecover(onlyHighS, cache, borCfg)
	require.ErrorIs(t, err, errNonCanonicalSeal, "high-S seal must be rejected")

	// Negative control: flipping only the recovery id keeps low-S (so it passes
	// the canonical check) but recovers a different, unauthorized signer.
	onlyFlipV := types.CopyHeader(orig)
	sealOf(onlyFlipV)[64] ^= 1
	gotV, errV := ecrecover(onlyFlipV, cache, borCfg)
	require.NoError(t, errV, "low-S seal with flipped recovery id must still recover a signer")
	require.NotEqual(t, addr, gotV, "recovery-id flip alone must not reproduce the authorized signer")

	// Out-of-range recovery id must be rejected before attempting recovery.
	invalidV := types.CopyHeader(orig)
	sealOf(invalidV)[64] = 2
	_, err = ecrecover(invalidV, cache, borCfg)
	require.ErrorIs(t, err, errNonCanonicalSeal, "out-of-range recovery id must be rejected")
}

// TestVerifyHeaderRejectsNonCanonicalSeal exercises the real peer-block
// verification entrypoint (verifyHeader -> verifyCascadingFields -> verifySeal
// -> ecrecover), the same path InsertChain runs for every imported header. The
// genuine header verifies; the malleated sibling is rejected.
func TestVerifyHeaderRejectsNonCanonicalSeal(t *testing.T) {
	t.Parallel()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	chain, engine := makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
		opts.rioBlock = big.NewInt(0)
	})(t)
	defer chain.Stop()

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	borCfg := chain.Config().Bor

	orig := newSignedStandardTestHeader(t, genesis, privKey, borCfg, func(opts *headerOptions) {
		opts.uncleHash = uncleHash
		opts.mixDigest = common.Hash{}
	})
	require.NoError(t, engine.verifyHeader(chain.HeaderChain(), orig, nil), "genuine header must verify")

	mut := types.CopyHeader(orig)
	malleateSeal(sealOf(mut))

	require.ErrorIs(t, engine.verifyHeader(chain.HeaderChain(), mut, nil), errNonCanonicalSeal,
		"malleated sibling must be rejected by header verification")
}
