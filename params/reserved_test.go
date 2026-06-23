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

package params

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestIsReservedBlockspace(t *testing.T) {
	t.Parallel()

	cfg := &BorConfig{ReservedBlockspaceBlock: big.NewInt(100)}
	if cfg.IsReservedBlockspace(big.NewInt(99)) {
		t.Error("should not be active at N-1")
	}
	if !cfg.IsReservedBlockspace(big.NewInt(100)) {
		t.Error("should be active at N")
	}
	if !cfg.IsReservedBlockspace(big.NewInt(101)) {
		t.Error("should be active at N+1")
	}

	// nil fork block = never active.
	none := &BorConfig{}
	if none.IsReservedBlockspace(big.NewInt(1_000_000)) {
		t.Error("nil ReservedBlockspaceBlock should never be active")
	}
}

func TestReservedClientClassifier(t *testing.T) {
	t.Parallel()

	a := common.HexToAddress("0x00000000000000000000000000000000000000Aa")
	b := common.HexToAddress("0x00000000000000000000000000000000000000Bb")
	c := common.HexToAddress("0x00000000000000000000000000000000000000Cc")
	other := common.HexToAddress("0x00000000000000000000000000000000000000Ff")

	cfg := &BorConfig{
		ReservedClients: []ReservedClient{
			{Addresses: []common.Address{a, b}, QuotaGas: 20_000_000},
			{Addresses: []common.Address{c}, QuotaGas: 10_000_000},
		},
	}

	for _, addr := range []common.Address{a, b, c} {
		if !cfg.IsReservedSender(addr) {
			t.Errorf("%s should be reserved", addr.Hex())
		}
	}
	if cfg.IsReservedSender(other) {
		t.Error("non-whitelisted address must not be reserved")
	}

	// QuotaOf returns the owning client's quota; addresses of the same client share it.
	if got := cfg.ReservedQuotaOf(a); got != 20_000_000 {
		t.Errorf("quota of a: got %d want 20000000", got)
	}
	if got := cfg.ReservedQuotaOf(b); got != 20_000_000 {
		t.Errorf("quota of b (same client as a): got %d want 20000000", got)
	}
	if got := cfg.ReservedQuotaOf(c); got != 10_000_000 {
		t.Errorf("quota of c: got %d want 10000000", got)
	}
	if got := cfg.ReservedQuotaOf(other); got != 0 {
		t.Errorf("quota of non-reserved: got %d want 0", got)
	}

	if got := cfg.ReservedCapacity(); got != 30_000_000 {
		t.Errorf("capacity: got %d want 30000000", got)
	}

	// Empty config classifies nothing and has zero capacity.
	empty := &BorConfig{}
	if empty.IsReservedSender(a) || empty.ReservedQuotaOf(a) != 0 || empty.ReservedCapacity() != 0 {
		t.Error("empty reserved config should classify nothing")
	}
}
