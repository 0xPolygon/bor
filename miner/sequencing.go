package miner

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/crypto"
)

type newTransactionsByPriceAndNonceFn func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce

// extractPriorityTxs filters the transactions from priority senders and
// returns them grouped by price and nonce.
func extractPriorityTxs(prio []common.Address, pendingTxs map[common.Address][]*txpool.LazyTransaction, newTransactionsByPriceAndNonce newTransactionsByPriceAndNonceFn) *transactionsByPriceAndNonce {
	prioPlainTxs := make(map[common.Address][]*txpool.LazyTransaction)
	for _, account := range prio {
		if txs := pendingTxs[account]; len(txs) > 0 {
			delete(pendingTxs, account)
			prioPlainTxs[account] = txs
		}
	}
	return newTransactionsByPriceAndNonce(prioPlainTxs)
}

// filterReservedTxs removes the transactions from reserved-eligible senders from
// pending transactions and returns them grouped by client ID.
func filterReservedTxs(pendingTxs map[common.Address][]*txpool.LazyTransaction, reservedRegistry ReservedRegistry) map[uint64]map[common.Address][]*txpool.LazyTransaction {
	var reserved = make(map[uint64]map[common.Address][]*txpool.LazyTransaction)
	for addr, txs := range pendingTxs {
		cid, ok := reservedRegistry.Lookup(addr)
		if !ok {
			continue
		}
		if reserved[cid] == nil {
			reserved[cid] = make(map[common.Address][]*txpool.LazyTransaction)
		}
		reserved[cid][addr] = txs
		delete(pendingTxs, addr)
	}
	return reserved
}

// orderClients returns ids sorted ascending by
// keccak256(8-byte-bigendian(id) || parentHash). Rotates per block so the
// reserved-pass quota race doesn't systematically favour low-id clients.
func orderClients(parentHash common.Hash, ids []uint64) []uint64 {
	if len(ids) == 0 {
		return nil
	}
	type ranked struct {
		id     uint64
		digest []byte
	}
	var buf [40]byte
	copy(buf[8:], parentHash[:])
	ranks := make([]ranked, len(ids))
	for i, id := range ids {
		binary.BigEndian.PutUint64(buf[:8], id)
		ranks[i] = ranked{id: id, digest: crypto.Keccak256(buf[:])}
	}
	sort.Slice(ranks, func(i, j int) bool {
		return bytes.Compare(ranks[i].digest, ranks[j].digest) < 0
	})
	out := make([]uint64, len(ranks))
	for i, r := range ranks {
		out[i] = r.id
	}
	return out
}

// selectReservedTxs picks the best transactions of a reserved client capped by their
// quota. Transactions are ordered in asending order of price (opposite from usual logic)
// to ensure that zero or below base fee transactions win over transactions carrying
// fallback fees (who can anyways compete in EIP-1559 fee market).
//
// The quota is measured by the transaction gas limit since actual gas used is only known
// after execution. Transactions overflowing the quota are added back again to the original
// pending list of transsactions.
//
// If block building is interrupted while the scan heap is being constructed, the
// constructor yields an empty heap, so this client contributes nothing to the
// build and its transactions stay in the pool for a later block. No overflow is
// re-added in that case, and it doesn't need to be: pendingTxs here is a local
// snapshot that's discarded once the (likewise-interrupted) commit pass returns,
// and the real pool is untouched — matching how an interrupted commit defers the
// remaining transactions.
func selectReservedTxs(
	clientTxs map[common.Address][]*txpool.LazyTransaction, quota uint64, newTransactionsByPriceAndNonce newTransactionsByPriceAndNonceFn,
) (selected *transactionsByPriceAndNonce, overflow map[common.Address][]*txpool.LazyTransaction) {
	scan := newTransactionsByPriceAndNonce(clientTxs)
	selectedTxs := make(map[common.Address][]*txpool.LazyTransaction)
	overflow = make(map[common.Address][]*txpool.LazyTransaction)
	blocked := make(map[common.Address]bool)

	var used uint64
	for {
		ltx, _ := scan.Peek()
		if ltx == nil {
			break
		}
		from, _ := scan.PeekFrom()
		if blocked[from] || used+ltx.Gas > quota {
			blocked[from] = true
			overflow[from] = append(overflow[from], ltx)
		} else {
			selectedTxs[from] = append(selectedTxs[from], ltx)
			used += ltx.Gas
		}
		scan.Shift()
	}

	selected = newTransactionsByPriceAndNonce(selectedTxs)
	return selected, overflow
}

func (w *worker) extractReservedTxs(parentHash common.Hash, pendingTxs map[common.Address][]*txpool.LazyTransaction, newTransactionsByPriceAndNonce newTransactionsByPriceAndNonceFn) []*transactionsByPriceAndNonce {
	if w.reservedRegistry == nil {
		return nil
	}

	registry := w.reservedRegistry.Snapshot()
	if len(registry.Clients()) == 0 {
		return nil
	}
	reservedTxs := filterReservedTxs(pendingTxs, registry)
	if len(reservedTxs) == 0 {
		return nil
	}

	// TODO: pin this order if there are multiple iterations of building a single block
	clientOrder := orderClients(parentHash, registry.Clients())
	var sequence = make([]*transactionsByPriceAndNonce, 0, len(clientOrder))
	for _, cid := range clientOrder {
		clientTxs := reservedTxs[cid]
		if len(clientTxs) == 0 {
			continue
		}

		selected, overflow := selectReservedTxs(clientTxs, registry.Quota(cid), newTransactionsByPriceAndNonce)
		for addr, txs := range overflow {
			pendingTxs[addr] = append(pendingTxs[addr], txs...)
		}
		if selected.Empty() {
			continue
		}
		sequence = append(sequence, selected)
	}

	return sequence
}

// sequenceTxs orders the pending transactions into the groups the block is
// filled from, in commit order: priority senders first, then each reserved
// client (deterministic, parent-hash-keyed order), then the remaining normal
// transactions. Each group is an independently-ordered transactionsByPriceAndNonce
// the caller commits in turn. Empty groups are omitted so the caller never spins
// up a commit pass for nothing.
//
// There is no error path — sequencing only groups and orders an in-memory
// snapshot; interruption is surfaced later, by commitTransactions.
func (w *worker) sequenceTxs(env *environment, pendingTxs map[common.Address][]*txpool.LazyTransaction) []*transactionsByPriceAndNonce {
	w.mu.RLock()
	prio := w.prio
	w.mu.RUnlock()

	newTransactionsByPriceAndNonceFn := func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newTransactionsByPriceAndNonce(env.signer, txs, env.header.BaseFee, &w.interruptBlockBuilding)
	}
	newReservedTransactionsByNonceFn := func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newReservedTransactionsByNonce(env.signer, txs, env.header.BaseFee, &w.interruptBlockBuilding)
	}

	var sequence = make([]*transactionsByPriceAndNonce, 0)

	// Priority transactions (operator override) commit first.
	if prioTxs := extractPriorityTxs(prio, pendingTxs, newTransactionsByPriceAndNonceFn); !prioTxs.Empty() {
		sequence = append(sequence, prioTxs)
	}

	// Reserved transactions, one ordered group per client.
	sequence = append(sequence, w.extractReservedTxs(env.header.ParentHash, pendingTxs, newReservedTransactionsByNonceFn)...)

	// Everything left (including reserved quota overflow added back above) is normal.
	if normalTxs := newTransactionsByPriceAndNonceFn(pendingTxs); !normalTxs.Empty() {
		sequence = append(sequence, normalTxs)
	}

	return sequence
}
