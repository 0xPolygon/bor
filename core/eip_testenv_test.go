// Copyright 2026 The go-ethereum Authors
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

package core

import (
	"crypto/ecdsa"
	"maps"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// The test environment below is upstream's, from core/eip7928_test.go, which
// bor does not carry yet (block access lists, POS-3737). Only the parts the
// EIP-8037 tests use are here; remove this file when core/eip7928_test.go is
// restored.

const txGasNewAccount = 250_000

type balTestEnv struct {
	cfg    *params.ChainConfig
	signer types.Signer
	key    *ecdsa.PrivateKey
	from   common.Address
	gspec  *Genesis
}

// newBALTestEnv builds an Amsterdam chain config, funds a sender and pre-deploys
// the EIP-7928 system contracts. Extra accounts can be merged into Alloc.
func newBALTestEnv(extra types.GenesisAlloc) *balTestEnv {
	cfg := amsterdamTestChainConfig()
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	from := crypto.PubkeyToAddress(key.PublicKey)

	alloc := types.GenesisAlloc{
		from:                             {Balance: newGwei(1_000_000_000)},
		params.BeaconRootsAddress:        {Nonce: 1, Code: params.BeaconRootsCode, Balance: common.Big0},
		params.HistoryStorageAddress:     {Nonce: 1, Code: params.HistoryStorageCode, Balance: common.Big0},
		params.WithdrawalQueueAddress:    {Nonce: 1, Code: params.WithdrawalQueueCode, Balance: common.Big0},
		params.ConsolidationQueueAddress: {Nonce: 1, Code: params.ConsolidationQueueCode, Balance: common.Big0},
		params.BuilderDepositAddress:     {Nonce: 1, Code: params.BuilderDepositCode, Balance: common.Big0},
		params.BuilderExitAddress:        {Nonce: 1, Code: params.BuilderExitCode, Balance: common.Big0},
	}
	maps.Copy(alloc, extra)
	return &balTestEnv{
		cfg:    cfg,
		signer: types.LatestSigner(cfg),
		key:    key,
		from:   from,
		gspec:  &Genesis{Config: cfg, Alloc: alloc},
	}
}

func (e *balTestEnv) tx(nonce uint64, to *common.Address, value *big.Int, gas uint64, tipGwei int64, data []byte) *types.Transaction {
	return types.MustSignNewTx(e.key, e.signer, &types.DynamicFeeTx{
		ChainID:   e.cfg.ChainID,
		Nonce:     nonce,
		To:        to,
		Value:     value,
		Gas:       gas,
		GasFeeCap: newGwei(10),
		GasTipCap: newGwei(tipGwei),
		Data:      data,
	})
}
