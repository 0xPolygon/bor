// nolint
package eth

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rpc"
)

var (
	// errMissingCurrentBlock is returned when we don't have the current block
	// present locally.
	errMissingCurrentBlock = errors.New("current block missing")

	// errChainOutOfSync is returned when we're trying to process a future
	// checkpoint/milestone and we haven't reached at that number yet.
	errChainOutOfSync = errors.New("chain out of sync")

	// errRootHash is returned when the root hash calculation for a range of blocks fails.
	errRootHash = errors.New("root hash calculation failed")

	// errHashMismatch is returned when the local hash doesn't match
	// with the hash of checkpoint/milestone. It is the root hash of blocks
	// in case of checkpoint and is end block hash in case of milestones.
	errHashMismatch = errors.New("hash mismatch")

	// errEndBlock is returned when we're unable to fetch a block locally.
	errEndBlock = errors.New("failed to get end block")

	// errEndBlock is returned when we're unable to fetch the tip confirmation block locally.
	errTipConfirmationBlock = errors.New("failed to get tip confirmation block")

	// rewindLengthMeter for collecting info about the length of chain rewound
	rewindLengthMeter = metrics.NewRegisteredMeter("chain/autorewind/length", nil)
)

const maxRewindLen uint64 = 126

type borVerifier struct {
	verify func(ctx context.Context, eth *Ethereum, handler *ethHandler, start uint64, end uint64, hash string, isCheckpoint bool) (string, error)
}

func newBorVerifier() *borVerifier {
	return &borVerifier{borVerify}
}

func borVerify(ctx context.Context, eth *Ethereum, handler *ethHandler, start uint64, end uint64, hash string, isCheckpoint bool) (string, error) {
	str := "milestone"
	if isCheckpoint {
		str = "checkpoint"
	}

	// check if we have the given blocks
	currentBlock := eth.BlockChain().CurrentBlock()
	if currentBlock == nil {
		log.Debug(fmt.Sprintf("Failed to fetch current block from blockchain while verifying incoming %s", str))
		return hash, errMissingCurrentBlock
	}

	head := currentBlock.Number.Uint64()

	if head < end {
		log.Debug(fmt.Sprintf("Current head block behind incoming %s block", str), "head", head, "end block", end)
		return hash, errChainOutOfSync
	}

	var localHash string

	// verify the hash
	if isCheckpoint {
		var err error

		// in case of checkpoint get the rootHash
		localHash, err = handler.ethAPI.GetRootHash(ctx, start, end)

		if err != nil {
			log.Debug("Failed to calculate root hash of given block range while whitelisting checkpoint", "start", start, "end", end, "err", err)
			return hash, fmt.Errorf("%w: %v", errRootHash, err)
		}
	} else {
		// in case of milestone(isCheckpoint==false) get the hash of endBlock
		block, err := handler.ethAPI.GetBlockByNumber(ctx, rpc.BlockNumber(end), false, nil)
		if err != nil || block == nil || block["hash"] == nil {
			log.Debug("Failed to get end block hash while whitelisting milestone", "number", end, "err", err)
			return hash, fmt.Errorf("%w: %v", errEndBlock, err)
		}

		localHash = fmt.Sprintf("%v", block["hash"])[2:]
	}

	//nolint
	if localHash != hash {
		if isCheckpoint {
			log.Warn("Root hash mismatch while whitelisting checkpoint", "expected", localHash, "got", hash)
		} else {
			log.Warn("End block hash mismatch while whitelisting milestone", "expected", localHash, "got", hash)
		}

		ethHandler := (*ethHandler)(eth.handler)

		var (
			rewindTo       uint64
			rewindToSet    bool
			rewindAttested bool
		)

		attestRewindAt := func(num uint64, expectedHash common.Hash) bool {
			return num < end && eth.BlockChain().GetCanonicalHash(num) == expectedHash
		}

		// Try anchor sources in attestability order; a non-attesting first source
		// must not short-circuit later attesting ones.
		if exists, num, h := ethHandler.downloader.GetWhitelistedMilestone(); exists {
			rewindTo, rewindToSet = num, true
			rewindAttested = attestRewindAt(num, h)
			log.Info("Using existing whitelisted milestone for rewind", "block", rewindTo, "attested", rewindAttested)
		}

		if !rewindAttested && !isCheckpoint && end > 0 {
			num, attested := findCommonAncestorWithFutureMilestones(eth, start, end, hash)
			// Race: findCommonAncestor's re-read of local-at-end may disagree
			// with the outer mismatch check and yield (end, true).
			if attested && num < end {
				rewindTo, rewindToSet, rewindAttested = num, true, true
				log.Info("Using future-milestone match for rewind", "block", rewindTo)
			} else if !rewindToSet {
				rewindTo, rewindToSet = num, true
			}
		}

		if !rewindToSet {
			// Checkpoint hash is a merkle root, never attests; used only for the
			// refusal-log rewindTo.
			if exists, num, _ := ethHandler.downloader.GetWhitelistedCheckpoint(); exists {
				rewindTo = num
				log.Info("Using existing whitelisted checkpoint for rewind", "block", rewindTo)
			} else if start > 0 {
				rewindTo = start - 1
			}
		}

		if head-rewindTo > maxRewindLen {
			rewindTo = head - maxRewindLen
			rewindAttested = false // clamped target is not the attested block
		}

		if isCheckpoint {
			log.Info("Rewinding chain due to checkpoint root hash mismatch", "number", rewindTo)
		} else {
			log.Info("Rewinding chain due to milestone endblock hash mismatch", "number", rewindTo)
		}

		var canonicalChain []*types.Block
		length := end - rewindTo

		// Only milestones provide a block-hash anchor (end block hash).
		// For checkpoints, `hash` is a merkle root, not a block hash; attempting to
		// resolve it via GetBlocksFromHash is always invalid.
		if !isCheckpoint {
			canonicalChain = eth.BlockChain().GetBlocksFromHash(
				common.HexToHash(hash),
				int(length))
		} else {
			canonicalChain = nil
		}

		// Reverse the canonical chain
		for i, j := 0, len(canonicalChain)-1; i < j; i, j = i+1, j-1 {
			canonicalChain[i], canonicalChain[j] = canonicalChain[j], canonicalChain[i]
		}

		if len(canonicalChain) != int(length) {
			canonicalChain = nil
		}

		if len(canonicalChain) > 0 {
			if reorgToFinalized(eth, head, rewindTo, canonicalChain) {
				ethHandler.downloader.PurgeMilestonesAfter(rewindTo)
			}
			return hash, errHashMismatch
		}

		// No canonical sidechain locally. Recoverable for milestones: SetHead to
		// an attested ancestor drops the bad-fork tip the downloader rejects as
		// "sidechain ghost-state attack", letting canonical resync proceed.
		// maxRewindLen keeps the target inside the in-memory trie window.
		if !isCheckpoint && rewindAttested && head-rewindTo <= maxRewindLen {
			log.Warn("Milestone mismatch: rewinding to attested canonical ancestor without local sidechain; canonical chain will resync from peers",
				"head", head, "rewindTo", rewindTo, "start", start, "end", end)

			defer pauseMiner(eth)()
			if err := rewind(eth, head, rewindTo); err != nil {
				// Chain still on bad fork; keep whitelist intact rather than weakening.
				return hash, errHashMismatch
			}
			// Stale future-fork entries would reject canonical peers via
			// IsValidChain / IsReorgAllowed / IsFutureMilestoneCompatible.
			ethHandler.downloader.PurgeMilestonesAfter(rewindTo)
			return hash, errHashMismatch
		}

		if isCheckpoint {
			log.Warn("Checkpoint mismatch: refusing to rewind without a canonical chain segment",
				"head", head, "rewindTo", rewindTo, "start", start, "end", end)
		} else {
			log.Warn("Milestone mismatch: refusing to rewind without a canonical chain segment",
				"head", head, "rewindTo", rewindTo, "start", start, "end", end,
				"attested", rewindAttested)
		}
		return hash, errHashMismatch
	}

	// fetch the end block hash
	block, err := handler.ethAPI.GetBlockByNumber(ctx, rpc.BlockNumber(end), false, nil)
	if err != nil {
		log.Debug("Failed to get end block hash while whitelisting", "err", err)
		return hash, fmt.Errorf("%w: %v", errEndBlock, err)
	}

	hash = fmt.Sprintf("%v", block["hash"])

	return hash, nil
}

// reorgToFinalized rewinds head to rewindTo and inserts canonicalChain. Returns
// true if head actually moved (the insert is best-effort and a partial insert
// still counts).
func reorgToFinalized(eth *Ethereum, head uint64, rewindTo uint64, canonicalChain []*types.Block) bool {
	if len(canonicalChain) == 0 {
		log.Warn("Refusing to reorg finalized without canonical chain",
			"head", head, "rewindTo", rewindTo)
		return false
	}

	defer pauseMiner(eth)()

	if err := rewind(eth, head, rewindTo); err != nil {
		return false
	}
	insertFinalized(eth, canonicalChain)
	return true
}

// pauseMiner stops the miner and returns a restart function for the caller to
// defer; the restart fires at the caller's return, not pauseMiner's.
func pauseMiner(eth *Ethereum) func() {
	if eth.Miner() == nil || !eth.Miner().Mining() {
		return func() {}
	}
	ch := make(chan struct{})
	eth.Miner().Stop(ch)
	<-ch
	return func() { eth.Miner().Start() }
}

// rewind cancels in-flight downloads and SetHeads to rewindTo. Callers must
// check the error before any downstream insert or whitelist purge.
func rewind(eth *Ethereum, head uint64, rewindTo uint64) error {
	eth.handler.downloader.Cancel()
	if err := eth.blockchain.SetHead(rewindTo); err != nil {
		log.Error("Error while rewinding the chain", "to", rewindTo, "err", err)
		return err
	}
	rewindLengthMeter.Mark(int64(head - rewindTo))
	return nil
}

// findCommonAncestorWithFutureMilestones returns a candidate rewind anchor.
// The bool is true only when local hash at the returned block matches a
// stored milestone hash; callers must not blind-rewind on (_, false).
func findCommonAncestorWithFutureMilestones(eth *Ethereum, start uint64, end uint64, milestoneEndHash string) (uint64, bool) {
	// Start from the milestone start block and work backwards
	// to find where our chain matches the expected chain
	if start == 0 {
		return 0, false
	}

	blockchain := eth.BlockChain()
	db := eth.ChainDb()

	// First, check if we have the correct milestone end block
	localBlock := blockchain.GetBlockByNumber(end)
	if localBlock != nil {
		localHash := localBlock.Hash().Hex()[2:]
		if localHash == milestoneEndHash {
			return end, true
		}
	}

	// We have a fork. Need to find the actual fork point by checking past milestones
	log.Info("Local chain forked from milestone", "milestone_end", end, "expected_hash", milestoneEndHash)

	// Read future milestone list from database
	// These are milestones that haven't been processed yet but are stored
	futureMilestoneOrder, futureMilestoneList, err := rawdb.ReadFutureMilestoneList(db)
	targetBlock := start - 1
	if err == nil && len(futureMilestoneOrder) > 0 {
		// Check future milestones from newest to oldest
		for i := len(futureMilestoneOrder) - 1; i >= 0; i-- {
			milestoneNum := futureMilestoneOrder[i]
			if milestoneNum >= end {
				continue
			}

			milestoneHash := futureMilestoneList[milestoneNum]
			localBlock := blockchain.GetBlockByNumber(milestoneNum)
			if localBlock != nil && localBlock.Hash() == milestoneHash {
				log.Info("Found matching future milestone", "block", milestoneNum, "hash", milestoneHash)
				return milestoneNum, true
			}

			if milestoneNum > 0 && milestoneNum < targetBlock {
				targetBlock = milestoneNum - 1
			}
		}
	}

	return targetBlock, false
}

// insertFinalized inserts the chain finalized by checkpoint/milestone and ensures the final block is set as canonical.
func insertFinalized(eth *Ethereum, canonicalChain []*types.Block) {
	if len(canonicalChain) == 0 {
		return
	}

	// Perform the canonical chain insertion
	log.Info("Inserting canonical chain", "from", canonicalChain[0].NumberU64(), "hash", canonicalChain[0].Hash(), "to", canonicalChain[len(canonicalChain)-1].NumberU64(), "hash", canonicalChain[len(canonicalChain)-1].Hash())
	_, err := eth.BlockChain().InsertChain(canonicalChain, eth.config.SyncAndProduceWitnesses)
	if err != nil {
		log.Warn("Failed to insert canonical chain", "err", err)
		return
	}

	// Then explicitly set the final block as canonical to ensure proper canonical mapping
	finalBlock := canonicalChain[len(canonicalChain)-1]
	_, err = eth.BlockChain().SetCanonical(finalBlock)
	if err != nil {
		log.Warn("Failed to set canonical head after insertion", "err", err, "block", finalBlock.NumberU64(), "hash", finalBlock.Hash())
		return
	}

	log.Info("Successfully inserted canonical chain")
}
