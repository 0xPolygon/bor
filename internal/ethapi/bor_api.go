package ethapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// isBorSystemTx checks if the tx is for bor genesis contract addresses or not
func isBorSystemTx(borCfg *params.BorConfig, to *common.Address) bool {
	if borCfg == nil {
		return false
	}
	if to == nil {
		return false
	}

	validatorContract := common.HexToAddress(borCfg.ValidatorContract)
	stateReceiverContract := common.HexToAddress(borCfg.StateReceiverContract)
	if to.Cmp(validatorContract) == 0 || to.Cmp(stateReceiverContract) == 0 {
		return true
	}

	return false
}

// GetRootHash returns root hash for given start and end block
func (s *BlockChainAPI) GetRootHash(ctx context.Context, starBlockNr uint64, endBlockNr uint64) (string, error) {
	root, err := s.b.GetRootHash(ctx, starBlockNr, endBlockNr)
	if err != nil {
		return "", err
	}

	return root, nil
}

func (s *BlockChainAPI) GetBorBlockReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return s.b.GetBorBlockReceipt(ctx, hash)
}

func (s *BlockChainAPI) GetVoteOnHash(ctx context.Context, starBlockNr uint64, endBlockNr uint64, hash string, milestoneId string) (bool, error) {
	return s.b.GetVoteOnHash(ctx, starBlockNr, endBlockNr, hash, milestoneId)
}

//
// Bor transaction utils
//

func (s *BlockChainAPI) appendRPCMarshalBorTransaction(ctx context.Context, block *types.Block, fields map[string]interface{}, fullTx bool) map[string]interface{} {
	if block != nil {
		txHash := types.GetDerivedBorTxHash(types.BorReceiptKey(block.Number().Uint64(), block.Hash()))

		borTx, blockHash, blockNumber, txIndex, _ := s.b.GetBorBlockTransactionWithBlockHash(ctx, txHash, block.Hash())
		if borTx != nil {
			formattedTxs := fields["transactions"].([]interface{})

			if fullTx {
				marshalledTx := newRPCTransaction(borTx, blockHash, blockNumber, block.Time(), txIndex, block.BaseFee(), s.b.ChainConfig())
				// newRPCTransaction calculates hash based on RLP of the transaction data.
				// In case of bor block tx, we need simple derived tx hash (same as function argument) instead of RLP hash
				marshalledTx.Hash = txHash
				marshalledTx.ChainID = nil
				fields["transactions"] = append(formattedTxs, marshalledTx)
			} else {
				fields["transactions"] = append(formattedTxs, txHash)
			}
		}
	}

	return fields
}

// BorAPI provides an API to access Bor related information.
type BorAPI struct {
	b Backend
}

// NewBorAPI creates a new Bor protocol API.
func NewBorAPI(b Backend) *BorAPI {
	return &BorAPI{b}
}

// SendRawTransactionConditional will add the signed transaction to the transaction pool.
// The sender/bundler is responsible for signing the transaction
func (api *BorAPI) SendRawTransactionConditional(ctx context.Context, input hexutil.Bytes, options types.OptionsPIP15) (common.Hash, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(input); err != nil {
		return common.Hash{}, err
	}

	currentHeader := api.b.CurrentHeader()
	currentState, _, err := api.b.StateAndHeaderByNumber(ctx, rpc.BlockNumber(currentHeader.Number.Int64()))

	if currentState == nil || err != nil {
		return common.Hash{}, err
	}

	// check block number range
	if err := currentHeader.ValidateBlockNumberOptionsPIP15(options.BlockNumberMin, options.BlockNumberMax); err != nil {
		return common.Hash{}, &rpc.OptionsValidateError{Message: "out of block range. err: " + err.Error()}
	}

	// check timestamp range
	if err := currentHeader.ValidateTimestampOptionsPIP15(options.TimestampMin, options.TimestampMax); err != nil {
		return common.Hash{}, &rpc.OptionsValidateError{Message: "out of time range. err: " + err.Error()}
	}

	// check knownAccounts length (number of slots/accounts) should be less than 1000
	if err := options.KnownAccounts.ValidateLength(); err != nil {
		return common.Hash{}, &rpc.KnownAccountsLimitExceededError{Message: "limit exceeded. err: " + err.Error()}
	}

	// check knownAccounts
	if err := currentState.ValidateKnownAccounts(options.KnownAccounts); err != nil {
		return common.Hash{}, &rpc.OptionsValidateError{Message: "storage error. err: " + err.Error()}
	}

	// put options data in Tx, to use it later while block building
	tx.PutOptions(&options)

	return SubmitTransaction(ctx, api.b, tx)
}

func (api *BorAPI) GetVoteOnHash(ctx context.Context, starBlockNr uint64, endBlockNr uint64, hash string, milestoneId string) (bool, error) {
	return api.b.GetVoteOnHash(ctx, starBlockNr, endBlockNr, hash, milestoneId)
}

// GetWitnessByNumber returns the witness for the given block number.
func (api *BorAPI) GetWitnessByNumber(ctx context.Context, number rpc.BlockNumber) (map[string]interface{}, error) {
	witness, err := api.b.WitnessByNumber(ctx, number)
	if witness == nil || err != nil {
		return nil, err
	}
	return RPCMarshalWitness(witness), nil
}

// GetWitnessByHash returns the witness for the given block hash.
func (api *BorAPI) GetWitnessByHash(ctx context.Context, hash common.Hash) (map[string]interface{}, error) {
	witness, err := api.b.WitnessByHash(ctx, hash)
	if witness == nil || err != nil {
		return nil, err
	}
	return RPCMarshalWitness(witness), nil
}

// GetWitnessByBlockNumberOrHash returns the witness for the given block number or hash.
func (api *BorAPI) GetWitnessByBlockNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (map[string]interface{}, error) {
	witness, err := api.b.WitnessByNumberOrHash(ctx, blockNrOrHash)
	if witness == nil || err != nil {
		return nil, err
	}
	return RPCMarshalWitness(witness), nil
}

// GetBlockReceiptsByBlockHash returns all transaction receipts for a block by hash.
//
// Parameters:
//   - blockHash: canonical block hash
//
// Returns an array of marshaled receipts, or error if the block is not found
func (api *BorAPI) GetBlockReceiptsByBlockHash(ctx context.Context, blockHash common.Hash) ([]map[string]interface{}, error) {
	// Get the block by hash
	block, err := api.b.BlockByHash(ctx, blockHash)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block not found %x", blockHash)
	}

	// Verify that the block is canonical
	blockNumber := block.Number().Uint64()
	canonicalHash := rawdb.ReadCanonicalHash(api.b.ChainDb(), blockNumber)
	if canonicalHash != blockHash {
		return nil, fmt.Errorf("the hash %s is not cannonical", blockHash.String())
	}

	// Get receipts for this block
	receipts, err := api.b.GetReceipts(ctx, blockHash)
	if err != nil {
		return nil, err
	}
	if receipts == nil {
		return nil, nil
	}

	chainConfig := api.b.ChainConfig()
	txs := block.Transactions()

	// Validate receipt/transaction count match
	if len(txs) != len(receipts) {
		return nil, fmt.Errorf("receipts length mismatch: %d vs %d", len(txs), len(receipts))
	}

	signer := types.MakeSigner(chainConfig, block.Number(), block.Time())

	// Marshal each receipt
	result := make([]map[string]interface{}, 0, len(receipts))
	for i, receipt := range receipts {
		marshaled := marshalReceipt(receipt, blockHash, blockNumber, signer, txs[i], i, false)
		result = append(result, marshaled)
	}

	// Handle state-sync receipts post Madhuguri HF
	if chainConfig.Bor != nil && chainConfig.Bor.IsMadhugiri(block.Number()) {
		return result, nil
	}

	// Pre-Madhugiri: fetch state-sync receipt separately
	stateSyncReceipt, err := api.b.GetBorBlockReceipt(ctx, blockHash)
	if err != nil && !errors.Is(err, ethereum.NotFound) {
		return nil, err
	}
	if stateSyncReceipt != nil {
		tx, _, _, _ := rawdb.ReadBorTransaction(api.b.ChainDb(), stateSyncReceipt.TxHash)
		result = append(result, marshalReceipt(stateSyncReceipt, blockHash, blockNumber, signer, tx, len(result), true))
	}

	return result, nil
}

// GetHeaderByHash returns a block's header by hash.
// It retrieves the header without transactions.
//
// Parameters:
//   - hash: Block hash
//
// Returns the block header or error if not found
func (api *BorAPI) GetHeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	// Get the header for the specified block hash
	header, err := api.b.HeaderByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if header == nil {
		// Return error for missing header
		return nil, fmt.Errorf("block header not found: %s", hash.String())
	}

	return header, nil
}

// GetHeaderByNumber returns a block's header by number.
// It retrieves the header, without transactions.
//
// Parameters:
//   - blockNumber: Block number tag (latest, earliest, pending, safe, finalized, or numeric)
//
// Returns the block header or error if not found
func (api *BorAPI) GetHeaderByNumber(ctx context.Context, blockNumber rpc.BlockNumber) (*types.Header, error) {
	// Pending block is only known by the miner/builder
	if blockNumber == rpc.PendingBlockNumber {
		block, _, _ := api.b.Pending()
		if block == nil {
			return nil, nil
		}
		// Return header directly
		return block.Header(), nil
	}

	// Get the header for the specified block number
	header, err := api.b.HeaderByNumber(ctx, blockNumber)
	if err != nil {
		return nil, err
	}
	if header == nil {
		// Return error for missing header
		return nil, fmt.Errorf("block header not found: %d", blockNumber)
	}

	return header, nil
}

// BlockNumber returns the block number for the given block tag:
// - nil input → latest executed (CurrentBlock)
// - "latest" → latest head (CurrentHeader) via GetLatestBlockNumber
// - "pending" → falls through to default (latest executed)
// - unknown/numeric → latest executed (CurrentBlock)
//
// Parameters:
//   - blockNrPtr: Optional block tag (latest, earliest, safe, finalized, pending)
//     If nil, returns the latest executed block number
//
// Returns the block number as hexutil.Uint64
func (api *BorAPI) BlockNumber(ctx context.Context, blockNrPtr *rpc.BlockNumber) (hexutil.Uint64, error) {
	// Handle nil input separately, returns the latest executed block
	if blockNrPtr == nil {
		block := api.b.CurrentBlock()
		if block == nil {
			return 0, errors.New("current block not found")
		}
		return hexutil.Uint64(block.Number.Uint64()), nil
	}

	blockNr := *blockNrPtr

	// Get the appropriate block number based on the tag
	var blockNum uint64
	switch blockNr {
	case rpc.LatestBlockNumber:
		// "latest" returns the latest head (forkchoice head), not the executed one
		header := api.b.CurrentHeader()
		if header == nil {
			return 0, errors.New("current header not found")
		}
		blockNum = header.Number.Uint64()

	case rpc.EarliestBlockNumber:
		blockNum = 0

	case rpc.SafeBlockNumber:
		// Get the safe block from the blockchain
		header := api.b.CurrentSafeBlock()
		if header == nil {
			return 0, errors.New("safe block not found")
		}
		blockNum = header.Number.Uint64()

	case rpc.FinalizedBlockNumber:
		// Get the finalized block using Heimdall's logic (milestone/checkpoint)
		finalNum, err := api.b.GetFinalizedBlockNumber(ctx)
		if err != nil {
			return 0, err
		}
		blockNum = finalNum

	default:
		// For unrecognized/custom block tags (including pending), return the latest executed block
		block := api.b.CurrentBlock()
		if block == nil {
			return 0, errors.New("current block not found")
		}
		blockNum = block.Number.Uint64()
	}

	return hexutil.Uint64(blockNum), nil
}

// Forks is a data type to record a list of forks
type Forks struct {
	GenesisHash common.Hash `json:"genesis"`
	HeightForks []uint64    `json:"heightForks"`
	TimeForks   []uint64    `json:"timeForks"`
}

// Forks implements bor_forks. Returns the genesis block hash and a sorted list of all forks block numbers.
//
// Returns:
//   - GenesisHash: The hash of the genesis block
//   - HeightForks: Sorted list of all block number forks
//   - TimeForks: Sorted list of all timestamp forks
func (api *BorAPI) Forks(ctx context.Context) (Forks, error) {
	// Get genesis block
	genesis, err := api.b.BlockByNumber(ctx, rpc.BlockNumber(0))
	if err != nil {
		return Forks{}, err
	}
	if genesis == nil {
		return Forks{}, errors.New("genesis block not found")
	}

	// Get chain config
	chainConfig := api.b.ChainConfig()
	if chainConfig == nil {
		return Forks{}, errors.New("chain config not found")
	}

	// Gather forks from chain config
	heightForks, timeForks := forkid.GatherForks(chainConfig, genesis.Time())

	return Forks{
		GenesisHash: genesis.Hash(),
		HeightForks: heightForks,
		TimeForks:   timeForks,
	}, nil
}

// GetLogsByHash returns the logs generated by the transactions by the block's hash.
//
// Parameters:
//   - hash: Block hash
//
// Returns an array where each element is the logs array for the corresponding transaction in the block.
// Returns nil if the block is not found.
func (api *BorAPI) GetLogsByHash(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
	// Get the block by hash
	block, err := api.b.BlockByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, nil
	}

	// Get receipts for this block
	receipts, err := api.b.GetReceipts(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("getReceipts error: %w", err)
	}
	if receipts == nil {
		return nil, nil
	}

	// Extract logs from receipts (one array per transaction)
	logs := make([][]*types.Log, len(receipts))
	for i, receipt := range receipts {
		if len(receipt.Logs) > 0 {
			logs[i] = receipt.Logs
		}
	}

	// Handle Bor state-sync logs (pre-Madhugiri)
	// State-sync receipts are stored with normal block receipts post Madhugiri HF
	chainConfig := api.b.ChainConfig()
	if chainConfig.Bor != nil && chainConfig.Bor.IsMadhugiri(block.Number()) {
		return logs, nil
	}

	// Pre-Madhuguri: fetch the state-sync receipt separately and append its logs
	stateSyncReceipt, err := api.b.GetBorBlockReceipt(ctx, hash)
	if err != nil && !errors.Is(err, ethereum.NotFound) {
		return nil, fmt.Errorf("getReceipts error: %w", err)
	}
	if stateSyncReceipt != nil {
		// Always append an entry for the state-sync receipt, even if it has no logs.
		logs = append(logs, stateSyncReceipt.Logs)
	}

	return logs, nil
}
