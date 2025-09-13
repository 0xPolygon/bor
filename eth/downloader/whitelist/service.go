package whitelist

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrMismatch = errors.New("mismatch error")
	ErrNoRemote = errors.New("remote peer doesn't have a target block number")

	ErrCheckpointMismatch = errors.New("checkpoint mismatch")
	ErrLongFutureChain    = errors.New("received future chain of unacceptable length")
	ErrNoRemoteCheckpoint = errors.New("remote peer doesn't have a checkpoint")
)

var (
	// maxForkCorrectnessLimit defines the maximum number of blocks to iterate backwards
	// in db for checking fork correctness instead of blindly accepting the chain.
	maxForkCorrectnessLimit = uint64(256)
)

type Service struct {
	db ethdb.Database
	checkpointService
	milestoneService
}

func NewService(db ethdb.Database) *Service {
	// Fetch last whitelisted checkpoint entry from db. Ignore in case of error or if
	// the whitelisted entry has empty hash.
	var checkpointDoExist = true
	checkpointNumber, checkpointHash, err := rawdb.ReadFinality[*rawdb.Checkpoint](db)
	if err != nil || checkpointHash == (common.Hash{}) {
		checkpointDoExist = false
	}

	// Fetch last whitelisted milestone entry from db. Ignore in case of error or if
	// the whitelisted entry has empty hash.
	var milestoneDoExist = true
	milestoneNumber, milestoneHash, err := rawdb.ReadFinality[*rawdb.Milestone](db)
	if err != nil || milestoneHash == (common.Hash{}) {
		milestoneDoExist = false
	}

	locked, lockedMilestoneNumber, lockedMilestoneHash, lockedMilestoneIDs, err := rawdb.ReadLockField(db)
	if err != nil || !locked {
		locked = false
		lockedMilestoneIDs = make(map[string]struct{})
	}

	order, list, err := rawdb.ReadFutureMilestoneList(db)
	if err != nil {
		order = make([]uint64, 0)
		list = make(map[uint64]common.Hash)
	}

	return &Service{
		db,
		&checkpoint{
			finality[*rawdb.Checkpoint]{
				doExist:  checkpointDoExist,
				Number:   checkpointNumber,
				Hash:     checkpointHash,
				interval: 256,
				db:       db,
				name:     "checkpoint",
			},
		},

		&milestone{
			finality: finality[*rawdb.Milestone]{
				doExist:  milestoneDoExist,
				Number:   milestoneNumber,
				Hash:     milestoneHash,
				interval: 256,
				db:       db,
				name:     "milestone",
			},

			Locked:                locked,
			LockedMilestoneNumber: lockedMilestoneNumber,
			LockedMilestoneHash:   lockedMilestoneHash,
			LockedMilestoneIDs:    lockedMilestoneIDs,
			FutureMilestoneList:   list,
			FutureMilestoneOrder:  order,
			MaxCapacity:           10,
			blockchain:            nil, // Will be set after blockchain creation
		},
	}
}

// SetBlockchain sets the blockchain reference for the milestone service
func (s *Service) SetBlockchain(blockchain ChainReader) {
	if milestone, ok := s.milestoneService.(*milestone); ok {
		milestone.blockchain = blockchain
	}
}

// IsValidPeer checks if the chain we're about to receive from a peer is valid or not
// in terms of reorgs. We won't reorg beyond the last bor checkpoint submitted to mainchain and last milestone voted in the heimdall
func (s *Service) IsValidPeer(fetchHeadersByNumber func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error)) (bool, error) {
	checkpointBool, err := s.checkpointService.IsValidPeer(fetchHeadersByNumber)
	if !checkpointBool {
		return checkpointBool, err
	}

	milestoneBool, err := s.milestoneService.IsValidPeer(fetchHeadersByNumber)
	if !milestoneBool {
		return milestoneBool, err
	}

	return true, nil
}

func (s *Service) PurgeWhitelistedCheckpoint() {
	s.checkpointService.Purge()
}

func (s *Service) PurgeWhitelistedMilestone() {
	s.milestoneService.Purge()
}

func (s *Service) GetWhitelistedCheckpoint() (bool, uint64, common.Hash) {
	return s.checkpointService.Get()
}

func (s *Service) GetWhitelistedMilestone() (bool, uint64, common.Hash) {
	return s.milestoneService.Get()
}

func (s *Service) ProcessMilestone(endBlockNum uint64, endBlockHash common.Hash) {
	s.milestoneService.Process(endBlockNum, endBlockHash)
}

func (s *Service) ProcessCheckpoint(endBlockNum uint64, endBlockHash common.Hash) {
	s.checkpointService.Process(endBlockNum, endBlockHash)
}

func (s *Service) IsValidChain(currentHeader *types.Header, chain []*types.Header) (bool, error) {
	checkpointBool, err := s.checkpointService.IsValidChain(currentHeader, chain)
	if !checkpointBool {
		return checkpointBool, err
	}

	milestoneBool, err := s.milestoneService.IsValidChain(currentHeader, chain)
	if !milestoneBool {
		return milestoneBool, err
	}

	// Check for fork correctness
	if len(chain) > 0 {
		return s.checkForkCorrectness(chain[0]), nil
	}

	return true, nil
}

// checkForkCorrectness checks if the chain being imported belongs to the correct fork
// by iterating backwards until the last whitelisted milestone or checkpoint. This helps
// in cases where the chain being imported doesn't overlap with any whitelisted entry and
// we have to blindly accept it.
func (s *Service) checkForkCorrectness(firstBlock *types.Header) bool {
	if firstBlock == nil {
		return true
	}
	headerNumber := firstBlock.Number.Uint64()

	// Fetch the latest whitelisted entry
	doExist, number, hash := s.milestoneService.Get()
	if !doExist {
		return true
	}
	// TODO: add checkpoint here

	// Blind accept the chain if we've to iterate more than `maxForkCorrectnessLimit` blocks
	if headerNumber-number > maxForkCorrectnessLimit {
		log.Debug("Skipping fork correctness check as block is too far ahead", "block", headerNumber, "last whitelisted", number)
		return true
	}

	// Only perform checks if first block being imported is ahead of last whitelisted entry. We can
	// skip checking for fork correctness otherwise as chain would be already validated before.
	if headerNumber > number {
		parentNumber := headerNumber - 1
		parentHash := firstBlock.ParentHash
		for {
			// Fetch the parent block by number and hash
			header := rawdb.ReadHeader(s.db, parentHash, parentNumber)
			if header == nil {
				// This shouldn't happen as the db should have all blocks before the first block
				// Log the issue and accept blindly (falling back to previous behaviour instead of rejecting)
				log.Warn("Missing parent block while checking fork correctness", "number", parentNumber, "hash", parentHash, "import block", headerNumber, "import hash", firstBlock.Hash())
				return true
			}

			// Check if we're at the whitelisted block
			if header.Number.Uint64() == number {
				// Check if hash matches and return
				return header.Hash() == hash
			}

			// Header not reached yet, move to parent
			parentHash = header.ParentHash
			parentNumber--
		}
	}

	return true
}

func (s *Service) GetMilestoneIDsList() []string {
	return s.milestoneService.GetMilestoneIDsList()
}

func splitChain(current uint64, chain []*types.Header) ([]*types.Header, []*types.Header) {
	var (
		pastChain   []*types.Header
		futureChain []*types.Header
		first       = chain[0].Number.Uint64()
		last        = chain[len(chain)-1].Number.Uint64()
	)

	if current >= first {
		if len(chain) == 1 || current >= last {
			pastChain = chain
		} else {
			pastChain = chain[:current-first+1]
		}
	}

	if current < last {
		if len(chain) == 1 || current < first {
			futureChain = chain
		} else {
			futureChain = chain[current-first+1:]
		}
	}

	return pastChain, futureChain
}

//nolint:unparam
func isValidChain(currentHeader *types.Header, chain []*types.Header, doExist bool, number uint64, hash common.Hash) (bool, error) {
	// Check if we have milestone to validate incoming chain in memory
	if !doExist {
		// We don't have any entry, no additional validation will be possible
		return true, nil
	}

	current := currentHeader.Number.Uint64()

	// Check if imported chain is less than whitelisted number
	if chain[len(chain)-1].Number.Uint64() < number {
		if current >= number { //If current tip of the chain is greater than whitelist number then return false
			return false, nil
		} else {
			return true, nil
		}
	}

	// Split the chain into past and future chain
	pastChain, _ := splitChain(current, chain)

	// Iterate over the chain and validate against the last milestone
	// It will handle all cases when the incoming chain has at least one milestone
	for i := len(pastChain) - 1; i >= 0; i-- {
		if pastChain[i].Number.Uint64() == number {
			res := pastChain[i].Hash() == hash

			return res, nil
		}
	}

	return true, nil
}

// FIXME: remoteHeader is not used
func isValidPeer(fetchHeadersByNumber func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error), doExist bool, number uint64, hash common.Hash) (bool, error) {
	// Check for availability of the last milestone block.
	// This can be also be empty if our heimdall is not responding
	// or we're running without it.
	if !doExist {
		// worst case, we don't have the milestone in memory
		return true, nil
	}

	// todo: we can extract this as an interface and mock as well or just test IsValidChain in isolation from downloader passing fake fetchHeadersByNumber functions
	headers, hashes, err := fetchHeadersByNumber(number, 1, 0, false)
	if err != nil {
		return false, fmt.Errorf("%w: last whitelisted block number %d, err %v", ErrNoRemote, number, err)
	}

	if len(headers) == 0 {
		return false, fmt.Errorf("%w: last whitlisted block number %d", ErrNoRemote, number)
	}

	reqBlockNum := headers[0].Number.Uint64()
	reqBlockHash := hashes[0]

	// Check against the whitelisted blocks
	if reqBlockNum == number && reqBlockHash == hash {
		return true, nil
	}

	return false, ErrMismatch
}
