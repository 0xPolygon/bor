package miner

import (
	"bytes"
	"encoding/binary"
	"math"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/metrics"
)

// Producer-side reserved-region signals that leave no trace in the block
// (they reflect the local pool view and build timing), so they can't be
// reconstructed later from chain data.
var (
	// reservedGasUsedGauge records each build's reserved-region gas total
	// (the value written into BlockExtraData.ReservedGasUsed).
	reservedGasUsedGauge = metrics.NewRegisteredGauge("worker/reserved/gasused", nil)
	// reservedOverflowMeter counts reserved-eligible transactions diverted to
	// the normal pass because of per-client quota or the global ceiling.
	reservedOverflowMeter = metrics.NewRegisteredMeter("worker/reserved/overflow", nil)
	// reservedInterruptCounter counts commit interrupts that fired while a
	// reserved group was being committed — a sign the time budget is tight
	// for reserved demand.
	reservedInterruptCounter = metrics.NewRegisteredCounter("worker/reserved/interrupt", nil)
)

type transactionsByPriceAndNonceFn func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce

// extractPriorityTxs filters the transactions from priority senders and
// returns them grouped by price and nonce.
func extractPriorityTxs(prio []common.Address, pendingTxs map[common.Address][]*txpool.LazyTransaction, newNormalTxSet transactionsByPriceAndNonceFn) *transactionsByPriceAndNonce {
	prioPlainTxs := make(map[common.Address][]*txpool.LazyTransaction)
	for _, account := range prio {
		if txs := pendingTxs[account]; len(txs) > 0 {
			delete(pendingTxs, account)
			prioPlainTxs[account] = txs
		}
	}
	return newNormalTxSet(prioPlainTxs)
}

// filterReservedTxs removes the transactions from reserved-eligible senders from
// pending transactions and returns them grouped by client ID.
func filterReservedTxs(pendingTxs map[common.Address][]*txpool.LazyTransaction, registry reservedRegistry) map[uint64]map[common.Address][]*txpool.LazyTransaction {
	var reserved = make(map[uint64]map[common.Address][]*txpool.LazyTransaction)
	for addr, txs := range pendingTxs {
		cid, ok := registry.Lookup(addr)
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
// quota. Transactions are ordered in ascending order of price (opposite from usual logic)
// to ensure that zero or below base fee transactions win over transactions carrying
// fallback fees (who can anyways compete in EIP-1559 fee market).
//
// The quota is measured by the transaction gas limit since actual gas used is only known
// after execution; used reports the declared gas the selection consumed so the caller
// can charge it against the global reserved ceiling. Transactions overflowing the quota
// are handed back to the caller for the normal pass.
//
// A sender's first quota breach diverts that transaction and every later nonce of
// the same sender to overflow: reserved groups commit before the normal pass, so
// selecting nonce n+1 after diverting nonce n would break the sender's nonce order.
//
// If block building is interrupted while the scan heap is being constructed, the
// constructor yields an empty heap, so this client contributes nothing to the
// build and its transactions stay in the pool for a later block. No overflow is
// re-added in that case, and it doesn't need to be: pendingTxs here is a local
// snapshot that's discarded once the (likewise-interrupted) commit pass returns,
// and the real pool is untouched — matching how an interrupted commit defers the
// remaining transactions.
func selectReservedTxs(
	clientTxs map[common.Address][]*txpool.LazyTransaction, quota uint64, newReservedTxSet transactionsByPriceAndNonceFn,
) (selected *transactionsByPriceAndNonce, used uint64, overflow map[common.Address][]*txpool.LazyTransaction) {
	scan := newReservedTxSet(clientTxs)
	selectedTxs := make(map[common.Address][]*txpool.LazyTransaction)
	overflow = make(map[common.Address][]*txpool.LazyTransaction)
	blocked := make(map[common.Address]bool)

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

	selected = newReservedTxSet(selectedTxs)
	return selected, used, overflow
}

// effectiveCeilingGas returns the registry's global reserved-region cap, normalizing
// zero (uncapped) to MaxUint64 so the selection loop needs no special case.
func effectiveCeilingGas(registry reservedRegistry) uint64 {
	if c := registry.CeilingGas(); c != 0 {
		return c
	}
	return math.MaxUint64
}

// extractReservedTxs pulls reserved-eligible transactions out of pendingTxs and
// returns one quota-trimmed, ordered group per client, in the deterministic
// parent-hash-keyed client order. registry is a per-build snapshot taken by the
// caller; a nil registry (production default until the registry module lands)
// or one with no clients is a no-op. Quota overflow — per-client and global
// ceiling alike — is re-added to pendingTxs for the normal pass.
func extractReservedTxs(registry reservedRegistry, parentHash common.Hash, pendingTxs map[common.Address][]*txpool.LazyTransaction, newReservedTxSet transactionsByPriceAndNonceFn) []*transactionsByPriceAndNonce {
	if registry == nil || len(registry.Clients()) == 0 {
		return nil
	}
	reservedTxs := filterReservedTxs(pendingTxs, registry)
	if len(reservedTxs) == 0 {
		return nil
	}

	// The global ceiling bounds the summed declared gas selected across all
	// clients. Like per-client quota, it is charged against declared gas
	// limits, not actual gas used.
	ceilingLeft := effectiveCeilingGas(registry)

	clientOrder := orderClients(parentHash, registry.Clients())
	var clientGroups = make([]*transactionsByPriceAndNonce, 0, len(clientOrder))
	for _, cid := range clientOrder {
		clientTxs := reservedTxs[cid]
		if len(clientTxs) == 0 {
			continue
		}

		selected, used, overflow := selectReservedTxs(clientTxs, min(registry.Quota(cid), ceilingLeft), newReservedTxSet)
		ceilingLeft -= used
		for addr, txs := range overflow {
			pendingTxs[addr] = append(pendingTxs[addr], txs...)
			reservedOverflowMeter.Mark(int64(len(txs)))
		}
		if selected.Empty() {
			continue
		}
		clientGroups = append(clientGroups, selected)
	}

	return clientGroups
}

// sequenceTxs orders the pending transactions into the groups the block is
// filled from, in commit order: priority senders first, then each reserved
// client (deterministic, parent-hash-keyed order), then the remaining normal
// transactions. Each group is an independently-ordered transactionsByPriceAndNonce
// the caller commits in turn. Empty groups are omitted so the caller never spins
// up a commit pass for nothing. registry is the caller's per-build snapshot
// (see worker.reservedRegistrySnapshot); nil disables the reserved pass.
//
// There is no error path — sequencing only groups and orders an in-memory
// snapshot; interruption is surfaced later, by commitTransactions.
func (w *worker) sequenceTxs(env *environment, registry reservedRegistry, pendingTxs map[common.Address][]*txpool.LazyTransaction) []*transactionsByPriceAndNonce {
	w.mu.RLock()
	prio := w.prio
	w.mu.RUnlock()

	newNormalTxSet := func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newTransactionsByPriceAndNonce(env.signer, txs, env.header.BaseFee, &w.interruptBlockBuilding)
	}
	newReservedTxSet := func(txs map[common.Address][]*txpool.LazyTransaction) *transactionsByPriceAndNonce {
		return newReservedTransactionsByNonce(env.signer, txs, env.header.BaseFee, &w.interruptBlockBuilding)
	}

	var txBatches = make([]*transactionsByPriceAndNonce, 0)

	// Priority transactions (operator override) commit first. A sender that is
	// both prioritized and registry-listed is consumed here: its transactions
	// bypass reserved quota accounting and pay normal fees. Operators should
	// not prioritize registered senders.
	if prioTxs := extractPriorityTxs(prio, pendingTxs, newNormalTxSet); !prioTxs.Empty() {
		txBatches = append(txBatches, prioTxs)
	}

	// Reserved transactions, one ordered group per client. No emptiness check
	// here, unlike the neighbouring groups: extractReservedTxs already omits
	// empty groups from the slice it returns, so every element is committable.
	txBatches = append(txBatches, extractReservedTxs(registry, env.header.ParentHash, pendingTxs, newReservedTxSet)...)

	// Everything left (including reserved quota overflow added back above) is
	// normal. Heap-init time is recorded for the normal batch only, keeping
	// the metric's historical meaning.
	if len(pendingTxs) > 0 {
		heapInitTime := time.Now()
		normalTxs := newNormalTxSet(pendingTxs)
		txHeapInitTimer.UpdateSince(heapInitTime)

		if !normalTxs.Empty() {
			txBatches = append(txBatches, normalTxs)
		}
	}

	return txBatches
}
