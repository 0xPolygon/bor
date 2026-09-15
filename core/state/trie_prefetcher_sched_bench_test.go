package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// Block shape taken from mainnet block producers (anonymous-91..94), where
// worker_txApplyDuration reports p50 ~155-210us and p75 ~600-850us.
const benchTxsPerBlock = 100

// benchState builds a committed state and hands back the identities a
// prefetch call needs: the account addresses, each account's storage root,
// and the slot keys.
func benchState(accounts, slots int) (*StateDB, []common.Address, map[common.Address]common.Hash, []common.Hash) {
	db := NewDatabaseForTesting()
	state, _ := New(types.EmptyRootHash, db)

	addrs := make([]common.Address, accounts)
	keys := make([]common.Hash, slots)

	for i := range keys {
		keys[i] = common.BigToHash(uint256.NewInt(uint64(i + 1)).ToBig())
	}

	for i := range addrs {
		addrs[i] = common.BytesToAddress(fmt.Appendf(nil, "acct-%06d", i))
		state.SetBalance(addrs[i], uint256.NewInt(uint64(i+1)), tracing.BalanceChangeUnspecified)
		state.SetCode(addrs[i], fmt.Appendf(nil, "code-%06d", i), tracing.CodeChangeUnspecified)

		for j := range keys {
			state.SetState(addrs[i], keys[j], keys[j])
		}
	}

	root, err := state.Commit(0, false, false)
	if err != nil {
		panic(err)
	}

	committed, err := New(root, db)
	if err != nil {
		panic(err)
	}

	roots := make(map[common.Address]common.Hash, len(addrs))
	for _, addr := range addrs {
		roots[addr] = committed.GetStorageRoot(addr)
	}

	return committed, addrs, roots, keys
}

// burn occupies this goroutine the way transaction execution occupies the
// sealing thread. A sleep would hand the core to the prefetcher and flatter
// the deferred variant, so spin instead.
func burn(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}

type benchShape struct {
	name     string
	accounts int // account reads per transaction
	slots    int // storage slot reads per transaction
	txWork   time.Duration
}

// A transaction reads a few accounts (sender, recipient, contracts, coinbase)
// and many more storage slots. That ratio matters: CommitWitnessTx batches the
// accounts into one prefetch call but still issues one call per storage slot,
// so most reads get pure deferral with no batching to pay for it.
var benchShapes = []benchShape{
	{"acct4/slot6/work20us", 4, 6, 20 * time.Microsecond},
	{"acct4/slot6/work180us", 4, 6, 180 * time.Microsecond},
	{"acct4/slot26/work20us", 4, 26, 20 * time.Microsecond},
	{"acct4/slot26/work180us", 4, 26, 180 * time.Microsecond},
	{"acct4/slot26/work600us", 4, 26, 600 * time.Microsecond},
	{"acct2/slot78/work180us", 2, 78, 180 * time.Microsecond},
}

// benchShapeRun drives a block's worth of reads through the prefetcher and
// returns only once it has drained, which is what the state root computation
// waits for.
//
// deferred=false is develop: each read is scheduled the instant the EVM
// touches it. deferred=true mirrors CommitWitnessTx exactly -- accounts are
// coalesced into a single call, storage slots are still one call each, and all
// of it happens when the transaction commits rather than during it.
func benchShapeRun(b *testing.B, shape benchShape, deferred bool) {
	db, addrs, roots, keys := benchState(256, 64)

	reads := shape.accounts + shape.slots
	perRead := shape.txWork / time.Duration(reads)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		p := newTriePrefetcher(db.db, db.originalRoot, "bench", false /* noreads: witness mode */)

		for tx := 0; tx < benchTxsPerBlock; tx++ {
			var (
				acctBuf []common.Address
				slotBuf []common.Hash
			)

			owner := addrs[tx%len(addrs)]

			for r := 0; r < shape.accounts; r++ {
				burn(perRead)

				addr := addrs[(tx*shape.accounts+r)%len(addrs)]
				if deferred {
					acctBuf = append(acctBuf, addr)
					continue
				}

				_ = p.prefetch(common.Hash{}, db.originalRoot, common.Address{}, []common.Address{addr}, nil, true)
			}

			for r := 0; r < shape.slots; r++ {
				burn(perRead)

				key := keys[(tx*shape.slots+r)%len(keys)]
				if deferred {
					slotBuf = append(slotBuf, key)
					continue
				}

				_ = p.prefetch(crypto.Keccak256Hash(owner[:]), roots[owner], owner, nil, []common.Hash{key}, true)
			}

			if deferred {
				if len(acctBuf) > 0 {
					_ = p.prefetch(common.Hash{}, db.originalRoot, common.Address{}, acctBuf, nil, true)
				}
				// One call per slot, exactly as CommitWitnessTx does.
				for _, key := range slotBuf {
					_ = p.prefetch(crypto.Keccak256Hash(owner[:]), roots[owner], owner, nil, []common.Hash{key}, true)
				}
			}
		}

		// Root computation blocks on the prefetcher; include that wait.
		p.terminate(true)
	}
}

func BenchmarkPrefetchScheduleImmediate(b *testing.B) {
	for _, s := range benchShapes {
		b.Run(s.name, func(b *testing.B) { benchShapeRun(b, s, false) })
	}
}

func BenchmarkPrefetchScheduleDeferred(b *testing.B) {
	for _, s := range benchShapes {
		b.Run(s.name, func(b *testing.B) { benchShapeRun(b, s, true) })
	}
}
