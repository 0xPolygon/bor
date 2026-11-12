package fetcher

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

const (
	stateSyncImportRetryDelay = 10 * time.Second
	maxStateSyncImportRetries = 2
)

func (f *BlockFetcher) insertBlockWithRetry(op *blockOrHeaderInject) error {
	for attempt := 0; ; attempt++ {
		_, err := f.insertChain(types.Blocks{op.block}, []*stateless.Witness{op.witness})
		if !errors.Is(err, core.ErrStateSyncProcessing) || isWitnessAttributableImportError(err) || attempt >= maxStateSyncImportRetries {
			return err
		}
		log.Warn("Retrying block import after state-sync processing failure", "number", op.number(), "hash", op.hash(), "delay", stateSyncImportRetryDelay, "err", err)
		timer := time.NewTimer(stateSyncImportRetryDelay)
		select {
		case <-timer.C:
		case <-f.quit:
			timer.Stop()
			return context.Canceled
		}
	}
}
