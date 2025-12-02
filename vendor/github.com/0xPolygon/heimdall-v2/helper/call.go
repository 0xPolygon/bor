package helper

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	lru "github.com/hashicorp/golang-lru"
	"github.com/pkg/errors"

	"github.com/0xPolygon/heimdall-v2/contracts/erc20"
	"github.com/0xPolygon/heimdall-v2/contracts/rootchain"
	"github.com/0xPolygon/heimdall-v2/contracts/slashmanager"
	"github.com/0xPolygon/heimdall-v2/contracts/stakemanager"
	"github.com/0xPolygon/heimdall-v2/contracts/stakinginfo"
	"github.com/0xPolygon/heimdall-v2/contracts/statereceiver"
	"github.com/0xPolygon/heimdall-v2/contracts/statesender"
	"github.com/0xPolygon/heimdall-v2/contracts/validatorset"
	"github.com/0xPolygon/heimdall-v2/x/bor/grpc"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// smart contracts' events names
const (
	NewHeaderBlockEvent = "NewHeaderBlock"
	TopUpFeeEvent       = "TopUpFee"
	StakedEvent         = "Staked"
	StakeUpdateEvent    = "StakeUpdate"
	UnstakeInitEvent    = "UnstakeInit"
	SignerChangeEvent   = "SignerChange"
	StateSyncedEvent    = "StateSynced"
	SlashedEvent        = "Slashed"
	UnJailedEvent       = "UnJailed"
)

// ContractsABIsMap is a cached map holding the ABIs of the contracts
var ContractsABIsMap = make(map[string]*abi.ABI)

// IContractCaller represents contract caller
type IContractCaller interface {
	GetHeaderInfo(headerID uint64, rootChainInstance *rootchain.Rootchain, childBlockInterval uint64) (root common.Hash, start, end, createdAt uint64, proposer string, err error)
	GetRootHash(start, end, checkpointLength uint64) ([]byte, error)
	GetVoteOnHash(start, end uint64, hash, milestoneID string) (bool, error)
	GetValidatorInfo(valID uint64, stakingInfoInstance *stakinginfo.Stakinginfo) (validator types.Validator, err error)
	GetLastChildBlock(rootChainInstance *rootchain.Rootchain) (uint64, error)
	CurrentHeaderBlock(rootChainInstance *rootchain.Rootchain, childBlockInterval uint64) (uint64, error)
	GetBalance(address common.Address) (*big.Int, error)
	SendCheckpoint(signedData []byte, sigs [][3]*big.Int, rootChainAddress common.Address, rootChainInstance *rootchain.Rootchain) (err error)
	GetCheckpointSign(txHash common.Hash) ([]byte, []byte, []byte, error)
	GetMainChainBlock(*big.Int) (*ethTypes.Header, error)
	GetBorChainBlock(context.Context, *big.Int) (*ethTypes.Header, error)
	GetBorChainBlockInfoInBatch(ctx context.Context, start, end int64) ([]*ethTypes.Header, []uint64, []common.Address, error)
	GetBorChainBlockTd(ctx context.Context, blockHash common.Hash) (uint64, error)
	GetBorChainBlockAuthor(*big.Int) (*common.Address, error)
	IsTxConfirmed(common.Hash, uint64) bool
	GetConfirmedTxReceipt(common.Hash, uint64) (*ethTypes.Receipt, error)
	GetBlockNumberFromTxHash(common.Hash) (*big.Int, error)

	DecodeNewHeaderBlockEvent(string, *ethTypes.Receipt, uint64) (*rootchain.RootchainNewHeaderBlock, error)

	DecodeValidatorTopupFeesEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoTopUpFee, error)
	DecodeValidatorJoinEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoStaked, error)
	DecodeValidatorStakeUpdateEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoStakeUpdate, error)
	DecodeValidatorExitEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoUnstakeInit, error)
	DecodeSignerUpdateEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoSignerChange, error)

	DecodeStateSyncedEvent(string, *ethTypes.Receipt, uint64) (*statesender.StatesenderStateSynced, error)

	DecodeSlashedEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoSlashed, error)
	DecodeUnJailedEvent(string, *ethTypes.Receipt, uint64) (*stakinginfo.StakinginfoUnJailed, error)

	GetMainTxReceipt(common.Hash) (*ethTypes.Receipt, error)
	GetBorTxReceipt(common.Hash) (*ethTypes.Receipt, error)
	ApproveTokens(*big.Int, common.Address, common.Address, *erc20.Erc20) error
	StakeFor(common.Address, *big.Int, *big.Int, bool, common.Address, *stakemanager.Stakemanager) error
	CurrentAccountStateRoot(stakingInfoInstance *stakinginfo.Stakinginfo) ([32]byte, error)
	CurrentSpanNumber(validatorSet *validatorset.Validatorset) (Number *big.Int)
	GetSpanDetails(id *big.Int, validatorSet *validatorset.Validatorset) (*big.Int, *big.Int, *big.Int, error)
	CurrentStateCounter(stateSenderInstance *statesender.Statesender) (Number *big.Int)
	CheckIfBlocksExist(end uint64) (bool, error)
	GetRootChainInstance(rootChainAddress string) (*rootchain.Rootchain, error)
	GetStakingInfoInstance(stakingInfoAddress string) (*stakinginfo.Stakinginfo, error)
	GetValidatorSetInstance(validatorSetAddress string) (*validatorset.Validatorset, error)
	GetStakeManagerInstance(stakingManagerAddress string) (*stakemanager.Stakemanager, error)
	GetSlashManagerInstance(slashManagerAddress string) (*slashmanager.Slashmanager, error)
	GetStateSenderInstance(stateSenderAddress string) (*statesender.Statesender, error)
	GetStateReceiverInstance(stateReceiverAddress string) (*statereceiver.Statereceiver, error)
	GetTokenInstance(tokenAddress string) (*erc20.Erc20, error)
}

// ContractCaller contract caller
type ContractCaller struct {
	MainChainClient  *ethclient.Client
	MainChainRPC     *rpc.Client
	MainChainTimeout time.Duration

	BorChainClient    *ethclient.Client
	BorChainRPCClient *rpc.Client
	BorChainTimeout   time.Duration

	BorChainGrpcFlag   bool
	BorChainGrpcClient *grpc.BorGRPCClient

	RootChainABI     abi.ABI
	StakingInfoABI   abi.ABI
	ValidatorSetABI  abi.ABI
	StateReceiverABI abi.ABI
	StateSenderABI   abi.ABI
	StakeManagerABI  abi.ABI
	SlashManagerABI  abi.ABI
	PolTokenABI      abi.ABI

	ReceiptCache *lru.Cache

	ContractInstanceCache map[common.Address]interface{}
}

type txExtraInfo struct {
	BlockNumber *string         `json:"blockNumber,omitempty"`
	BlockHash   *common.Hash    `json:"blockHash,omitempty"`
	From        *common.Address `json:"from,omitempty"`
}

type rpcTransaction struct {
	txExtraInfo
}

// NewContractCaller contract caller
func NewContractCaller() (contractCallerObj ContractCaller, err error) {
	config := GetConfig()
	contractCallerObj.MainChainClient = GetMainClient()
	contractCallerObj.MainChainTimeout = config.EthRPCTimeout
	contractCallerObj.BorChainClient = GetBorClient()
	contractCallerObj.BorChainTimeout = config.BorRPCTimeout
	contractCallerObj.MainChainRPC = GetMainChainRPCClient()
	contractCallerObj.BorChainRPCClient = GetBorRPCClient()
	contractCallerObj.ReceiptCache, err = lru.New(1000)
	contractCallerObj.BorChainGrpcFlag = config.BorGRPCFlag
	contractCallerObj.BorChainGrpcClient = GetBorGRPCClient()

	if err != nil {
		return contractCallerObj, err
	}

	// listeners and processors instance cache (address->ABI)
	contractCallerObj.ContractInstanceCache = make(map[common.Address]interface{})
	// package global cache (string->ABI)
	if err = populateABIs(&contractCallerObj); err != nil {
		return contractCallerObj, err
	}

	return contractCallerObj, nil
}

// GetRootChainInstance returns the RootChain contract instance for a selected chain
func (c *ContractCaller) GetRootChainInstance(rootChainAddress string) (*rootchain.Rootchain, error) {
	address := common.HexToAddress(rootChainAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := rootchain.NewRootchain(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the root chain instance from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*rootchain.Rootchain), nil
}

// GetStakingInfoInstance returns stakingInfo contract instance for a selected chain
func (c *ContractCaller) GetStakingInfoInstance(stakingInfoAddress string) (*stakinginfo.Stakinginfo, error) {
	address := common.HexToAddress(stakingInfoAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := stakinginfo.NewStakinginfo(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the stakingInfo instance from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*stakinginfo.Stakinginfo), nil
}

// GetValidatorSetInstance returns stakingInfo contract instance for a selected chain
func (c *ContractCaller) GetValidatorSetInstance(validatorSetAddress string) (*validatorset.Validatorset, error) {
	address := common.HexToAddress(validatorSetAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := validatorset.NewValidatorset(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the validator set from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*validatorset.Validatorset), nil
}

// GetStakeManagerInstance returns stakingInfo contract instance for a selected base chain
func (c *ContractCaller) GetStakeManagerInstance(stakingManagerAddress string) (*stakemanager.Stakemanager, error) {
	address := common.HexToAddress(stakingManagerAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := stakemanager.NewStakemanager(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the stake manager from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*stakemanager.Stakemanager), nil
}

// GetSlashManagerInstance returns the slashManager contract instance for a selected base chain
func (c *ContractCaller) GetSlashManagerInstance(slashManagerAddress string) (*slashmanager.Slashmanager, error) {
	address := common.HexToAddress(slashManagerAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := slashmanager.NewSlashmanager(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the slash manager from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*slashmanager.Slashmanager), nil
}

// GetStateSenderInstance returns stakingInfo contract instance for a selected base chain
func (c *ContractCaller) GetStateSenderInstance(stateSenderAddress string) (*statesender.Statesender, error) {
	address := common.HexToAddress(stateSenderAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := statesender.NewStatesender(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the stateSender from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*statesender.Statesender), nil
}

// GetStateReceiverInstance returns stakingInfo contract instance for a selected base chain
func (c *ContractCaller) GetStateReceiverInstance(stateReceiverAddress string) (*statereceiver.Statereceiver, error) {
	address := common.HexToAddress(stateReceiverAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := statereceiver.NewStatereceiver(address, borClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the stateReceiver from mainChain client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*statereceiver.Statereceiver), nil
}

// GetTokenInstance returns the contract instance for a selected chain
func (c *ContractCaller) GetTokenInstance(tokenAddress string) (*erc20.Erc20, error) {
	address := common.HexToAddress(tokenAddress)

	contractInstance, ok := c.ContractInstanceCache[address]
	if !ok {
		ci, err := erc20.NewErc20(address, mainChainClient)
		c.ContractInstanceCache[address] = ci

		if err != nil {
			Logger.Error("error in fetching the token address from client", "error", err)
			return nil, err
		}

		return ci, err
	}

	return contractInstance.(*erc20.Erc20), nil
}

// GetHeaderInfo get header info from checkpoint number
func (c *ContractCaller) GetHeaderInfo(headerID uint64, rootChainInstance *rootchain.Rootchain, childBlockInterval uint64) (
	root common.Hash,
	start,
	end,
	createdAt uint64,
	proposer string,
	err error,
) {
	// get header from rootChain
	checkpointBigInt := big.NewInt(0).Mul(big.NewInt(0).SetUint64(headerID), big.NewInt(0).SetUint64(childBlockInterval))

	headerBlock, err := rootChainInstance.HeaderBlocks(nil, checkpointBigInt)
	if err != nil {
		return root, start, end, createdAt, proposer, errors.New("unable to fetch checkpoint block")
	}

	return headerBlock.Root,
		headerBlock.Start.Uint64(),
		headerBlock.End.Uint64(),
		headerBlock.CreatedAt.Uint64(),
		headerBlock.Proposer.String(),
		nil
}

// GetRootHash get root hash from the bor chain for the corresponding start and end block
func (c *ContractCaller) GetRootHash(start, end, checkpointLength uint64) ([]byte, error) {
	noOfBlock := end - start + 1

	if start > end {
		return nil, errors.New("start is greater than end")
	}

	if noOfBlock > checkpointLength {
		return nil, errors.New("number of headers requested exceeds checkpoint length")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.BorChainTimeout)
	defer cancel()

	var rootHash string
	var err error

	if c.BorChainGrpcFlag {
		rootHash, err = c.BorChainGrpcClient.GetRootHash(ctx, start, end)
	} else {
		rootHash, err = c.BorChainClient.GetRootHash(ctx, start, end)
	}

	if err != nil {
		Logger.Error("could not fetch rootHash from bor chain", "error", err)
		return nil, err
	}

	return common.FromHex(rootHash), nil
}

// GetVoteOnHash get vote on hash from the bor chain for the corresponding milestone
func (c *ContractCaller) GetVoteOnHash(start, end uint64, hash, milestoneID string) (bool, error) {
	if start > end {
		return false, errors.New("Start block number is greater than the end block number")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.BorChainTimeout)
	defer cancel()

	var vote bool
	var err error

	if c.BorChainGrpcFlag {
		vote, err = c.BorChainGrpcClient.GetVoteOnHash(ctx, start, end, hash, milestoneID)
	} else {
		vote, err = c.BorChainClient.GetVoteOnHash(ctx, start, end, hash, milestoneID)
	}

	if err != nil {
		return false, errors.New(fmt.Sprint("Error in fetching vote from bor chain", "err", err))
	}

	return vote, nil
}

// GetLastChildBlock fetch current child block
func (c *ContractCaller) GetLastChildBlock(rootChainInstance *rootchain.Rootchain) (uint64, error) {
	lastChildBlock, err := rootChainInstance.GetLastChildBlock(nil)
	if err != nil {
		Logger.Error("Could not fetch current child block from rootChain contract", "error", err)
		return 0, err
	}

	if lastChildBlock == nil {
		Logger.Error("Contract returned nil value for lastChildBlock")
		return 0, fmt.Errorf("contract returned nil value")
	}

	return lastChildBlock.Uint64(), nil
}

// CurrentHeaderBlock fetches the current header block
func (c *ContractCaller) CurrentHeaderBlock(rootChainInstance *rootchain.Rootchain, childBlockInterval uint64) (uint64, error) {
	currentHeaderBlock, err := rootChainInstance.CurrentHeaderBlock(nil)
	if err != nil {
		Logger.Error("Could not fetch current header block from rootChain contract", "error", err)
		return 0, err
	}

	if currentHeaderBlock == nil {
		Logger.Error("Contract returned nil value for currentHeaderBlock")
		return 0, fmt.Errorf("contract returned nil value")
	}

	return currentHeaderBlock.Uint64() / childBlockInterval, nil
}

// GetBalance get balance of an account (returns big.Int balance won't fit in uint64)
func (c *ContractCaller) GetBalance(address common.Address) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.MainChainTimeout)
	defer cancel()

	balance, err := c.MainChainClient.BalanceAt(ctx, address, nil)
	if err != nil {
		Logger.Error("unable to fetch balance of account from root chain", "Address", address.String(), "error", err)
		return big.NewInt(0), err
	}

	return balance, nil
}

// GetValidatorInfo get validator info
func (c *ContractCaller) GetValidatorInfo(valID uint64, stakingInfoInstance *stakinginfo.Stakinginfo) (validator types.Validator, err error) {
	stakerDetails, err := stakingInfoInstance.GetStakerDetails(nil, big.NewInt(int64(valID)))
	if err != nil {
		Logger.Error("error fetching validator information from stake manager", "validatorId", valID, "error", err)
		return
	}

	newAmount, err := GetPowerFromAmount(stakerDetails.Amount)
	if err != nil {
		return
	}

	// newAmount
	validator = types.Validator{
		ValId:       valID,
		VotingPower: newAmount.Int64(),
		StartEpoch:  stakerDetails.ActivationEpoch.Uint64(),
		EndEpoch:    stakerDetails.DeactivationEpoch.Uint64(),
		Signer:      stakerDetails.Signer.String(),
	}

	return validator, nil
}

// GetMainChainBlock returns main chain block header
func (c *ContractCaller) GetMainChainBlock(blockNum *big.Int) (header *ethTypes.Header, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.MainChainTimeout)
	defer cancel()

	latestBlock, err := c.MainChainClient.HeaderByNumber(ctx, blockNum)
	if err != nil {
		Logger.Error("unable to connect to main chain", "error", err)
		return
	}

	return latestBlock, nil
}

// GetMainChainFinalizedBlock returns the finalized main chain block header (post-merge)
func (c *ContractCaller) GetMainChainFinalizedBlock() (header *ethTypes.Header, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.MainChainTimeout)
	defer cancel()

	latestFinalizedBlock, err := c.MainChainClient.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		Logger.Error("unable to connect to bor chain", "error", err)
		return
	}

	return latestFinalizedBlock, nil
}

// GetMainChainBlockTime returns main chain block time
func (c *ContractCaller) GetMainChainBlockTime(ctx context.Context, blockNum uint64) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, c.MainChainTimeout)
	defer cancel()

	latestBlock, err := c.MainChainClient.BlockByNumber(ctx, big.NewInt(0).SetUint64(blockNum))
	if err != nil {
		Logger.Error("unable to connect to bor chain", "error", err)
		return time.Time{}, err
	}

	return time.Unix(int64(latestBlock.Time()), 0), nil
}

// GetBorChainBlock returns bor chain block header
func (c *ContractCaller) GetBorChainBlock(ctx context.Context, blockNum *big.Int) (header *ethTypes.Header, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.BorChainTimeout)
	defer cancel()

	var latestBlock *ethTypes.Header

	if c.BorChainGrpcFlag {
		if blockNum == nil {
			// LatestBlockNumber is BlockNumber(-2) in go-ethereum rpc
			latestBlock, err = c.BorChainGrpcClient.HeaderByNumber(ctx, -2)
		} else {
			latestBlock, err = c.BorChainGrpcClient.HeaderByNumber(ctx, blockNum.Int64())
		}
	} else {
		latestBlock, err = c.BorChainClient.HeaderByNumber(ctx, blockNum)
	}

	if err != nil {
		Logger.Error("unable to connect to bor chain", "error", err)
		return
	}

	return latestBlock, nil
}

// GetBorChainBlockInfoInBatch returns bor chain block headers and TD via a single RPC Batch call.
// It tries to get blocks from the range interval but returns only the ones found on the chain
func (c *ContractCaller) GetBorChainBlockInfoInBatch(ctx context.Context, start, end int64) ([]*ethTypes.Header, []uint64, []common.Address, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, c.BorChainTimeout)
	defer cancel()

	totalBlocks := end - start + 1
	rpcClient := c.BorChainClient.Client()
	batchElems := make([]rpc.BatchElem, 0, 2*(totalBlocks))

	// Header Batch
	result := make([]*ethTypes.Header, totalBlocks)
	for i := start; i <= end; i++ {
		blockNumHex := fmt.Sprintf("0x%x", i)

		batchElems = append(batchElems, rpc.BatchElem{
			Method: "eth_getHeaderByNumber",
			Args:   []interface{}{blockNumHex},
			Result: &result[i-start],
		})
	}

	type tdResp struct {
		TotalDifficulty hexutil.Uint64 `json:"totalDifficulty"`
	}

	// TD Batch
	resultTd := make([]*tdResp, totalBlocks)
	for i := start; i <= end; i++ {
		blockNumHex := fmt.Sprintf("0x%x", i)

		batchElems = append(batchElems, rpc.BatchElem{
			Method: "eth_getTdByNumber",
			Args:   []interface{}{blockNumHex},
			Result: &resultTd[i-start],
		})
	}

	// Author Batch
	resultAuthor := make([]*common.Address, totalBlocks)
	for i := start; i <= end; i++ {
		if i > 0 { // skip genesis block
			blockNumHex := fmt.Sprintf("0x%x", i)
			batchElems = append(batchElems, rpc.BatchElem{
				Method: "bor_getAuthor",
				Args:   []interface{}{blockNumHex},
				Result: &resultAuthor[i-start],
			})
		}
	}

	if err := rpcClient.BatchCallContext(timeoutCtx, batchElems); err != nil {
		return nil, nil, nil, err
	}

	// Get results until capture an error (header not found)
	tds := make([]uint64, 0, totalBlocks)
	headers := make([]*ethTypes.Header, 0, totalBlocks)
	authors := make([]common.Address, 0, totalBlocks)
	for i := 0; i < int(totalBlocks); i++ {
		blockNum := start + int64(i)
		elemHeader := batchElems[i]
		elemTd := batchElems[i+int(totalBlocks)]

		if elemHeader.Error != nil || elemTd.Error != nil || result[i] == nil || resultTd[i] == nil {
			Logger.Debug("error fetching block info", "error", elemHeader.Error, "error", elemTd.Error, "blockNum", blockNum)
			break
		}

		var author common.Address
		if blockNum > 0 {
			authorReqIndex := 2*int(totalBlocks) + i
			if start == 0 {
				authorReqIndex--
			}
			elemAuthor := batchElems[authorReqIndex]
			if elemAuthor.Error != nil || resultAuthor[i] == nil {
				Logger.Debug("error fetching block author", "error", elemAuthor.Error, "blockNum", blockNum)
				break
			}
			author = *resultAuthor[i]
		}

		headers = append(headers, result[i])
		tds = append(tds, uint64(resultTd[i].TotalDifficulty))
		authors = append(authors, author)
	}

	return headers, tds, authors, nil
}

// GetBorChainBlockTd returns total difficulty of a block
func (c *ContractCaller) GetBorChainBlockTd(ctx context.Context, blockHash common.Hash) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.BorChainTimeout)
	defer cancel()

	rpcClient := c.BorChainClient.Client()

	var resp map[string]interface{}
	if err := rpcClient.CallContext(ctx, &resp, "eth_getTdByHash", blockHash.Hex()); err != nil {
		return 0, err
	}

	raw, ok := resp["totalDifficulty"].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected totalDifficulty type %T", resp["totalDifficulty"])
	}

	td, err := hexutil.DecodeUint64(raw)
	if err != nil {
		return 0, fmt.Errorf("failed to decode totalDifficulty %q: %w", raw, err)
	}

	return td, nil
}

// GetBorChainBlockAuthor returns the producer of the bor block
func (c *ContractCaller) GetBorChainBlockAuthor(blockNum *big.Int) (*common.Address, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.BorChainTimeout)
	defer cancel()

	var author *common.Address
	err := c.BorChainClient.Client().CallContext(ctx, &author, "bor_getAuthor", toBlockNumArg(blockNum))
	if err != nil {
		Logger.Error("Unable to connect to bor chain", "error", err)
		return nil, err
	}

	if author == nil {
		return nil, ethereum.NotFound
	}

	return author, nil
}

// GetBlockNumberFromTxHash gets the block number of transaction
func (c *ContractCaller) GetBlockNumberFromTxHash(tx common.Hash) (*big.Int, error) {
	var rpcTx rpcTransaction
	if err := c.MainChainRPC.CallContext(context.Background(), &rpcTx, "eth_getTransactionByHash", tx); err != nil {
		return nil, err
	}

	if rpcTx.BlockNumber == nil {
		return nil, errors.New("no tx found")
	}

	blkNum := big.NewInt(0)

	blkNum, ok := blkNum.SetString(*rpcTx.BlockNumber, 0)
	if !ok {
		return nil, errors.New("unable to set string")
	}

	return blkNum, nil
}

// IsTxConfirmed checks whether the tx corresponding to the given hash is confirmed with given
// requiredConfirmations numbers
func (c *ContractCaller) IsTxConfirmed(txHash common.Hash, requiredConfirmations uint64) bool {
	// get main tx receipt
	receipt, err := c.GetConfirmedTxReceipt(txHash, requiredConfirmations)
	if err != nil {
		Logger.Error("error while fetching the tx receipt", "error", err)
		return false
	}

	if receipt == nil {
		return false
	}

	return true
}

// GetConfirmedTxReceipt returns confirmed tx receipt
func (c *ContractCaller) GetConfirmedTxReceipt(tx common.Hash, requiredConfirmations uint64) (*ethTypes.Receipt, error) {
	var receipt *ethTypes.Receipt

	receiptCache, ok := c.ReceiptCache.Get(tx.String())
	if !ok {
		var err error

		// get main tx receipt
		receipt, err = c.GetMainTxReceipt(tx)
		if err != nil {
			Logger.Error("error while fetching mainChain receipt", "txHash", tx.Hex(), "error", err)
			return nil, err
		}

		c.ReceiptCache.Add(tx.String(), receipt)
	} else {
		receipt, ok = receiptCache.(*ethTypes.Receipt)
		if !ok {
			return nil, errors.New("error in casting the fetched receipt into eth receipt")
		}
	}

	receiptBlockNumber := receipt.BlockNumber.Uint64()

	Logger.Debug("tx included in block", "block", receiptBlockNumber, "tx", tx)

	// fetch the last finalized main chain block (available post-merge)
	latestFinalizedBlock, err := c.GetMainChainFinalizedBlock()
	if err != nil {
		Logger.Error("error getting latest finalized block from main chain", "error", err)
	}

	// If the latest finalized block is available, use it to check if the receipt is finalized or not.
	// Else, fallback to the `requiredConfirmations` value
	if latestFinalizedBlock != nil {
		Logger.Debug("latest finalized block on main chain obtained", "Block", latestFinalizedBlock.Number.Uint64(), "receipt block", receiptBlockNumber)

		if receiptBlockNumber > latestFinalizedBlock.Number.Uint64() {
			return nil, errors.New("not enough confirmations")
		}
	} else {
		// get the current/latest main chain block
		latestBlk, err := c.GetMainChainBlock(nil)
		if err != nil {
			Logger.Error("error getting latest block from main chain", "error", err)
			return nil, err
		}

		Logger.Debug("latest block on main chain obtained", "Block", latestBlk.Number.Uint64(), "receipt block", receiptBlockNumber)

		diff := latestBlk.Number.Uint64() - receiptBlockNumber
		if diff < requiredConfirmations {
			return nil, errors.New("not enough confirmations")
		}
	}

	return receipt, nil
}

//
// Validator decode events
//

// DecodeNewHeaderBlockEvent represents the new header block event
func (c *ContractCaller) DecodeNewHeaderBlockEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*rootchain.RootchainNewHeaderBlock, error) {
	event := new(rootchain.RootchainNewHeaderBlock)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.RootChainABI, event, NewHeaderBlockEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeValidatorTopupFeesEvent represents topUp for fees tokens
func (c *ContractCaller) DecodeValidatorTopupFeesEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoTopUpFee, error) {
	event := new(stakinginfo.StakinginfoTopUpFee)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, TopUpFeeEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeValidatorJoinEvent represents validator staked event
func (c *ContractCaller) DecodeValidatorJoinEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoStaked, error) {
	event := new(stakinginfo.StakinginfoStaked)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, StakedEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeValidatorStakeUpdateEvent represents validator stake update event
func (c *ContractCaller) DecodeValidatorStakeUpdateEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoStakeUpdate, error) {
	event := new(stakinginfo.StakinginfoStakeUpdate)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, StakeUpdateEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeValidatorExitEvent represents validator stake unStake event
func (c *ContractCaller) DecodeValidatorExitEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoUnstakeInit, error) {
	event := new(stakinginfo.StakinginfoUnstakeInit)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, UnstakeInitEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeSignerUpdateEvent represents sig update event
func (c *ContractCaller) DecodeSignerUpdateEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoSignerChange, error) {
	event := new(stakinginfo.StakinginfoSignerChange)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, SignerChangeEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeStateSyncedEvent decode state sync data
func (c *ContractCaller) DecodeStateSyncedEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*statesender.StatesenderStateSynced, error) {
	event := new(statesender.StatesenderStateSynced)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StateSenderABI, event, StateSyncedEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// decode slashing events

// DecodeSlashedEvent represents tick ack on contract
func (c *ContractCaller) DecodeSlashedEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoSlashed, error) {
	event := new(stakinginfo.StakinginfoSlashed)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, SlashedEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

// DecodeUnJailedEvent represents unJail on contract
func (c *ContractCaller) DecodeUnJailedEvent(contractAddressString string, receipt *ethTypes.Receipt, logIndex uint64) (*stakinginfo.StakinginfoUnJailed, error) {
	event := new(stakinginfo.StakinginfoUnJailed)

	contractAddress := common.HexToAddress(contractAddressString)

	for _, vLog := range receipt.Logs {
		if uint64(vLog.Index) == logIndex && bytes.Equal(vLog.Address.Bytes(), contractAddress.Bytes()) {
			if err := UnpackLog(&c.StakingInfoABI, event, UnJailedEvent, vLog); err != nil {
				return nil, err
			}

			return event, nil
		}
	}

	return nil, errors.New("event not found")
}

//
// Account root functions
//

// CurrentAccountStateRoot get current account root from on the chain
func (c *ContractCaller) CurrentAccountStateRoot(stakingInfoInstance *stakinginfo.Stakinginfo) ([32]byte, error) {
	accountStateRoot, err := stakingInfoInstance.GetAccountStateRoot(nil)
	if err != nil {
		Logger.Error("unable to get current account state root", "error", err)

		var emptyArr [32]byte

		return emptyArr, err
	}

	return accountStateRoot, nil
}

//
// Span-related functions
//

// CurrentSpanNumber get current span
func (c *ContractCaller) CurrentSpanNumber(validatorSetInstance *validatorset.Validatorset) (Number *big.Int) {
	result, err := validatorSetInstance.CurrentSpanNumber(nil)
	if err != nil {
		Logger.Error("unable to get current span number", "error", err)
		return nil
	}

	return result
}

// GetSpanDetails get span details
func (c *ContractCaller) GetSpanDetails(id *big.Int, validatorSetInstance *validatorset.Validatorset) (
	*big.Int,
	*big.Int,
	*big.Int,
	error,
) {
	d, err := validatorSetInstance.GetSpan(nil, id)
	if err != nil {
		return nil, nil, nil, errors.New("unable to get span details")
	}
	return d.Number, d.StartBlock, d.EndBlock, nil
}

// CurrentStateCounter get state counter
func (c *ContractCaller) CurrentStateCounter(stateSenderInstance *statesender.Statesender) (Number *big.Int) {
	result, err := stateSenderInstance.Counter(nil)
	if err != nil {
		Logger.Error("unable to get current counter number", "error", err)
		return nil
	}

	return result
}

// CheckIfBlocksExist - check if the given block exists on the local chain
func (c *ContractCaller) CheckIfBlocksExist(end uint64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.BorChainTimeout)
	defer cancel()

	block, err := c.GetBlockByNumber(ctx, end)
	if block == nil {
		return false, err
	}

	return end == block.NumberU64(), err
}

// GetBlockByNumber returns blocks by number from the child chain (bor)
func (c *ContractCaller) GetBlockByNumber(ctx context.Context, blockNumber uint64) (*ethTypes.Block, error) {
	var block *ethTypes.Block
	var err error

	if c.BorChainGrpcFlag {
		block, err = c.BorChainGrpcClient.BlockByNumber(ctx, int64(blockNumber))
	} else {
		block, err = c.BorChainClient.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
	}

	if err != nil {
		Logger.Error("Unable to fetch block by number from child chain", "block", block, "err", err)
		return nil, err
	}

	return block, nil
}

//
// Receipt functions
//

// GetMainTxReceipt returns main tx receipt
func (c *ContractCaller) GetMainTxReceipt(txHash common.Hash) (*ethTypes.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.MainChainTimeout)
	defer cancel()

	return c.getTxReceipt(ctx, c.MainChainClient, nil, txHash)
}

// GetBorTxReceipt returns bor tx receipt
func (c *ContractCaller) GetBorTxReceipt(txHash common.Hash) (*ethTypes.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.BorChainTimeout)
	defer cancel()

	if c.BorChainGrpcFlag {
		return c.getTxReceipt(ctx, nil, c.BorChainGrpcClient, txHash)
	}
	return c.getTxReceipt(ctx, c.BorChainClient, nil, txHash)
}

func (c *ContractCaller) getTxReceipt(ctx context.Context, client *ethclient.Client, grpcClient *grpc.BorGRPCClient, txHash common.Hash) (*ethTypes.Receipt, error) {
	if grpcClient != nil {
		return grpcClient.TransactionReceipt(ctx, txHash)
	}
	return client.TransactionReceipt(ctx, txHash)
}

// GetCheckpointSign returns sigs input of committed checkpoint transaction
func (c *ContractCaller) GetCheckpointSign(txHash common.Hash) ([]byte, []byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.MainChainTimeout)
	defer cancel()

	mainChainClient := GetMainClient()

	transaction, isPending, err := mainChainClient.TransactionByHash(ctx, txHash)
	if err != nil {
		Logger.Error("error while Fetching Transaction By hash from MainChain", "error", err)
		return []byte{}, []byte{}, []byte{}, err
	} else if isPending {
		return []byte{}, []byte{}, []byte{}, errors.New("transaction is still pending")
	}

	payload := transaction.Data()
	chainABI := c.RootChainABI

	return UnpackSigAndVotes(payload, chainABI)
}

// utility and helper methods

// populateABIs fills the package level cache for contracts' ABIs.
// When called the first time, ContractsABIsMap will be filled,
// and the getABI method won't be invoked the next time.
// This reduces the number of calls to JSON decode methods made by the contract caller.
// It uses ABIs' definitions instead of contracts' addresses,
// as the latter might not be available at initialization time
func populateABIs(contractCallerObj *ContractCaller) error {
	var ccAbi *abi.ABI

	var err error

	contractsABIs := [8]string{
		rootchain.RootchainMetaData.ABI, stakinginfo.StakinginfoMetaData.ABI, validatorset.ValidatorsetMetaData.ABI,
		statereceiver.StatereceiverMetaData.ABI, statesender.StatesenderMetaData.ABI, stakemanager.StakemanagerMetaData.ABI,
		slashmanager.SlashmanagerMetaData.ABI, erc20.Erc20MetaData.ABI,
	}

	// iterate over supported ABIs
	for _, contractABI := range contractsABIs {
		ccAbi, err = chooseContractCallerABI(contractCallerObj, contractABI)
		if err != nil {
			Logger.Error("error while fetching contract caller ABI", "error", err)
			return err
		}

		if ContractsABIsMap[contractABI] == nil {
			// fills cached abi map
			if *ccAbi, err = getABI(contractABI); err != nil {
				Logger.Error("error while getting ABI for contract caller", "name", contractABI, "error", err)
				return err
			} else {
				ContractsABIsMap[contractABI] = ccAbi
			}
		} else {
			// use cached abi
			*ccAbi = *ContractsABIsMap[contractABI]
		}
	}

	return nil
}

// chooseContractCallerABI extracts and returns the abo.ABI object from the contractCallerObj based on its abi string
func chooseContractCallerABI(contractCallerObj *ContractCaller, abi string) (*abi.ABI, error) {
	switch abi {
	case rootchain.RootchainMetaData.ABI:
		return &contractCallerObj.RootChainABI, nil
	case stakinginfo.StakinginfoMetaData.ABI:
		return &contractCallerObj.StakingInfoABI, nil
	case validatorset.ValidatorsetMetaData.ABI:
		return &contractCallerObj.ValidatorSetABI, nil
	case statereceiver.StatereceiverMetaData.ABI:
		return &contractCallerObj.StateReceiverABI, nil
	case statesender.StatesenderMetaData.ABI:
		return &contractCallerObj.StateSenderABI, nil
	case stakemanager.StakemanagerMetaData.ABI:
		return &contractCallerObj.StakeManagerABI, nil
	case slashmanager.SlashmanagerMetaData.ABI:
		return &contractCallerObj.SlashManagerABI, nil
	case erc20.Erc20MetaData.ABI:
		return &contractCallerObj.PolTokenABI, nil
	}

	return nil, errors.New("no ABI associated with such data")
}

// getABI returns the contract's ABI struct from on its JSON representation
func getABI(data string) (abi.ABI, error) {
	return abi.JSON(strings.NewReader(data))
}

// copied from bor/ethclient package
func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	if number.Sign() >= 0 {
		return hexutil.EncodeBig(number)
	}
	// It's negative.
	if number.IsInt64() {
		return rpc.BlockNumber(number.Int64()).String()
	}
	// It's negative and large, which is invalid.
	return fmt.Sprintf("<invalid %d>", number)
}
