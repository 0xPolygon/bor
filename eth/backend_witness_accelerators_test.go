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

package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/eth/ethconfig"
)

// TestWitnessSafeAccelerators pins the boot-time rule that a witness-recording
// node runs neither execution accelerator, and that a node which records no
// witnesses keeps whatever the operator asked for.
//
// Both accelerators produce witnesses whose contents depend on wall clock, and
// WIT/2's cross-peer page-count check reads that divergence as a misbehaving
// peer. The two guards are independent: either witness flag alone is enough to
// disable both accelerators.
func TestWitnessSafeAccelerators(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		witnessProtocol  bool
		syncAndProduce   bool
		parallelEVM      bool
		pipelinedSRC     bool
		wantParallelEVM  bool
		wantPipelinedSRC bool
	}{
		{
			name:             "no witnesses keeps both accelerators",
			parallelEVM:      true,
			pipelinedSRC:     true,
			wantParallelEVM:  true,
			wantPipelinedSRC: true,
		},
		{
			name:            "wit protocol disables both",
			witnessProtocol: true,
			parallelEVM:     true,
			pipelinedSRC:    true,
		},
		{
			name:           "sync-and-produce disables both",
			syncAndProduce: true,
			parallelEVM:    true,
			pipelinedSRC:   true,
		},
		{
			name:            "both witness flags disable both",
			witnessProtocol: true,
			syncAndProduce:  true,
			parallelEVM:     true,
			pipelinedSRC:    true,
		},
		{
			name:            "already-off accelerators stay off",
			witnessProtocol: true,
		},
		{
			name:            "parallel EVM alone is disabled",
			witnessProtocol: true,
			parallelEVM:     true,
		},
		{
			name:            "pipelined SRC alone is disabled",
			witnessProtocol: true,
			pipelinedSRC:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := &ethconfig.Config{
				WitnessProtocol:          tc.witnessProtocol,
				SyncAndProduceWitnesses:  tc.syncAndProduce,
				EnablePipelinedImportSRC: tc.pipelinedSRC,
			}
			config.ParallelEVM.Enable = tc.parallelEVM

			parallelEVM, pipelinedSRC := witnessSafeAccelerators(config)
			if parallelEVM != tc.wantParallelEVM {
				t.Errorf("parallelEVM = %v, want %v", parallelEVM, tc.wantParallelEVM)
			}
			if pipelinedSRC != tc.wantPipelinedSRC {
				t.Errorf("pipelinedImportSRC = %v, want %v", pipelinedSRC, tc.wantPipelinedSRC)
			}
		})
	}
}
