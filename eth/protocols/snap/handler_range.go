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

package snap

import (
	"bytes"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb/database"
)

var errUnknownAccount = errors.New("unknown account")

// serveAccountRange reads the requested accounts from it and proves the range
// against tr. An iterator that fails midway stops after the last account it
// read, so the accounts read so far are served as a range capped there.
func serveAccountRange(tr *trie.Trie, it snapshot.AccountIterator, req *GetAccountRangePacket) ([]*AccountData, [][]byte) {
	accounts, last, err := collectAccounts(it, req.Limit, req.Bytes)
	it.Release()
	if err != nil && len(accounts) == 0 {
		return nil, nil
	}
	// Generate the Merkle proofs for the first and last account
	proof := trienode.NewProofSet()
	if err := tr.Prove(req.Origin[:], proof); err != nil {
		log.Warn("Failed to prove account range", "origin", req.Origin, "err", err)
		return nil, nil
	}
	if last != (common.Hash{}) {
		if err := tr.Prove(last[:], proof); err != nil {
			log.Warn("Failed to prove account range", "last", last, "err", err)
			return nil, nil
		}
	}
	return accounts, proof.List()
}

// serveStorageRanges reads the storage of the requested accounts in order,
// opening each account's iterator with open, until the response limit is
// reached. If an iterator cannot be opened, the reply ends before that account.
// The ranges served before it are complete and carry no proof, which the
// requester accepts, and it asks again for the accounts left out.
func serveStorageRanges(db database.NodeDatabase, req *GetStorageRangesPacket, open func(account, origin common.Hash) (snapshot.StorageIterator, error)) ([][]*StorageData, [][]byte) {
	// Calculate the hard limit at which to abort, even if mid storage trie
	hardLimit := uint64(float64(req.Bytes) * (1 + stateLookupSlack))

	var (
		slots  [][]*StorageData
		proofs [][]byte
		size   uint64
	)
	// If we've exceeded the requested data limit, abort without opening
	// a new storage range (that we'd need to prove due to exceeded size)
	for i := 0; i < len(req.Accounts) && size < req.Bytes; i++ {
		account := req.Accounts[i]
		origin, limit := nextStorageBounds(req)

		it, err := open(account, origin)
		if err != nil {
			break
		}
		storage, proof, done, err := serveStorageRange(db, req.Root, account, it, origin, limit, &size, hardLimit)
		if err != nil {
			return nil, nil
		}
		if len(storage) > 0 {
			slots = append(slots, storage)
		}
		// Proof terminates the reply as proofs are only added if a node
		// refuses to serve more data (exception when a contract fetch is
		// finishing, but that's that).
		if done {
			proofs = proof
			break
		}
	}
	return slots, proofs
}

// nextStorageBounds returns the origin and limit of the next account's range,
// consuming those of the request: only the first account might start from a
// different origin and end sooner.
func nextStorageBounds(req *GetStorageRangesPacket) (common.Hash, common.Hash) {
	var origin common.Hash
	if len(req.Origin) > 0 {
		origin, req.Origin = common.BytesToHash(req.Origin), nil
	}
	var limit = common.MaxHash
	if len(req.Limit) > 0 {
		limit, req.Limit = common.BytesToHash(req.Limit), nil
	}
	return origin, limit
}

// serveStorageRange reads the slots of one account from it. A range that starts
// past the first slot or is capped, including by an iterator failing midway, is
// proven against the storage trie of the account at root, and done reports that
// the reply ends with it. An iterator that fails before any slot ends the reply
// before this account.
func serveStorageRange(db database.NodeDatabase, root, account common.Hash, it snapshot.StorageIterator, origin, limit common.Hash, size *uint64, hardLimit uint64) ([]*StorageData, [][]byte, bool, error) {
	storage, last, abort, err := collectStorage(it, limit, size, hardLimit)
	it.Release()
	if err != nil && len(storage) == 0 {
		return nil, nil, true, nil
	}
	// If the entire storage trie is included in the response, no need for any proofs.
	if origin == (common.Hash{}) && (!abort || len(storage) == 0) {
		return storage, nil, false, nil
	}
	proof, err := proveStorageRange(db, root, account, origin, last)
	return storage, proof, true, err
}

// proveStorageRange generates the Merkle proofs for the first and last storage
// slot of a range.
func proveStorageRange(db database.NodeDatabase, root, account, origin, last common.Hash) ([][]byte, error) {
	accTrie, err := trie.NewStateTrie(trie.StateTrieID(root), db)
	if err != nil {
		return nil, err
	}
	acc, err := accTrie.GetAccountByHash(account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, errUnknownAccount
	}
	stTrie, err := trie.NewStateTrie(trie.StorageTrieID(root, account, acc.Root), db)
	if err != nil {
		return nil, err
	}
	proof := trienode.NewProofSet()
	if err := stTrie.Prove(origin[:], proof); err != nil {
		log.Warn("Failed to prove storage range", "origin", origin, "err", err)
		return nil, err
	}
	if last != (common.Hash{}) {
		if err := stTrie.Prove(last[:], proof); err != nil {
			log.Warn("Failed to prove storage range", "last", last, "err", err)
			return nil, err
		}
	}
	return proof.List(), nil
}

// collectAccounts reads accounts from it up to limit, or until more than
// maxBytes were read. The iterator can fail midway, e.g. when the layer it
// reads turns stale, in which case the accounts read so far are returned
// along with the error.
func collectAccounts(it snapshot.AccountIterator, limit common.Hash, maxBytes uint64) ([]*AccountData, common.Hash, error) {
	var (
		accounts []*AccountData
		size     uint64
		last     common.Hash
	)
	for it.Next() {
		hash, account := it.Hash(), common.CopyBytes(it.Account())

		// Track the returned interval for the Merkle proofs
		last = hash

		// Assemble the reply item
		size += uint64(common.HashLength + len(account))
		accounts = append(accounts, &AccountData{
			Hash: hash,
			Body: account,
		})
		// If we've exceeded the request threshold, abort
		if bytes.Compare(hash[:], limit[:]) >= 0 {
			break
		}
		if size > maxBytes {
			break
		}
	}
	return accounts, last, it.Error()
}

// collectStorage reads the slots of one account from it up to limit, adding
// their size to the response size. It stops early and reports abort once the
// response size reaches hardLimit. Like collectAccounts, it returns the slots
// read before the iterator failed along with the error, and reports abort for
// them, as a truncated range served without a proof would be taken as the
// account's complete storage.
func collectStorage(it snapshot.StorageIterator, limit common.Hash, size *uint64, hardLimit uint64) ([]*StorageData, common.Hash, bool, error) {
	var (
		storage []*StorageData
		last    common.Hash
		abort   bool
	)
	for it.Next() {
		if *size >= hardLimit {
			abort = true
			break
		}
		hash, slot := it.Hash(), common.CopyBytes(it.Slot())

		// Track the returned interval for the Merkle proofs
		last = hash

		// Assemble the reply item
		*size += uint64(common.HashLength + len(slot))
		storage = append(storage, &StorageData{
			Hash: hash,
			Body: slot,
		})
		// If we've exceeded the request threshold, abort
		if bytes.Compare(hash[:], limit[:]) >= 0 {
			break
		}
	}
	if err := it.Error(); err != nil {
		return storage, last, true, err
	}
	return storage, last, abort, nil
}
