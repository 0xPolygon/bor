package eth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	maxConcurrencyLimit = 5
)

// interface for testability
//
//go:generate mockgen -source=state_sync.go -destination=../tests/bor/mocks/MockHeaderProvider.go -package=mocks
type HeaderProvider interface {
	HeaderByNumber(ctx context.Context, num rpc.BlockNumber) (*types.Header, error)
	CurrentHeader() *types.Header
}

// checkStateSyncConsistency runs over the block interval checking
// if there are any missing state sync txs. The data is
// retrieved from Heimdall as source of truth, which demands a
// synced heimdall node.
func (eth *Ethereum) checkStateSyncConsistency(start, end uint64, headerProvider HeaderProvider, bor *bor.Bor) ([]common.Hash, error) {
	ctx := context.Background()
	startBlockHeader, err := headerProvider.HeaderByNumber(ctx, rpc.BlockNumber(start))
	if err != nil {
		return nil, err
	}

	endBlockHeader, err := headerProvider.HeaderByNumber(ctx, rpc.BlockNumber(end))
	if err != nil {
		return nil, err
	}

	// Binary Search over first id
	lastStateIdBig, err := bor.GenesisContractsClient.LastStateId(nil, headerProvider.CurrentHeader().Number.Uint64(), headerProvider.CurrentHeader().Hash())
	if err != nil {
		return nil, err
	}
	targetBlockStartTime := time.Unix(int64(startBlockHeader.Time), 0)

	startStateSyncId, err := findBoundaryStateSync(0, lastStateIdBig.Uint64(), targetBlockStartTime, bor)
	if err != nil {
		return nil, err
	}

	// Fetch State Syncs and checks agains local db
	targetBlockEndTime := time.Unix(int64(endBlockHeader.Time), 0)

	return checkStateSyncOnRange(startStateSyncId, targetBlockEndTime, bor, eth.chainDb)
}

func findBoundaryStateSync(lo, hi uint64, targetBlockTime time.Time, bor *bor.Bor) (uint64, error) {
	for lo < hi {
		mid := lo + (hi-lo)/2
		resp, err := bor.HeimdallClient.StateSyncEventById(context.Background(), mid)
		if err != nil {
			return 0, err
		}
		direction := targetBlockTime.Compare(resp.Time)

		switch direction {
		case -1:
			hi = mid
		case 1:
			lo = mid + 1
		case 0:
			return mid, nil
		default:
			return 0, fmt.Errorf("unexpected response %q at index %d", resp, mid)
		}
	}
	return lo, nil
}

func checkStateSyncOnRange(startStateSyncId uint64, targetBlockTime time.Time, bor *bor.Bor, db ethdb.Reader) ([]common.Hash, error) {
	missingStateSyncTxs := make([]common.Hash, 0)
	var missingStateSyncTxsMu sync.Mutex
	// closed when we reach lastTargetBlockTime
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	concurrencySem := make(chan struct{}, maxConcurrencyLimit)
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	go func() {
		for stateSyncId := startStateSyncId; ; stateSyncId += bor.HeimdallClient.StateFetchLimit() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			concurrencySem <- struct{}{}
			wg.Add(1)

			go func(x uint64) {
				defer wg.Done()
				defer func() { <-concurrencySem }()

				// new context because we dont cancel pending queries even though cancel is called
				resp, err := bor.HeimdallClient.StateSyncEventsList(context.Background(), x)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}

				for _, stateSyncEvent := range resp {
					// cancel when reach target time
					if targetBlockTime.Compare(stateSyncEvent.Time) < 0 {
						cancel()
						return
					}
					lookup := rawdb.ReadBorTxLookupEntry(db, stateSyncEvent.TxHash)
					if lookup == nil {
						missingStateSyncTxsMu.Lock()
						missingStateSyncTxs = append(missingStateSyncTxs, stateSyncEvent.TxHash)
						missingStateSyncTxsMu.Unlock()
					}
				}

			}(stateSyncId)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// either capture first error or all workers done
	select {
	case firstErr := <-errCh:
		<-done
		return nil, firstErr
	case <-done:
		return missingStateSyncTxs, nil
	}
}
