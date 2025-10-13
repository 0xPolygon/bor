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

package core

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
)

// ExecuteStateless runs a stateless execution based on a witness, verifies
// everything it can locally and returns the state root and receipt root, that
// need the other side to explicitly check.
//
// This method is a bit of a sore thumb here, but:
//   - It cannot be placed in core/stateless, because state.New prodces a circular dep
//   - It cannot be placed outside of core, because it needs to construct a dud headerchain
//
// TODO(karalabe): Would be nice to resolve both issues above somehow and move it.
func ExecuteStateless(config *params.ChainConfig, vmconfig vm.Config, block *types.Block, witness *stateless.Witness, author *common.Address, consensus consensus.Engine, diskdb ethdb.Database) (common.Hash, common.Hash, *state.StateDB, *ProcessResult, error) {

	fmt.Printf("PSP - Executing stateless for block %s\n", block.Number())
	fmt.Printf("\tPSP - Witness State Hashes:\n")

	// Open log file for writing witness state hash information
	logFile, err := os.OpenFile("witness_state_hashes.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Warn("Failed to open witness state hashes log file", "error", err)
	} else {
		defer logFile.Close()

		// Write block execution header with timestamp
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(logFile, "\n=== [%s] Block %s ===\n", timestamp, block.Number())
		fmt.Fprintf(logFile, "Witness State Hashes:\n")
		WriteHashesToFile(logFile, witness)
	}

	// Sanity check if the supplied block accidentally contains a set root or
	// receipt hash. If so, be very loud, but still continue.
	if block.Root() != (common.Hash{}) {
		log.Error("stateless runner received state root it's expected to calculate (faulty consensus client)", "block", block.Number())
	}
	if block.ReceiptHash() != (common.Hash{}) {
		log.Error("stateless runner received receipt root it's expected to calculate (faulty consensus client)", "block", block.Number())
	}
	// Create and populate the state database to serve as the stateless backend
	memdb := witness.MakeHashDB(diskdb)
	db, err := state.New(witness.Root(), state.NewDatabase(triedb.NewDatabase(memdb, triedb.HashDefaults), nil))
	if err != nil {
		return common.Hash{}, common.Hash{}, nil, nil, err
	}
	// Create a blockchain that is idle, but can be used to access headers through
	headerChain := &HeaderChain{
		config:      config,
		chainDb:     memdb,
		headerCache: lru.NewCache[common.Hash, *types.Header](256),
		engine:      consensus,
	}
	processor := NewStateProcessor(config, headerChain)
	validator := NewBlockValidator(config, nil) // No chain, we only validate the state, not the block

	res, err := processor.Process(block, db, vmconfig, author, context.Background())
	if err != nil {
		return common.Hash{}, common.Hash{}, nil, nil, err
	}

	if err = validator.ValidateState(block, db, res, true); err != nil {
		return common.Hash{}, common.Hash{}, nil, nil, err
	}
	// Almost everything validated, but receipt and state root needs to be returned
	receiptRoot := types.DeriveSha(res.Receipts, trie.NewStackTrie(nil))
	stateRoot := db.IntermediateRoot(config.IsEIP158(block.Number()))

	fmt.Printf("\tPSP - Updated Witness State Hashes:\n")
	if logFile != nil {
		fmt.Fprintf(logFile, "Updated Witness State Hashes:\n")
	}

	_, stateUdate, _ := db.CommitAndReturnStateUpdate(block.Number().Uint64(), config.IsEIP158(block.Number()))

	nodes := stateUdate.Nodes
	var order []common.Hash
	for owner := range nodes.Sets {
		if owner == (common.Hash{}) {
			continue
		}
		order = append(order, owner)
	}
	if _, ok := nodes.Sets[common.Hash{}]; ok {
		order = append(order, common.Hash{})
	}
	for _, owner := range order {
		subset := nodes.Sets[owner]
		fmt.Printf("\t\t- %s:\n", subset.Summary())
		if logFile != nil {
			fmt.Fprintf(logFile, "  - %s\n", subset.Summary())
		}
		subset.ForEachWithOrder(func(path string, n *trienode.Node) {
			if n.IsDeleted() {
				return // ignore deletion
			}
			fmt.Printf("\t\t- %s:\n", n.Hash)
			if logFile != nil {
				fmt.Fprintf(logFile, "  - %s\n", n.Hash)
			}
		})
	}

	return stateRoot, receiptRoot, db, res, nil
}

func WriteHashesToFile(file *os.File, w *stateless.Witness) {
	var (
		hasher = crypto.NewKeccakState()
		hash   = make([]byte, 32)
	)

	for node := range w.State {
		blob := []byte(node)

		hasher.Reset()
		hasher.Write(blob)
		hasher.Read(hash)
		fmt.Printf("\t\t- %s:\n", common.Bytes2Hex(hash))
		fmt.Fprintf(file, "  - %s\n", common.Bytes2Hex(hash))
	}
}
