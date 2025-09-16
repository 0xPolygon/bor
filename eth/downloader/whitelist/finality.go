package whitelist

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

type finality[T rawdb.BlockFinality[T]] struct {
	sync.RWMutex
	db       ethdb.Database
	Hash     common.Hash // Whitelisted Hash, populated by reaching out to heimdall
	Number   uint64      // Number , populated by reaching out to heimdall
	interval uint64      // Interval, until which we can allow importing
	doExist  bool
	name     string // Name of the service (checkpoint or milestone)
}

type finalityService interface {
	IsValidPeer(fetchHeadersByNumber func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error)) (bool, error)
	IsValidChain(currentHeader *types.Header, chain []*types.Header) (bool, error)
	Get() (bool, uint64, common.Hash)
	Process(block uint64, hash common.Hash)
	Purge()
}

func isIncorrectMilestone(number uint64, hash common.Hash) bool {
	var (
		incorrectEnd  = uint64(76273070)
		incorrectHash = common.HexToHash("7910a20918558674edb87759a2bb08b31af0de0e4eef0d8909be75af3591748f")
	)
	if number == incorrectEnd && hash == incorrectHash {
		log.Debug("Ignoring validating against incorrect milestone", "number", incorrectEnd, "hash", incorrectHash)
		return true
	}
	return false
}

// IsValidPeer checks if the chain we're about to receive from a peer is valid or not
// in terms of reorgs. We won't reorg beyond the last bor finality submitted to mainchain.
func (f *finality[T]) IsValidPeer(fetchHeadersByNumber func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error)) (bool, error) {
	// We want to validate the chain by comparing the last finalized block
	f.RLock()
	doExist := f.doExist
	number := f.Number
	hash := f.Hash
	f.RUnlock()

	// Ignore validating against incorrect milestone
	if isIncorrectMilestone(number, hash) {
		return true, nil
	}

	return isValidPeer(fetchHeadersByNumber, doExist, number, hash)
}

// IsValidChain checks the validity of chain by comparing it against the local
// whitelisted entry of checkpoint/milestone.
func (f *finality[T]) IsValidChain(currentHeader *types.Header, chain []*types.Header) (bool, error) {
	// Return if we've received empty chain
	if len(chain) == 0 {
		return false, nil
	}

	// Ignore validating against incorrect milestone
	if isIncorrectMilestone(f.Number, f.Hash) {
		return true, nil
	}

	return isValidChain(currentHeader, chain, f.doExist, f.Number, f.Hash)
}

// reportWhitelist logs the block number and hash if a new and unique entry is being inserted
// and it doesn't log for duplicate/redundant entries.
func (f *finality[T]) reportWhitelist(block uint64, hash common.Hash) {
	msg := fmt.Sprintf("Whitelisting new %s from heimdall", f.name)
	if !f.doExist {
		log.Info(msg, "block", block, "hash", hash)
	} else {
		if f.Number != block && f.Hash != hash {
			log.Info(msg, "block", block, "hash", hash)
		}
	}
}

func (f *finality[T]) Process(block uint64, hash common.Hash) {
	f.reportWhitelist(block, hash)

	f.doExist = true
	f.Hash = hash
	f.Number = block

	err := rawdb.WriteLastFinality[T](f.db, block, hash)
	if err != nil {
		log.Error("Error in writing whitelist state to db", "err", err)
	}
}

// Get returns the existing whitelisted entries of the form
// (doExist, block number, block hash).
func (f *finality[T]) Get() (bool, uint64, common.Hash) {
	f.RLock()
	defer f.RUnlock()

	if f.doExist {
		return f.doExist, f.Number, f.Hash
	}

	block, hash, err := rawdb.ReadFinality[T](f.db)
	if err != nil {
		log.Debug("Unable to find whitelist entry from db", "err", err)
		return false, f.Number, f.Hash
	}

	return true, block, hash
}

// Purge removes the whitelisted checkpoint
func (f *finality[T]) Purge() {
	f.Lock()
	defer f.Unlock()

	f.doExist = false
}
