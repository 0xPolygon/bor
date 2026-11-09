// Copyright 2023 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/era"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

var (
	count uint64 = 128
	step  uint64 = 16
)

func TestHistoryImportAndExport(t *testing.T) {
	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		genesis = &core.Genesis{
			Config: params.TestChainConfig,
			Alloc:  types.GenesisAlloc{address: {Balance: big.NewInt(1000000000000000000)}},
		}
		signer = types.LatestSigner(genesis.Config)
	)

	// Generate chain.
	db, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), int(count), func(i int, g *core.BlockGen) {
		if i == 0 {
			return
		}
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID:    genesis.Config.ChainID,
			Nonce:      uint64(i - 1),
			GasTipCap:  common.Big0,
			GasFeeCap:  g.PrevBlock(0).BaseFee(),
			Gas:        50000,
			To:         &common.Address{0xaa},
			Value:      big.NewInt(int64(i)),
			Data:       nil,
			AccessList: nil,
		})
		if err != nil {
			t.Fatalf("error creating tx: %v", err)
		}
		g.AddTx(tx)
	})

	// Initialize BlockChain.
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("unable to initialize chain: %v", err)
	}
	if _, err := chain.InsertChain(blocks, false); err != nil {
		t.Fatalf("error inserting chain: %v", err)
	}

	// Make temp directory for era files.
	dir := t.TempDir()

	// Export history to temp directory.
	if err := ExportHistory(chain, dir, 0, count, step); err != nil {
		t.Fatalf("error exporting history: %v", err)
	}

	// Read checksums.
	b, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatalf("failed to read checksums: %v", err)
	}
	checksums := strings.Split(string(b), "\n")

	// Verify each Era.
	entries, _ := era.ReadDir(dir, "mainnet")
	for i, filename := range entries {
		func() {
			f, err := os.Open(filepath.Join(dir, filename))
			if err != nil {
				t.Fatalf("error opening era file: %v", err)
			}
			var (
				h   = sha256.New()
				buf = bytes.NewBuffer(nil)
			)
			if _, err := io.Copy(h, f); err != nil {
				t.Fatalf("unable to recalculate checksum: %v", err)
			}
			if got, want := common.BytesToHash(h.Sum(buf.Bytes()[:])).Hex(), checksums[i]; got != want {
				t.Fatalf("checksum %d does not match: got %s, want %s", i, got, want)
			}
			e, err := era.From(f)
			if err != nil {
				t.Fatalf("error opening era: %v", err)
			}
			defer e.Close()
			it, err := era.NewIterator(e)
			if err != nil {
				t.Fatalf("error making era reader: %v", err)
			}
			for j := 0; it.Next(); j++ {
				n := i*int(step) + j
				if it.Error() != nil {
					t.Fatalf("error reading block entry %d: %v", n, it.Error())
				}
				block, receipts, err := it.BlockAndReceipts()
				if err != nil {
					t.Fatalf("error reading block entry %d: %v", n, err)
				}
				want := chain.GetBlockByNumber(uint64(n))
				if want, got := uint64(n), block.NumberU64(); want != got {
					t.Fatalf("blocks out of order: want %d, got %d", want, got)
				}
				if want.Hash() != block.Hash() {
					t.Fatalf("block hash mismatch %d: want %s, got %s", n, want.Hash().Hex(), block.Hash().Hex())
				}
				if got := types.DeriveSha(block.Transactions(), trie.NewStackTrie(nil)); got != want.TxHash() {
					t.Fatalf("tx hash %d mismatch: want %s, got %s", n, want.TxHash(), got)
				}
				if got := types.CalcUncleHash(block.Uncles()); got != want.UncleHash() {
					t.Fatalf("uncle hash %d mismatch: want %s, got %s", n, want.UncleHash(), got)
				}
				if got := types.DeriveSha(receipts, trie.NewStackTrie(nil)); got != want.ReceiptHash() {
					t.Fatalf("receipt root %d mismatch: want %s, got %s", n, want.ReceiptHash(), got)
				}
			}
		}()
	}

	// Now import Era.
	db2, err := rawdb.Open(rawdb.NewMemoryDatabase(), rawdb.OpenOptions{})
	if err != nil {
		panic(err)
	}
	t.Cleanup(func() {
		db2.Close()
	})

	genesis.MustCommit(db2, triedb.NewDatabase(db2, triedb.HashDefaults))
	imported, err := core.NewBlockChain(db2, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("unable to initialize chain: %v", err)
	}
	if err := ImportHistory(imported, db2, dir, "mainnet"); err != nil {
		t.Fatalf("failed to import chain: %v", err)
	}
	if have, want := imported.CurrentHeader(), chain.CurrentHeader(); have.Hash() != want.Hash() {
		t.Fatalf("imported chain does not match expected, have (%d, %s) want (%d, %s)", have.Number, have.Hash(), want.Number, want.Hash())
	}
}

// TestHistoryImportReadError checks that a read failure in the middle of an
// Era file fails the import instead of ending it early as a success.
func TestHistoryImportReadError(t *testing.T) {
	genesis := &core.Genesis{Config: params.TestChainConfig}
	db, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 15, nil)
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("unable to initialize chain: %v", err)
	}
	if _, err := chain.InsertChain(blocks, false); err != nil {
		t.Fatalf("error inserting chain: %v", err)
	}
	dir := t.TempDir()
	if err := ExportHistory(chain, dir, 0, 16, 16); err != nil {
		t.Fatalf("error exporting history: %v", err)
	}

	// Point the first file's index entry for block 1 at the version record at
	// the start of the file, a record of the wrong type, then refresh the
	// checksum so only the read itself fails.
	entries, err := era.ReadDir(dir, "mainnet")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no era files: %v", err)
	}
	path := filepath.Join(dir, entries[0])
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const blocksPerFile, block = 16, 1
	indexRecord := int64(len(data)) - 24 - blocksPerFile*8
	entry := len(data) - 8 - (blocksPerFile-block)*8
	binary.LittleEndian.PutUint64(data[entry:], uint64(-indexRecord))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(sums), "\n")
	lines[0] = common.BytesToHash(sha256Sum(data)).Hex()
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	db2, err := rawdb.Open(rawdb.NewMemoryDatabase(), rawdb.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	genesis.MustCommit(db2, triedb.NewDatabase(db2, triedb.HashDefaults))
	imported, err := core.NewBlockChain(db2, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatalf("unable to initialize chain: %v", err)
	}
	err = ImportHistory(imported, db2, dir, "mainnet")
	if err == nil || !strings.Contains(err.Error(), entries[0]) {
		t.Fatalf("want a read error for %s, got %v (head %d)", entries[0], err, imported.CurrentHeader().Number)
	}
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
