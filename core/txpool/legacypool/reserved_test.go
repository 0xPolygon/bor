// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package legacypool

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

func reservedChainConfig(reserved common.Address) *params.ChainConfig {
	cfg := *params.BorUnittestChainConfig // London active at 0
	bor := *cfg.Bor
	bor.ReservedBlockspaceBlock = big.NewInt(0)
	bor.ReservedClients = []params.ReservedClient{
		{Addresses: []common.Address{reserved}, QuotaGas: 30_000_000},
	}
	cfg.Bor = &bor
	return &cfg
}

func setupReservedPool(reserved common.Address) *LegacyPool {
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	cfg := reservedChainConfig(reserved)
	bc := newTestBlockChain(cfg, 10_000_000, statedb, new(event.Feed))
	pool := New(testTxPoolConfig, bc)
	if err := pool.Init(testTxPoolConfig.PriceLimit, bc.CurrentBlock(), newReserver()); err != nil {
		panic(err)
	}
	<-pool.initDoneCh
	// A realistic positive tip floor (PIP-35 ~25 gwei). Reserved senders must
	// bypass it; everyone else is held to it.
	pool.SetGasTip(big.NewInt(30_000_000_000))
	return pool
}

func zeroFeeTx(t *testing.T, cfg *params.ChainConfig, key *ecdsa.PrivateKey, nonce uint64, to common.Address) *types.Transaction {
	t.Helper()
	tx, err := types.SignNewTx(key, types.LatestSigner(cfg), &types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		Gas:       100_000,
		To:        &to,
		Value:     big.NewInt(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestReservedZeroFeeTxAdmittedAndPending verifies the core ticket-D behaviour:
// a zero-fee tx from a reserved sender is admitted past the tip floor, kept in
// the pool, and surfaced by Pending even under a high miner MinTip — whereas the
// same tx from a non-reserved sender is rejected at admission.
func TestReservedZeroFeeTxAdmittedAndPending(t *testing.T) {
	t.Parallel()

	reservedKey, _ := crypto.GenerateKey()
	reservedAddr := crypto.PubkeyToAddress(reservedKey.PublicKey)
	pool := setupReservedPool(reservedAddr)
	defer pool.Close()

	otherKey, _ := crypto.GenerateKey()
	otherAddr := crypto.PubkeyToAddress(otherKey.PublicKey)

	testAddBalance(pool, reservedAddr, big.NewInt(1_000_000))
	testAddBalance(pool, otherAddr, big.NewInt(1_000_000))

	cfg := pool.chainconfig

	// Reserved sender: zero-fee tx must be admitted.
	rtx := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0x42})
	if err := pool.Add([]*types.Transaction{rtx}, true)[0]; err != nil {
		t.Fatalf("reserved zero-fee tx rejected: %v", err)
	}
	if pool.Get(rtx.Hash()) == nil {
		t.Fatal("reserved zero-fee tx not kept in pool")
	}

	// Non-reserved sender: identical zero-fee tx must be rejected at the floor.
	otx := zeroFeeTx(t, cfg, otherKey, 0, common.Address{0x42})
	if err := pool.Add([]*types.Transaction{otx}, true)[0]; err == nil {
		t.Fatal("non-reserved zero-fee tx should be rejected at the tip floor")
	}

	// The reserved tx must survive a high miner tip filter so it reaches the miner.
	pending := pool.Pending(txpool.PendingFilter{
		MinTip:  uint256.NewInt(30_000_000_000),
		BaseFee: uint256.NewInt(1_000_000_000),
	}, nil)
	if got := pending[reservedAddr]; len(got) != 1 || got[0].Hash != rtx.Hash() {
		t.Fatalf("reserved zero-fee tx missing from Pending under high MinTip: %v", got)
	}
}

// TestReservedZeroFeeReplacement verifies replacement-by-arrival: a reserved
// sender can replace a stuck zero-fee tx with another zero-fee tx at the same
// nonce, which the standard price-bump rule would reject.
func TestReservedZeroFeeReplacement(t *testing.T) {
	t.Parallel()

	reservedKey, _ := crypto.GenerateKey()
	reservedAddr := crypto.PubkeyToAddress(reservedKey.PublicKey)
	pool := setupReservedPool(reservedAddr)
	defer pool.Close()

	testAddBalance(pool, reservedAddr, big.NewInt(1_000_000))
	cfg := pool.chainconfig

	first := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0xaa})
	if err := pool.Add([]*types.Transaction{first}, true)[0]; err != nil {
		t.Fatalf("first reserved tx rejected: %v", err)
	}

	// Same nonce, different recipient, still zero fee — must replace by arrival.
	second := zeroFeeTx(t, cfg, reservedKey, 0, common.Address{0xbb})
	if err := pool.Add([]*types.Transaction{second}, true)[0]; err != nil {
		t.Fatalf("reserved zero-fee replacement rejected: %v", err)
	}

	if pool.Get(second.Hash()) == nil {
		t.Fatal("replacement tx not in pool")
	}
	if pool.Get(first.Hash()) != nil {
		t.Fatal("replaced tx should have been evicted")
	}
}
