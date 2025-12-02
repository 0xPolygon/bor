package types

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/0xPolygon/heimdall-v2/common/cache"
	"github.com/0xPolygon/heimdall-v2/helper"
	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

var (
	defaultTTL  = 10 * time.Second
	rootCache   *cache.Cache[[]byte]
	existsCache *cache.Cache[bool]
	initOnce    sync.Once
)

// IsValidCheckpoint validates if checkpoint rootHash matches or not
func IsValidCheckpoint(start uint64, end uint64, rootHash []byte, checkpointLength uint64, contractCaller helper.IContractCaller, confirmations uint64) (bool, error) {
	initOnce.Do(func() {
		rootCache = cache.NewCache[[]byte](defaultTTL)
		existsCache = cache.NewCache[bool](defaultTTL)
	})

	existsKey := fmt.Sprintf("%d-%d", end, confirmations)
	exists, err := existsCache.Get(existsKey)

	if !exists || err != nil {
		if err != nil {
			helper.Logger.Debug("Blocks existence not found in cache, querying contract",
				"end", end,
				"confirmations", confirmations,
				"existsKey", existsKey,
				"error", err,
			)
		}
		// Check if blocks exist locally
		exists, err := contractCaller.CheckIfBlocksExist(end + confirmations)
		if err != nil {
			return false, borTypes.ErrFailedToQueryBor
		}
		if !exists {
			return false, errors.New("blocks not found locally")
		}

		existsCache.Set(existsKey, exists)
	}

	rootKey := fmt.Sprintf("%d-%d-%d", start, end, checkpointLength)
	root, err := rootCache.Get(rootKey)
	if err != nil {
		helper.Logger.Debug("Root hash not found in cache, querying contract",
			"start", start,
			"end", end,
			"checkpointLength", checkpointLength,
			"rootKey", rootKey,
			"error", err,
		)

		// Compare RootHash
		root, err = contractCaller.GetRootHash(start, end, checkpointLength)
		if err != nil {
			return false, borTypes.ErrFailedToQueryBor
		}

		if len(root) > 0 {
			rootCache.Set(rootKey, root)
		}
	}

	if bytes.Equal(root, rootHash) {
		return true, nil
	}

	return false, nil
}
