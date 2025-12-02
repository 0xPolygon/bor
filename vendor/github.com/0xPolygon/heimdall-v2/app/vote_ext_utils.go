package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"cosmossdk.io/log"
	abciTypes "github.com/cometbft/cometbft/abci/types"
	cmtCrypto "github.com/cometbft/cometbft/crypto"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/protoio"
	cmtTypes "github.com/cometbft/cometbft/proto/tendermint/types"
	cometTypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/metrics/consensus"
	"github.com/0xPolygon/heimdall-v2/sidetxs"
	chainManagerKeeper "github.com/0xPolygon/heimdall-v2/x/chainmanager/keeper"
	checkpointKeeper "github.com/0xPolygon/heimdall-v2/x/checkpoint/keeper"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	milestoneAbci "github.com/0xPolygon/heimdall-v2/x/milestone/abci"
	milestoneKeeper "github.com/0xPolygon/heimdall-v2/x/milestone/keeper"
	stakeKeeper "github.com/0xPolygon/heimdall-v2/x/stake/keeper"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// ValidateVoteExtensions verifies the vote extension correctness
// It checks the signature of each vote extension with its signer's public key
// Also, it checks if the vote extensions are enabled, valid and have >2/3 voting power
// It returns an error in case the validation fails
func ValidateVoteExtensions(ctx sdk.Context, reqHeight int64, extVoteInfo []abciTypes.ExtendedVoteInfo, round int32, validatorSet *stakeTypes.ValidatorSet, milestoneKeeper milestoneKeeper.Keeper) error {
	// check if VEs are enabled
	if err := checkIfVoteExtensionsDisabled(ctx, reqHeight+1); err != nil {
		return err
	}

	// check if reqHeight is the initial height
	if reqHeight <= retrieveVoteExtensionsEnableHeight(ctx) {
		if len(extVoteInfo) != 0 {
			return fmt.Errorf("non-empty VEs received at initial height %d", reqHeight)
		}
		return nil
	}

	totalVotingPower := validatorSet.GetTotalVotingPower()
	sumVP := int64(0)

	// Map to track seen validator addresses
	seenValidators := make(map[string]struct{})
	var blockHash []byte

	ac := address.HexCodec{}

	for _, vote := range extVoteInfo {

		// make sure the BlockIdFlag is valid
		if !isBlockIdFlagValid(vote.BlockIdFlag) {
			return fmt.Errorf("received vote with invalid block ID %s flag at height %d", vote.BlockIdFlag.String(), reqHeight)
		}
		// if not BlockIDFlagCommit, skip that vote, as it doesn't have relevant information
		if vote.BlockIdFlag != cmtTypes.BlockIDFlagCommit {
			continue
		}

		valAddrStr, err := ac.BytesToString(vote.Validator.Address)
		if err != nil {
			return fmt.Errorf("validator address %v is not valid", vote.Validator.Address)
		}

		if len(vote.ExtensionSignature) == 0 {
			return fmt.Errorf("received empty vote extension signature at height %d from validator %s", reqHeight, valAddrStr)
		}

		voteExtension := new(sidetxs.VoteExtension)
		if err = voteExtension.Unmarshal(vote.VoteExtension); err != nil {
			return fmt.Errorf("error while unmarshalling vote extension: %w", err)
		}

		if voteExtension.Height != reqHeight-1 {
			return fmt.Errorf("invalid height received for vote extension, expected %d, got %d", reqHeight-1, voteExtension.Height)
		}

		// blockHash consistency check
		if blockHash == nil {
			// store the block hash from the first vote
			blockHash = voteExtension.BlockHash
		} else {
			// compare the current block hash with the stored block hash
			if !bytes.Equal(blockHash, voteExtension.BlockHash) {
				return fmt.Errorf("invalid block hash found for vote extension, expected %s, received %s, validator %s",
					common.Bytes2Hex(blockHash), common.Bytes2Hex(voteExtension.BlockHash), valAddrStr)
			}
		}

		txHash, err := validateSideTxResponses(voteExtension.SideTxResponses)
		if err != nil {
			return fmt.Errorf("invalid sideTxResponses detected for validator %s and tx %s, error: %w", valAddrStr, common.Bytes2Hex(txHash), err)
		}

		if err := milestoneAbci.ValidateMilestoneProposition(ctx, &milestoneKeeper, voteExtension.MilestoneProposition); err != nil {
			return fmt.Errorf("invalid milestone proposition detected for validator %s, error: %w", valAddrStr, err)
		}

		// Check for duplicate votes by the same validator
		if _, found := seenValidators[valAddrStr]; found {
			return fmt.Errorf("duplicate vote detected from validator %s at height %d", valAddrStr, reqHeight)
		}
		// Add validator address to the map
		seenValidators[valAddrStr] = struct{}{}

		_, validator := validatorSet.GetByAddress(valAddrStr)
		if validator == nil {
			if milestoneAbci.ShouldErrorOnValidatorNotFound(ctx.BlockHeight()) {
				return fmt.Errorf("failed to get validator %s", valAddrStr)
			}
			continue
		}

		cmtPubKey, err := getValidatorPublicKey(validator)
		if err != nil {
			return err
		}

		cve := cmtTypes.CanonicalVoteExtension{
			Extension: vote.VoteExtension,
			Height:    reqHeight - 1, // the vote extension was signed in the previous height
			Round:     int64(round),
			ChainId:   ctx.ChainID(),
		}

		marshalDelimitedFn := func(msg proto.Message) ([]byte, error) {
			var buf bytes.Buffer
			if _, err := protoio.NewDelimitedWriter(&buf).WriteMsg(msg); err != nil {
				return nil, err
			}

			return buf.Bytes(), nil
		}

		extSignBytes, err := marshalDelimitedFn(&cve)
		if err != nil {
			return fmt.Errorf("failed to encode CanonicalVoteExtension: %w", err)
		}

		if !cmtPubKey.VerifySignature(extSignBytes, vote.ExtensionSignature) {
			return fmt.Errorf("failed to verify validator %X vote extension signature", valAddrStr)
		}

		sumVP += validator.VotingPower
	}

	// Ensure we have at least 2/3 voting power for the submitted vote extensions in each side tx
	majorityVP := totalVotingPower * 2 / 3
	if sumVP <= majorityVP {
		return fmt.Errorf("insufficient cumulative voting power received to verify vote extensions; got: %d, expected: >=%d", sumVP, majorityVP)
	}

	return nil
}

// tallyVotes tallies the votes received for the side tx
// It returns the lists of txs which got >2/3+ YES, NO and UNSPECIFIED votes respectively
func tallyVotes(extVoteInfo []abciTypes.ExtendedVoteInfo, logger log.Logger, totalVotingPower int64, currentHeight int64) ([][]byte, [][]byte, [][]byte, error) {
	logger.Debug("Tallying votes")

	voteByTxHash, err := aggregateVotes(extVoteInfo, currentHeight, logger)
	if err != nil {
		return nil, nil, nil, err
	}

	// check for vote majority
	txHashList := make([]string, 0, len(voteByTxHash))
	for txHash := range voteByTxHash {
		txHashList = append(txHashList, txHash)
	}

	sort.Strings(txHashList)

	approvedTxs, rejectedTxs, skippedTxs := make([][]byte, 0, len(txHashList)), make([][]byte, 0, len(txHashList)), make([][]byte, 0, len(txHashList))

	majorityVP := totalVotingPower * 2 / 3

	for _, txHash := range txHashList {
		voteMap := voteByTxHash[txHash]

		// calculate the total voting power in the voteMap
		power := voteMap[sidetxs.Vote_VOTE_YES] + voteMap[sidetxs.Vote_VOTE_NO] + voteMap[sidetxs.Vote_UNSPECIFIED]

		// ensure the total votes do not exceed the total voting power
		if shouldCheckVotingPower(currentHeight) && power > totalVotingPower {
			logger.Error("the votes power exceeds the total voting power", "txHash", txHash, "power", power, "totalVotingPower", totalVotingPower)
			return nil, nil, nil, fmt.Errorf("votes power %d exceeds total voting power %d for txHash %s", power, totalVotingPower, txHash)
		}

		if voteMap[sidetxs.Vote_VOTE_YES] > majorityVP {
			// approved
			logger.Debug("Approved side-tx", "txHash", txHash)
			consensus.RecordConsensusApproved()
			// append to approved tx slice
			approvedTxs = append(approvedTxs, common.FromHex(txHash))
		} else if voteMap[sidetxs.Vote_VOTE_NO] > majorityVP {
			// rejected
			logger.Debug("Rejected side-tx", "txHash", txHash)
			consensus.RecordConsensusRejected()
			// append to rejected tx slice
			rejectedTxs = append(rejectedTxs, common.FromHex(txHash))
		} else {
			// skipped - 2/3 consensus not reached.
			logger.Debug("Skipped side-tx", "txHash", txHash)
			consensus.RecordConsensusFailure()
			// append to rejected tx slice
			skippedTxs = append(skippedTxs, common.FromHex(txHash))
		}
	}

	logger.Debug(fmt.Sprintf("Height %d: approved %d txs, rejected %d txs, skipped %d txs. ", currentHeight, len(approvedTxs), len(rejectedTxs), len(skippedTxs)))

	// HV2: currently, there is no functional difference between a tc being rejected or skipped (only used for debugging)
	return approvedTxs, rejectedTxs, skippedTxs, nil
}

// aggregateVotes collates votes received for side txs
func aggregateVotes(extVoteInfo []abciTypes.ExtendedVoteInfo, currentHeight int64, logger log.Logger) (map[string]map[sidetxs.Vote]int64, error) {
	voteByTxHash := make(map[string]map[sidetxs.Vote]int64)  // track votes for a side tx
	validatorToTxMap := make(map[string]map[string]struct{}) // ensure a validator doesn't procure conflicting votes for a side tx
	var blockHash []byte                                     // store the block hash to make sure all votes are for the same block

	for _, vote := range extVoteInfo {
		// make sure the BlockIdFlag is valid
		if !isBlockIdFlagValid(vote.BlockIdFlag) {
			return nil, fmt.Errorf("received vote with invalid block ID %s flag at height %d", vote.BlockIdFlag.String(), currentHeight-1)
		}
		// if not BlockIDFlagCommit, skip that vote, as it doesn't have relevant information
		if vote.BlockIdFlag != cmtTypes.BlockIDFlagCommit {
			continue
		}

		ve := new(sidetxs.VoteExtension)
		err := ve.Unmarshal(vote.VoteExtension)
		if err != nil {
			return nil, err
		}

		if ve.Height != currentHeight-1 {
			return nil, fmt.Errorf("invalid height received for vote extension, VeHeight should match CurrentHeight-1. VeHeight: %d, CurrentHeight: %d", ve.Height, currentHeight)
		}

		// blockHash consistency check
		if blockHash == nil {
			// store the block hash from the first vote
			blockHash = ve.BlockHash
		} else {
			ac := address.HexCodec{}
			valAddr, err := ac.BytesToString(vote.Validator.Address)
			if err != nil {
				return nil, err
			}
			// compare the current block hash with the stored block hash
			if !bytes.Equal(blockHash, ve.BlockHash) {
				logger.Error("invalid block hash found for vote extension",
					"expectedBlockHash", common.Bytes2Hex(blockHash),
					"receivedBlockHash", common.Bytes2Hex(ve.BlockHash),
					"validator", valAddr)
				return nil, fmt.Errorf("mismatching block hash for vote extension from validator %s", valAddr)
			}
		}

		addr, err := address.NewHexCodec().BytesToString(vote.Validator.Address)
		if err != nil {
			return nil, err
		}

		if validatorToTxMap[addr] != nil {
			return nil, fmt.Errorf("duplicate vote received from %s", addr)
		}
		validatorToTxMap[addr] = make(map[string]struct{})

		// iterate through vote extensions and accumulate voting power for YES/NO/UNSPECIFIED votes
		for _, res := range ve.SideTxResponses {
			txHashStr := common.Bytes2Hex(res.TxHash)

			if _, hasVoted := validatorToTxMap[addr][txHashStr]; hasVoted {
				logger.Error("multiple votes received for side tx",
					"txHash", txHashStr, "validatorAddress", addr)
				return nil, fmt.Errorf("multiple votes received for side tx %s from validator %s", txHashStr, addr)
			}

			if !isVoteValid(res.Result) {
				return nil, fmt.Errorf("invalid vote %v received for side tx %s", res.Result, txHashStr)
			}

			if voteByTxHash[txHashStr] == nil {
				voteByTxHash[txHashStr] = make(map[sidetxs.Vote]int64)
			}

			voteByTxHash[txHashStr][res.Result] += vote.Validator.Power

			// validator's vote received; mark it to avoid duplicated votes
			validatorToTxMap[addr][txHashStr] = struct{}{}
		}

	}

	return voteByTxHash, nil
}

// validateSideTxResponses validates the SideTxResponses and returns the txHash of the first invalid tx detected, plus the error
func validateSideTxResponses(sideTxResponses []sidetxs.SideTxResponse) ([]byte, error) {
	// track votes of the validator
	txVoteMap := make(map[string]struct{})

	if len(sideTxResponses) > maxSideTxResponsesCount {
		return nil, fmt.Errorf("too many side tx responses received: %d, max: %d", len(sideTxResponses), maxSideTxResponsesCount)
	}

	for _, res := range sideTxResponses {
		// check txHash is well-formed
		if len(res.TxHash) != common.HashLength {
			return res.TxHash, fmt.Errorf("invalid tx hash received: %s", common.Bytes2Hex(res.TxHash))
		}

		if !isVoteValid(res.Result) {
			return res.TxHash, fmt.Errorf("invalid vote result type %v received for side tx %s", res.Result, common.Bytes2Hex(res.TxHash))
		}

		// check if the validator has already voted for the side tx
		if _, found := txVoteMap[string(res.TxHash)]; found {
			return res.TxHash, fmt.Errorf("duplicated votes detected for side tx %s", common.Bytes2Hex(res.TxHash))
		}

		txVoteMap[string(res.TxHash)] = struct{}{}
	}

	return nil, nil
}

const maxSideTxResponsesCount = 50

// checkIfVoteExtensionsDisabled indicates whether the proposer must include VEs from previous height in the block proposal as a special transaction.
// Since we are using a hard fork approach for the heimdall migration, VEs will be enabled from v2 genesis' initial height (v1 last height +1).
// Returning error will result in CometBFT panic.
func checkIfVoteExtensionsDisabled(ctx sdk.Context, height int64) error {
	// voteExtensionsEnableHeight is the height from which the vote extensions are enabled, and it's (v1_last_height +1)
	voteExtensionsEnableHeight := retrieveVoteExtensionsEnableHeight(ctx)
	if voteExtensionsEnableHeight == 0 {
		return errors.New("VoteExtensions are disabled: VoteExtensionsEnableHeight is set to 0")
	}
	if height < voteExtensionsEnableHeight {
		return fmt.Errorf("vote extensions are disabled: current height is %d, and VoteExtensionsEnableHeight is set to %d", height, voteExtensionsEnableHeight)
	}
	return nil
}

func isVoteValid(v sidetxs.Vote) bool {
	return v == sidetxs.Vote_UNSPECIFIED || v == sidetxs.Vote_VOTE_YES || v == sidetxs.Vote_VOTE_NO
}

func isBlockIdFlagValid(flag cmtTypes.BlockIDFlag) bool {
	return flag == cmtTypes.BlockIDFlagAbsent || flag == cmtTypes.BlockIDFlagCommit || flag == cmtTypes.BlockIDFlagNil
}

// retrieveVoteExtensionsEnableHeight returns the height from which the vote extensions are enabled, which is equal to initial height of the v2 genesis
func retrieveVoteExtensionsEnableHeight(ctx sdk.Context) int64 {
	consensusParams := ctx.ConsensusParams()
	return consensusParams.GetAbci().GetVoteExtensionsEnableHeight()
}

// GetDummyNonRpVoteExtension returns a dummy non-rp vote extension for given height and chain id
func GetDummyNonRpVoteExtension(height int64, chainID string) ([]byte, error) {
	var buf bytes.Buffer

	writtenBytes, err := buf.Write(dummyNonRpVoteExtension)
	if err != nil {
		return nil, err
	}
	if writtenBytes != len(dummyNonRpVoteExtension) {
		return nil, errors.New("failed to write dummy vote extension")
	}
	if err := buf.WriteByte('|'); err != nil {
		return nil, err
	}

	if err := binary.Write(&buf, binary.BigEndian, height); err != nil {
		return nil, err
	}
	if err := buf.WriteByte('|'); err != nil {
		return nil, err
	}

	writtenBytes, err = buf.WriteString(chainID)
	if err != nil {
		return nil, err
	}
	if writtenBytes != len(chainID) {
		return nil, errors.New("failed to write chainID")
	}

	return buf.Bytes(), nil
}

// ValidateNonRpVoteExtensions validates the non-rp vote extensions
func ValidateNonRpVoteExtensions(
	ctx sdk.Context,
	height int64,
	extVoteInfo []abciTypes.ExtendedVoteInfo,
	validatorSet *stakeTypes.ValidatorSet,
	chainManagerKeeper chainManagerKeeper.Keeper,
	checkpointKeeper checkpointKeeper.Keeper,
	contractCaller helper.IContractCaller,
	logger log.Logger,
) error {
	if height <= retrieveVoteExtensionsEnableHeight(ctx) {
		return nil
	}

	// Check if there is vote extension with majority voting power
	majorityExt, err := getMajorityNonRpVoteExtension(ctx, extVoteInfo, validatorSet, logger)
	if err != nil {
		return err
	}

	if err := ValidateNonRpVoteExtension(ctx, height-1, majorityExt, chainManagerKeeper, checkpointKeeper, contractCaller); err != nil {
		return fmt.Errorf("failed to validate majority non rp vote extension: %w", err)
	}

	// Check the signatures
	if err := checkNonRpVoteExtensionsSignatures(ctx, extVoteInfo, validatorSet); err != nil {
		return fmt.Errorf("failed to check non rp vote extensions signatures: %w", err)
	}

	return nil
}

// ValidateNonRpVoteExtension validates the non-rp vote extension
func ValidateNonRpVoteExtension(
	ctx sdk.Context,
	height int64,
	extension []byte,
	chainManagerKeeper chainManagerKeeper.Keeper,
	checkpointKeeper checkpointKeeper.Keeper,
	contractCaller helper.IContractCaller,
) error {
	if len(extension) > maxNonRpVoteExtensionSize {
		return fmt.Errorf("non-rp vote extension size is too large: %d, max: %d", len(extension), maxNonRpVoteExtensionSize)
	}

	// Check if its dummy vote non rp extension
	dummyExt, err := GetDummyNonRpVoteExtension(height, ctx.ChainID())
	if err != nil {
		return err
	}

	if bytes.Equal(extension, dummyExt) {
		// This is dummy vote extension, we have nothing else to check
		return nil
	}

	// Check if valid checkpoint data
	if err := validateCheckpointMsgData(ctx, extension, chainManagerKeeper, checkpointKeeper, contractCaller); err != nil {
		return fmt.Errorf("failed to validate checkpoint msg data: %w", err)
	}

	return nil
}

const maxNonRpVoteExtensionSize = 500

// checkNonRpVoteExtensionsSignatures checks the signatures of the non-rp vote extensions
func checkNonRpVoteExtensionsSignatures(ctx sdk.Context, extVoteInfo []abciTypes.ExtendedVoteInfo, validatorSet *stakeTypes.ValidatorSet) error {
	ac := address.HexCodec{}

	for _, vote := range extVoteInfo {
		// if not BlockIDFlagCommit, skip that vote, as it doesn't have relevant information
		if vote.BlockIdFlag != cmtTypes.BlockIDFlagCommit {
			continue
		}

		valAddr, err := ac.BytesToString(vote.Validator.Address)
		if err != nil {
			return err
		}

		_, validator := validatorSet.GetByAddress(valAddr)
		if validator == nil {
			if milestoneAbci.ShouldErrorOnValidatorNotFound(ctx.BlockHeight()) {
				return fmt.Errorf("failed to get validator %s", valAddr)
			}
			continue
		}

		cmtPubKey, err := getValidatorPublicKey(validator)
		if err != nil {
			return err
		}

		if !cmtPubKey.VerifySignature(vote.NonRpVoteExtension, vote.NonRpExtensionSignature) {
			return fmt.Errorf("failed to verify validator %X vote extension signature", valAddr)
		}
	}

	return nil
}

// getMajorityNonRpVoteExtension returns the non-rp vote extension with the majority voting power
func getMajorityNonRpVoteExtension(ctx sdk.Context, extVoteInfo []abciTypes.ExtendedVoteInfo, validatorSet *stakeTypes.ValidatorSet, logger log.Logger) ([]byte, error) {
	ac := address.HexCodec{}

	hashToExt := make(map[string][]byte)
	hashToVotingPower := make(map[string]int64)

	for _, vote := range extVoteInfo {
		// if not BlockIDFlagCommit, skip that vote, as it doesn't have relevant information
		if vote.BlockIdFlag != cmtTypes.BlockIDFlagCommit {
			continue
		}

		hash := common.BytesToHash(crypto.Keccak256(vote.NonRpVoteExtension)).String()
		hashToExt[hash] = vote.NonRpVoteExtension
		if _, ok := hashToVotingPower[hash]; !ok {
			hashToVotingPower[hash] = 0
		}

		valAddr, err := ac.BytesToString(vote.Validator.Address)
		if err != nil {
			return nil, err
		}

		_, validator := validatorSet.GetByAddress(valAddr)
		if validator == nil {
			if milestoneAbci.ShouldErrorOnValidatorNotFound(ctx.BlockHeight()) {
				return nil, fmt.Errorf("failed to get validator %s", valAddr)
			}
			continue
		}

		hashToVotingPower[hash] += validator.VotingPower
	}

	if len(hashToVotingPower) > 1 {
		logger.Error("MULTIPLE NON-RP VOTE EXTENSIONS DETECTED, THERE SHOULD BE ONLY ONE - POSSIBLE MALICIOUS ACTIVITY")
	}

	var maxVotingPower int64
	var maxHash string

	hashList := make([]string, 0, len(hashToVotingPower))
	for hash := range hashToVotingPower {
		hashList = append(hashList, hash)
	}
	sort.Strings(hashList)

	for _, hash := range hashList {
		if votingPower := hashToVotingPower[hash]; votingPower > maxVotingPower {
			maxVotingPower = votingPower
			maxHash = hash
		}
	}

	return hashToExt[maxHash], nil
}

// validateCheckpointMsgData validates the extension is valid checkpoint
func validateCheckpointMsgData(ctx sdk.Context, extension []byte, chainManagerKeeper chainManagerKeeper.Keeper, checkpointKeeper checkpointKeeper.Keeper, contractCaller helper.IContractCaller) error {
	checkpointMsg, err := checkpointTypes.UnpackCheckpointSideSignBytes(extension[1:]) // skip the first byte which is the vote
	if err != nil {
		return fmt.Errorf("failed to unpack checkpoint side sign bytes: %w", err)
	}

	chainParams, err := chainManagerKeeper.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get chain manager params: %w", err)
	}

	if chainParams.ChainParams.BorChainId != checkpointMsg.BorChainId {
		return fmt.Errorf("invalid bor chain id, expected %s, got %s", chainParams.ChainParams.BorChainId, checkpointMsg.BorChainId)
	}

	borChainTxConfirmations := chainParams.BorChainTxConfirmations

	params, err := checkpointKeeper.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get checkpoint params: %w", err)
	}

	isValid, err := checkpointTypes.IsValidCheckpoint(
		checkpointMsg.StartBlock,
		checkpointMsg.EndBlock,
		checkpointMsg.RootHash,
		params.MaxCheckpointLength,
		contractCaller,
		borChainTxConfirmations)
	if err != nil {
		return fmt.Errorf("failed to validate checkpoint msg: %w", err)
	}

	if !isValid {
		return errors.New("invalid checkpoint msg data")
	}

	return nil
}

// getPreviousBlockValidatorSet returns the validator set from the previous block
func getPreviousBlockValidatorSet(ctx sdk.Context, stakeKeeper stakeKeeper.Keeper) (*stakeTypes.ValidatorSet, error) {
	// Fetch validatorSet from previous block
	validatorSet, err := stakeKeeper.GetPreviousBlockValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	if len(validatorSet.Validators) == 0 {
		return nil, errors.New("no validators found in validator set")
	}
	return &validatorSet, nil
}

// getPenultimateBlockValidatorSet returns the validator set from 2 blocks ago
func getPenultimateBlockValidatorSet(ctx sdk.Context, stakeKeeper stakeKeeper.Keeper) (*stakeTypes.ValidatorSet, error) {
	// fetch validatorSet from 2 blocks ago
	validatorSet, err := stakeKeeper.GetPenultimateBlockValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	if len(validatorSet.Validators) == 0 {
		return nil, errors.New("no validators found in validator set from 2 blocks ago")
	}
	return &validatorSet, nil
}

// getValidatorPublicKey returns the public key of the validator given
func getValidatorPublicKey(validator *stakeTypes.Validator) (cmtCrypto.PubKey, error) {
	cmtPubKeyProto, err := validator.CmtConsPublicKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get validator %s public key: %w", validator.Signer, err)
	}

	cmtPubKey, err := cryptoenc.PubKeyFromProto(cmtPubKeyProto)
	if err != nil {
		return nil, fmt.Errorf("failed to convert validator %s public key: %w", validator.Signer, err)
	}

	return cmtPubKey, nil
}

// FindCheckpointTx finds the checkpoint tx from the given txs that generated the given extension
// If no such tx is found empty string is returned
func findCheckpointTx(txs [][]byte, extension []byte, txDecoder txDecoder, logger log.Logger) string {
	for _, rawTx := range txs {
		tx, err := txDecoder.TxDecode(rawTx)
		if err != nil {
			logger.Error("failed to decode tx", "error", err)
			continue
		}

		messages := tx.GetMsgs()
		for _, msg := range messages {
			if checkpointTypes.IsCheckpointMsg(msg) {
				checkpointMsg, ok := msg.(*checkpointTypes.MsgCheckpoint)
				if !ok {
					logger.Error("type mismatch for MsgCheckpoint")
					continue
				}

				signBytes := checkpointMsg.GetSideSignBytes()

				if bytes.Equal(signBytes, extension) {
					var txBytes cometTypes.Tx = rawTx
					return common.Bytes2Hex(txBytes.Hash())
				}
			}
		}
	}

	return ""
}

// getCheckpointSignatures returns the checkpoint signatures from the given extVoteInfo
func getCheckpointSignatures(extension []byte, extVoteInfo []abciTypes.ExtendedVoteInfo) checkpointTypes.CheckpointSignatures {
	result := checkpointTypes.CheckpointSignatures{
		Signatures: make([]checkpointTypes.CheckpointSignature, 0),
	}
	for _, vote := range extVoteInfo {
		if bytes.Equal(vote.NonRpVoteExtension, extension) {
			result.Signatures = append(result.Signatures, checkpointTypes.CheckpointSignature{
				ValidatorAddress: vote.Validator.Address,
				Signature:        vote.NonRpExtensionSignature,
			})
		}
	}
	return result
}

type txDecoder interface {
	TxDecode(txBytes []byte) (sdk.Tx, error)
}

var dummyNonRpVoteExtension = []byte("\t\r\n#HEIMDALL-VOTE-EXTENSION#\r\n\t")

func packExtensionWithVote(extension []byte) []byte {
	yesVote := []byte{0x01}
	var buf bytes.Buffer
	buf.Write(yesVote)
	buf.Write(extension)
	return buf.Bytes()
}

func shouldCheckVotingPower(currentHeight int64) bool {
	return currentHeight < helper.GetDisableVPCheckHeight() || currentHeight >= helper.GetTallyFixHeight()
}
