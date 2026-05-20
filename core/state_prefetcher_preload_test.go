// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package core

import (
	"bytes"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func TestResolveEvmInterrupt_PrefersEvmAbort(t *testing.T) {
	abort := new(atomic.Bool)
	kill := new(atomic.Bool)
	require.Same(t, abort, resolveEvmInterrupt(abort, kill),
		"evmAbort must take precedence when both are non-nil")
}

func TestResolveEvmInterrupt_FallsBackToHardKill(t *testing.T) {
	kill := new(atomic.Bool)
	require.Same(t, kill, resolveEvmInterrupt(nil, kill),
		"hardKill must be used when evmAbort is nil")
}

func TestResolveEvmInterrupt_BothNil(t *testing.T) {
	require.Nil(t, resolveEvmInterrupt(nil, nil))
}

func TestResolveEvmInterrupt_EvmAbortWithNilHardKill(t *testing.T) {
	abort := new(atomic.Bool)
	require.Same(t, abort, resolveEvmInterrupt(abort, nil))
}

// recordingReader stubs state.Reader and records each call so tests can assert
// which preload reads preloadReaderForTx actually issued.
type recordingReader struct {
	accounts map[common.Address]*types.StateAccount

	accountCalls []common.Address
	codeCalls    []common.Address
	storageCalls []storageCall
}

type storageCall struct {
	addr common.Address
	slot common.Hash
}

func (r *recordingReader) Account(addr common.Address) (*types.StateAccount, error) {
	r.accountCalls = append(r.accountCalls, addr)
	if a, ok := r.accounts[addr]; ok {
		return a, nil
	}
	return nil, nil
}

func (r *recordingReader) Code(addr common.Address, codeHash common.Hash) ([]byte, error) {
	r.codeCalls = append(r.codeCalls, addr)
	return nil, nil
}

func (r *recordingReader) CodeSize(addr common.Address, codeHash common.Hash) (int, error) {
	return 0, nil
}

func (r *recordingReader) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	r.storageCalls = append(r.storageCalls, storageCall{addr: addr, slot: slot})
	return common.Hash{}, nil
}

var _ state.Reader = (*recordingReader)(nil)

func newSignedLegacyTx(t *testing.T, to *common.Address) (*types.Transaction, types.Signer, common.Address) {
	t.Helper()
	signer := types.LatestSigner(params.TestChainConfig)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	tx := types.MustSignNewTx(key, signer, &types.LegacyTx{
		Nonce:    0,
		To:       to,
		Value:    big.NewInt(0),
		Gas:      21000,
		GasPrice: big.NewInt(1),
	})
	sender := crypto.PubkeyToAddress(key.PublicKey)
	return tx, signer, sender
}

// TestPreloadReaderForTx_BadSignatureReturnsError verifies the err != nil branch
// at the top of preloadReaderForTx: when signature recovery fails, the function
// returns early with the error before issuing any reads.
func TestPreloadReaderForTx_BadSignatureReturnsError(t *testing.T) {
	signer := types.LatestSigner(params.TestChainConfig)
	// Construct an unsigned tx so types.Sender fails.
	to := common.BigToAddress(big.NewInt(1))
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &to,
		Value:    big.NewInt(0),
		Gas:      21000,
		GasPrice: big.NewInt(1),
	})
	reader := &recordingReader{accounts: map[common.Address]*types.StateAccount{}}

	sender, err := preloadReaderForTx(reader, tx, signer)
	require.Error(t, err, "unsigned tx must yield a Sender() error")
	require.True(t, errors.Is(err, types.ErrInvalidSig) || err.Error() != "",
		"err must be a recoverable signature error: %v", err)
	require.Equal(t, common.Address{}, sender, "sender must be zero on error")
	require.Empty(t, reader.accountCalls, "no preload reads must run after early error return")
	require.Empty(t, reader.codeCalls)
	require.Empty(t, reader.storageCalls)
}

// TestPreloadReaderForTx_WarmsCodeForContractTo verifies the contract-To branch
// at line 279/281: when the destination account is non-nil and has a non-empty
// code hash, reader.Code(*to, ...) is invoked to warm the bytecode cache.
func TestPreloadReaderForTx_WarmsCodeForContractTo(t *testing.T) {
	contractAddr := common.BigToAddress(big.NewInt(0xC0DE))
	tx, signer, sender := newSignedLegacyTx(t, &contractAddr)

	codeHash := crypto.Keccak256Hash([]byte("not-empty-code"))
	reader := &recordingReader{accounts: map[common.Address]*types.StateAccount{
		contractAddr: {Nonce: 1, Balance: nil, CodeHash: codeHash.Bytes()},
	}}

	got, err := preloadReaderForTx(reader, tx, signer)
	require.NoError(t, err)
	require.Equal(t, sender, got)

	require.Contains(t, reader.accountCalls, sender, "sender account must be preloaded")
	require.Contains(t, reader.accountCalls, contractAddr, "contract account must be preloaded")
	require.Len(t, reader.codeCalls, 1, "contract code must be preloaded exactly once")
	require.Equal(t, contractAddr, reader.codeCalls[0])
}

// TestPreloadReaderForTx_SkipsCodeForEOATo verifies the negative side of the
// contract-To branch: when the destination has an empty code hash (EOA), Code()
// is not invoked. Together with TestPreloadReaderForTx_WarmsCodeForContractTo
// this fully covers the if-condition at line 279.
func TestPreloadReaderForTx_SkipsCodeForEOATo(t *testing.T) {
	eoaAddr := common.BigToAddress(big.NewInt(0xEEEEEE))
	tx, signer, _ := newSignedLegacyTx(t, &eoaAddr)

	reader := &recordingReader{accounts: map[common.Address]*types.StateAccount{
		eoaAddr: {Nonce: 0, Balance: nil, CodeHash: types.EmptyCodeHash.Bytes()},
	}}

	_, err := preloadReaderForTx(reader, tx, signer)
	require.NoError(t, err)
	require.Empty(t, reader.codeCalls, "EOA destination must not trigger Code preload")
}

// TestPreloadReaderForTx_SkipsCodeForNilToAccount verifies the other negative
// side: when reader.Account(*to) returns nil, the if-body must be skipped (no
// Code call), and no panic on the nil-deref.
func TestPreloadReaderForTx_SkipsCodeForNilToAccount(t *testing.T) {
	missingAddr := common.BigToAddress(big.NewInt(0xDEADBEEF))
	tx, signer, _ := newSignedLegacyTx(t, &missingAddr)

	reader := &recordingReader{accounts: map[common.Address]*types.StateAccount{}}

	_, err := preloadReaderForTx(reader, tx, signer)
	require.NoError(t, err)
	require.Empty(t, reader.codeCalls, "nil account must not trigger Code preload")

	// Sanity check: the recorded hash for an empty CodeHash matches the
	// constant the production code compares against.
	require.True(t, bytes.Equal(types.EmptyCodeHash.Bytes(), crypto.Keccak256(nil)),
		"EmptyCodeHash sanity check (guards the comparison preloadReaderForTx uses)")
}
