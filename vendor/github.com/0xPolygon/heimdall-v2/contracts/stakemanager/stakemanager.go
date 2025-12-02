// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package stakemanager

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
)

// StakemanagerMetaData contains all meta data concerning the Stakemanager contract.
var StakemanagerMetaData = &bind.MetaData{
	ABI: "[{\"constant\":true,\"inputs\":[],\"name\":\"getCurrentValidatorSet\",\"outputs\":[{\"internalType\":\"uint256[]\",\"name\":\"\",\"type\":\"uint256[]\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"heimdallFee\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acceptDelegation\",\"type\":\"bool\"},{\"internalType\":\"bytes\",\"name\":\"signerPubkey\",\"type\":\"bytes\"}],\"name\":\"stake\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"blockInterval\",\"type\":\"uint256\"},{\"internalType\":\"bytes32\",\"name\":\"voteHash\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"stateRoot\",\"type\":\"bytes32\"},{\"internalType\":\"address\",\"name\":\"proposer\",\"type\":\"address\"},{\"internalType\":\"bytes\",\"name\":\"sigs\",\"type\":\"bytes\"}],\"name\":\"checkSignatures\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_limit\",\"type\":\"uint256\"}],\"name\":\"updateSignerUpdateLimit\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"auctionPeriod\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"totalRewards\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"WITHDRAWAL_DELAY\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"_registry\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_rootchain\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_NFTContract\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_stakingLogger\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_ValidatorShareFactory\",\"type\":\"address\"}],\"name\":\"updateConstructor\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"_token\",\"type\":\"address\"}],\"name\":\"setToken\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newThreshold\",\"type\":\"uint256\"}],\"name\":\"updateValidatorThreshold\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"getValidatorId\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"accountStateRoot\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"checkPointBlockInterval\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"isValidator\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"stakeRewards\",\"type\":\"bool\"}],\"name\":\"restake\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"unstake\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"NFTContract\",\"outputs\":[{\"internalType\":\"contractStakingNFT\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"proposerBonus\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"name\":\"validators\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"reward\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"activationEpoch\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deactivationEpoch\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"jailTime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"signer\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"contractAddress\",\"type\":\"address\"},{\"internalType\":\"enumStakeManagerStorage.Status\",\"name\":\"status\",\"type\":\"uint8\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"name\":\"signerToValidator\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"unJail\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"minDeposit\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"totalStakedFor\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"signerUpdateLimit\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"validatorThreshold\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"heimdallFee\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acceptDelegation\",\"type\":\"bool\"},{\"internalType\":\"bytes\",\"name\":\"signerPubkey\",\"type\":\"bytes\"}],\"name\":\"stakeFor\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"startAuction\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"name\":\"validatorAuction\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"startEpoch\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"delegationEnabled\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"NFTCounter\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"getValidatorContract\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"governance\",\"outputs\":[{\"internalType\":\"contractIGovernance\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"name\":\"validatorState\",\"outputs\":[{\"internalType\":\"int256\",\"name\":\"amount\",\"type\":\"int256\"},{\"internalType\":\"int256\",\"name\":\"stakerCount\",\"type\":\"int256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"_slashingInfoList\",\"type\":\"bytes\"}],\"name\":\"slash\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"tokenId\",\"type\":\"uint256\"}],\"name\":\"ownerOf\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"heimdallFee\",\"type\":\"uint256\"}],\"name\":\"topUpForFee\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"accumFeeAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"proof\",\"type\":\"bytes\"}],\"name\":\"claimFee\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"delegator\",\"type\":\"address\"}],\"name\":\"delegationDeposit\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"supportsHistory\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"pure\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"dynasty\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"currentEpoch\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"replacementCoolDown\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"name\":\"userFeeExit\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"registry\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"CHECKPOINT_REWARD\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"currentValidatorSetSize\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"totalStaked\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"isOwner\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"epoch\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"forceUnstake\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"withdrawRewards\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"rootChain\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"totalHeimdallFee\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newProposerBonus\",\"type\":\"uint256\"}],\"name\":\"updateProposerBonus\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"int256\",\"name\":\"amount\",\"type\":\"int256\"}],\"name\":\"updateValidatorState\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_blocks\",\"type\":\"uint256\"}],\"name\":\"updateCheckPointBlockInterval\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"currentValidatorSetTotalStake\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[],\"name\":\"unlock\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"withdrawalDelay\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_minDeposit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"_minHeimdallFee\",\"type\":\"uint256\"}],\"name\":\"updateMinAmounts\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"bytes32\",\"name\":\"voteHash\",\"type\":\"bytes32\"},{\"internalType\":\"bytes\",\"name\":\"sigs\",\"type\":\"bytes\"}],\"name\":\"verifyConsensus\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"delegator\",\"type\":\"address\"}],\"name\":\"transferFunds\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"factory\",\"outputs\":[{\"internalType\":\"contractValidatorShareFactory\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"heimdallFee\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acceptDelegation\",\"type\":\"bool\"},{\"internalType\":\"bytes\",\"name\":\"signerPubkey\",\"type\":\"bytes\"}],\"name\":\"confirmAuctionBid\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newReward\",\"type\":\"uint256\"}],\"name\":\"updateCheckpointReward\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"totalRewardsLiquidated\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"locked\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"pub\",\"type\":\"bytes\"}],\"name\":\"pubToAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"pure\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"name\":\"latestSignerUpdateEpoch\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"unstakeClaim\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newDynasty\",\"type\":\"uint256\"}],\"name\":\"updateDynastyValue\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"newRootChain\",\"type\":\"address\"}],\"name\":\"changeRootChain\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"}],\"name\":\"validatorStake\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"logger\",\"outputs\":[{\"internalType\":\"contractStakingInfo\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"bool\",\"name\":\"enabled\",\"type\":\"bool\"}],\"name\":\"setDelegationEnabled\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"validatorId\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signerPubkey\",\"type\":\"bytes\"}],\"name\":\"updateSigner\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"forNCheckpoints\",\"type\":\"uint256\"}],\"name\":\"stopAuctions\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[],\"name\":\"lock\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"minHeimdallFee\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"token\",\"outputs\":[{\"internalType\":\"contractIERC20\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousRootChain\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newRootChain\",\"type\":\"address\"}],\"name\":\"RootChainChanged\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"}]",
}

// StakemanagerABI is the input ABI used to generate the binding from.
// Deprecated: Use StakemanagerMetaData.ABI instead.
var StakemanagerABI = StakemanagerMetaData.ABI

// Stakemanager is an auto generated Go binding around an Ethereum contract.
type Stakemanager struct {
	StakemanagerCaller     // Read-only binding to the contract
	StakemanagerTransactor // Write-only binding to the contract
	StakemanagerFilterer   // Log filterer for contract events
}

// StakemanagerCaller is an auto generated read-only Go binding around an Ethereum contract.
type StakemanagerCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StakemanagerTransactor is an auto generated write-only Go binding around an Ethereum contract.
type StakemanagerTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StakemanagerFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type StakemanagerFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StakemanagerSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type StakemanagerSession struct {
	Contract     *Stakemanager     // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// StakemanagerCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type StakemanagerCallerSession struct {
	Contract *StakemanagerCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts       // Call options to use throughout this session
}

// StakemanagerTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type StakemanagerTransactorSession struct {
	Contract     *StakemanagerTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts       // Transaction auth options to use throughout this session
}

// StakemanagerRaw is an auto generated low-level Go binding around an Ethereum contract.
type StakemanagerRaw struct {
	Contract *Stakemanager // Generic contract binding to access the raw methods on
}

// StakemanagerCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type StakemanagerCallerRaw struct {
	Contract *StakemanagerCaller // Generic read-only contract binding to access the raw methods on
}

// StakemanagerTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type StakemanagerTransactorRaw struct {
	Contract *StakemanagerTransactor // Generic write-only contract binding to access the raw methods on
}

// NewStakemanager creates a new instance of Stakemanager, bound to a specific deployed contract.
func NewStakemanager(address common.Address, backend bind.ContractBackend) (*Stakemanager, error) {
	contract, err := bindStakemanager(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Stakemanager{StakemanagerCaller: StakemanagerCaller{contract: contract}, StakemanagerTransactor: StakemanagerTransactor{contract: contract}, StakemanagerFilterer: StakemanagerFilterer{contract: contract}}, nil
}

// NewStakemanagerCaller creates a new read-only instance of Stakemanager, bound to a specific deployed contract.
func NewStakemanagerCaller(address common.Address, caller bind.ContractCaller) (*StakemanagerCaller, error) {
	contract, err := bindStakemanager(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &StakemanagerCaller{contract: contract}, nil
}

// NewStakemanagerTransactor creates a new write-only instance of Stakemanager, bound to a specific deployed contract.
func NewStakemanagerTransactor(address common.Address, transactor bind.ContractTransactor) (*StakemanagerTransactor, error) {
	contract, err := bindStakemanager(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &StakemanagerTransactor{contract: contract}, nil
}

// NewStakemanagerFilterer creates a new log filterer instance of Stakemanager, bound to a specific deployed contract.
func NewStakemanagerFilterer(address common.Address, filterer bind.ContractFilterer) (*StakemanagerFilterer, error) {
	contract, err := bindStakemanager(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &StakemanagerFilterer{contract: contract}, nil
}

// bindStakemanager binds a generic wrapper to an already deployed contract.
func bindStakemanager(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(StakemanagerABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Stakemanager *StakemanagerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Stakemanager.Contract.StakemanagerCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Stakemanager *StakemanagerRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Stakemanager.Contract.StakemanagerTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Stakemanager *StakemanagerRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Stakemanager.Contract.StakemanagerTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Stakemanager *StakemanagerCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Stakemanager.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Stakemanager *StakemanagerTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Stakemanager.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Stakemanager *StakemanagerTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Stakemanager.Contract.contract.Transact(opts, method, params...)
}

// CHECKPOINTREWARD is a free data retrieval call binding the contract method 0x7d669752.
//
// Solidity: function CHECKPOINT_REWARD() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) CHECKPOINTREWARD(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "CHECKPOINT_REWARD")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CHECKPOINTREWARD is a free data retrieval call binding the contract method 0x7d669752.
//
// Solidity: function CHECKPOINT_REWARD() view returns(uint256)
func (_Stakemanager *StakemanagerSession) CHECKPOINTREWARD() (*big.Int, error) {
	return _Stakemanager.Contract.CHECKPOINTREWARD(&_Stakemanager.CallOpts)
}

// CHECKPOINTREWARD is a free data retrieval call binding the contract method 0x7d669752.
//
// Solidity: function CHECKPOINT_REWARD() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) CHECKPOINTREWARD() (*big.Int, error) {
	return _Stakemanager.Contract.CHECKPOINTREWARD(&_Stakemanager.CallOpts)
}

// NFTContract is a free data retrieval call binding the contract method 0x31c2273b.
//
// Solidity: function NFTContract() view returns(address)
func (_Stakemanager *StakemanagerCaller) NFTContract(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "NFTContract")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// NFTContract is a free data retrieval call binding the contract method 0x31c2273b.
//
// Solidity: function NFTContract() view returns(address)
func (_Stakemanager *StakemanagerSession) NFTContract() (common.Address, error) {
	return _Stakemanager.Contract.NFTContract(&_Stakemanager.CallOpts)
}

// NFTContract is a free data retrieval call binding the contract method 0x31c2273b.
//
// Solidity: function NFTContract() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) NFTContract() (common.Address, error) {
	return _Stakemanager.Contract.NFTContract(&_Stakemanager.CallOpts)
}

// NFTCounter is a free data retrieval call binding the contract method 0x5508d8e1.
//
// Solidity: function NFTCounter() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) NFTCounter(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "NFTCounter")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// NFTCounter is a free data retrieval call binding the contract method 0x5508d8e1.
//
// Solidity: function NFTCounter() view returns(uint256)
func (_Stakemanager *StakemanagerSession) NFTCounter() (*big.Int, error) {
	return _Stakemanager.Contract.NFTCounter(&_Stakemanager.CallOpts)
}

// NFTCounter is a free data retrieval call binding the contract method 0x5508d8e1.
//
// Solidity: function NFTCounter() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) NFTCounter() (*big.Int, error) {
	return _Stakemanager.Contract.NFTCounter(&_Stakemanager.CallOpts)
}

// WITHDRAWALDELAY is a free data retrieval call binding the contract method 0x0ebb172a.
//
// Solidity: function WITHDRAWAL_DELAY() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) WITHDRAWALDELAY(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "WITHDRAWAL_DELAY")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// WITHDRAWALDELAY is a free data retrieval call binding the contract method 0x0ebb172a.
//
// Solidity: function WITHDRAWAL_DELAY() view returns(uint256)
func (_Stakemanager *StakemanagerSession) WITHDRAWALDELAY() (*big.Int, error) {
	return _Stakemanager.Contract.WITHDRAWALDELAY(&_Stakemanager.CallOpts)
}

// WITHDRAWALDELAY is a free data retrieval call binding the contract method 0x0ebb172a.
//
// Solidity: function WITHDRAWAL_DELAY() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) WITHDRAWALDELAY() (*big.Int, error) {
	return _Stakemanager.Contract.WITHDRAWALDELAY(&_Stakemanager.CallOpts)
}

// AccountStateRoot is a free data retrieval call binding the contract method 0x17c2b910.
//
// Solidity: function accountStateRoot() view returns(bytes32)
func (_Stakemanager *StakemanagerCaller) AccountStateRoot(opts *bind.CallOpts) ([32]byte, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "accountStateRoot")
	if err != nil {
		return *new([32]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([32]byte)).(*[32]byte)

	return out0, err
}

// AccountStateRoot is a free data retrieval call binding the contract method 0x17c2b910.
//
// Solidity: function accountStateRoot() view returns(bytes32)
func (_Stakemanager *StakemanagerSession) AccountStateRoot() ([32]byte, error) {
	return _Stakemanager.Contract.AccountStateRoot(&_Stakemanager.CallOpts)
}

// AccountStateRoot is a free data retrieval call binding the contract method 0x17c2b910.
//
// Solidity: function accountStateRoot() view returns(bytes32)
func (_Stakemanager *StakemanagerCallerSession) AccountStateRoot() ([32]byte, error) {
	return _Stakemanager.Contract.AccountStateRoot(&_Stakemanager.CallOpts)
}

// AuctionPeriod is a free data retrieval call binding the contract method 0x0cccfc58.
//
// Solidity: function auctionPeriod() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) AuctionPeriod(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "auctionPeriod")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// AuctionPeriod is a free data retrieval call binding the contract method 0x0cccfc58.
//
// Solidity: function auctionPeriod() view returns(uint256)
func (_Stakemanager *StakemanagerSession) AuctionPeriod() (*big.Int, error) {
	return _Stakemanager.Contract.AuctionPeriod(&_Stakemanager.CallOpts)
}

// AuctionPeriod is a free data retrieval call binding the contract method 0x0cccfc58.
//
// Solidity: function auctionPeriod() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) AuctionPeriod() (*big.Int, error) {
	return _Stakemanager.Contract.AuctionPeriod(&_Stakemanager.CallOpts)
}

// CheckPointBlockInterval is a free data retrieval call binding the contract method 0x25316411.
//
// Solidity: function checkPointBlockInterval() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) CheckPointBlockInterval(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "checkPointBlockInterval")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CheckPointBlockInterval is a free data retrieval call binding the contract method 0x25316411.
//
// Solidity: function checkPointBlockInterval() view returns(uint256)
func (_Stakemanager *StakemanagerSession) CheckPointBlockInterval() (*big.Int, error) {
	return _Stakemanager.Contract.CheckPointBlockInterval(&_Stakemanager.CallOpts)
}

// CheckPointBlockInterval is a free data retrieval call binding the contract method 0x25316411.
//
// Solidity: function checkPointBlockInterval() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) CheckPointBlockInterval() (*big.Int, error) {
	return _Stakemanager.Contract.CheckPointBlockInterval(&_Stakemanager.CallOpts)
}

// CurrentEpoch is a free data retrieval call binding the contract method 0x76671808.
//
// Solidity: function currentEpoch() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) CurrentEpoch(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "currentEpoch")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CurrentEpoch is a free data retrieval call binding the contract method 0x76671808.
//
// Solidity: function currentEpoch() view returns(uint256)
func (_Stakemanager *StakemanagerSession) CurrentEpoch() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentEpoch(&_Stakemanager.CallOpts)
}

// CurrentEpoch is a free data retrieval call binding the contract method 0x76671808.
//
// Solidity: function currentEpoch() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) CurrentEpoch() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentEpoch(&_Stakemanager.CallOpts)
}

// CurrentValidatorSetSize is a free data retrieval call binding the contract method 0x7f952d95.
//
// Solidity: function currentValidatorSetSize() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) CurrentValidatorSetSize(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "currentValidatorSetSize")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CurrentValidatorSetSize is a free data retrieval call binding the contract method 0x7f952d95.
//
// Solidity: function currentValidatorSetSize() view returns(uint256)
func (_Stakemanager *StakemanagerSession) CurrentValidatorSetSize() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentValidatorSetSize(&_Stakemanager.CallOpts)
}

// CurrentValidatorSetSize is a free data retrieval call binding the contract method 0x7f952d95.
//
// Solidity: function currentValidatorSetSize() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) CurrentValidatorSetSize() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentValidatorSetSize(&_Stakemanager.CallOpts)
}

// CurrentValidatorSetTotalStake is a free data retrieval call binding the contract method 0xa4769071.
//
// Solidity: function currentValidatorSetTotalStake() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) CurrentValidatorSetTotalStake(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "currentValidatorSetTotalStake")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CurrentValidatorSetTotalStake is a free data retrieval call binding the contract method 0xa4769071.
//
// Solidity: function currentValidatorSetTotalStake() view returns(uint256)
func (_Stakemanager *StakemanagerSession) CurrentValidatorSetTotalStake() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentValidatorSetTotalStake(&_Stakemanager.CallOpts)
}

// CurrentValidatorSetTotalStake is a free data retrieval call binding the contract method 0xa4769071.
//
// Solidity: function currentValidatorSetTotalStake() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) CurrentValidatorSetTotalStake() (*big.Int, error) {
	return _Stakemanager.Contract.CurrentValidatorSetTotalStake(&_Stakemanager.CallOpts)
}

// DelegationEnabled is a free data retrieval call binding the contract method 0x54b8c601.
//
// Solidity: function delegationEnabled() view returns(bool)
func (_Stakemanager *StakemanagerCaller) DelegationEnabled(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "delegationEnabled")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// DelegationEnabled is a free data retrieval call binding the contract method 0x54b8c601.
//
// Solidity: function delegationEnabled() view returns(bool)
func (_Stakemanager *StakemanagerSession) DelegationEnabled() (bool, error) {
	return _Stakemanager.Contract.DelegationEnabled(&_Stakemanager.CallOpts)
}

// DelegationEnabled is a free data retrieval call binding the contract method 0x54b8c601.
//
// Solidity: function delegationEnabled() view returns(bool)
func (_Stakemanager *StakemanagerCallerSession) DelegationEnabled() (bool, error) {
	return _Stakemanager.Contract.DelegationEnabled(&_Stakemanager.CallOpts)
}

// Dynasty is a free data retrieval call binding the contract method 0x7060054d.
//
// Solidity: function dynasty() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) Dynasty(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "dynasty")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// Dynasty is a free data retrieval call binding the contract method 0x7060054d.
//
// Solidity: function dynasty() view returns(uint256)
func (_Stakemanager *StakemanagerSession) Dynasty() (*big.Int, error) {
	return _Stakemanager.Contract.Dynasty(&_Stakemanager.CallOpts)
}

// Dynasty is a free data retrieval call binding the contract method 0x7060054d.
//
// Solidity: function dynasty() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) Dynasty() (*big.Int, error) {
	return _Stakemanager.Contract.Dynasty(&_Stakemanager.CallOpts)
}

// Epoch is a free data retrieval call binding the contract method 0x900cf0cf.
//
// Solidity: function epoch() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) Epoch(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "epoch")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// Epoch is a free data retrieval call binding the contract method 0x900cf0cf.
//
// Solidity: function epoch() view returns(uint256)
func (_Stakemanager *StakemanagerSession) Epoch() (*big.Int, error) {
	return _Stakemanager.Contract.Epoch(&_Stakemanager.CallOpts)
}

// Epoch is a free data retrieval call binding the contract method 0x900cf0cf.
//
// Solidity: function epoch() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) Epoch() (*big.Int, error) {
	return _Stakemanager.Contract.Epoch(&_Stakemanager.CallOpts)
}

// Factory is a free data retrieval call binding the contract method 0xc45a0155.
//
// Solidity: function factory() view returns(address)
func (_Stakemanager *StakemanagerCaller) Factory(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "factory")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Factory is a free data retrieval call binding the contract method 0xc45a0155.
//
// Solidity: function factory() view returns(address)
func (_Stakemanager *StakemanagerSession) Factory() (common.Address, error) {
	return _Stakemanager.Contract.Factory(&_Stakemanager.CallOpts)
}

// Factory is a free data retrieval call binding the contract method 0xc45a0155.
//
// Solidity: function factory() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Factory() (common.Address, error) {
	return _Stakemanager.Contract.Factory(&_Stakemanager.CallOpts)
}

// GetCurrentValidatorSet is a free data retrieval call binding the contract method 0x0209fdd0.
//
// Solidity: function getCurrentValidatorSet() view returns(uint256[])
func (_Stakemanager *StakemanagerCaller) GetCurrentValidatorSet(opts *bind.CallOpts) ([]*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "getCurrentValidatorSet")
	if err != nil {
		return *new([]*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new([]*big.Int)).(*[]*big.Int)

	return out0, err
}

// GetCurrentValidatorSet is a free data retrieval call binding the contract method 0x0209fdd0.
//
// Solidity: function getCurrentValidatorSet() view returns(uint256[])
func (_Stakemanager *StakemanagerSession) GetCurrentValidatorSet() ([]*big.Int, error) {
	return _Stakemanager.Contract.GetCurrentValidatorSet(&_Stakemanager.CallOpts)
}

// GetCurrentValidatorSet is a free data retrieval call binding the contract method 0x0209fdd0.
//
// Solidity: function getCurrentValidatorSet() view returns(uint256[])
func (_Stakemanager *StakemanagerCallerSession) GetCurrentValidatorSet() ([]*big.Int, error) {
	return _Stakemanager.Contract.GetCurrentValidatorSet(&_Stakemanager.CallOpts)
}

// GetValidatorContract is a free data retrieval call binding the contract method 0x56342d8c.
//
// Solidity: function getValidatorContract(uint256 validatorId) view returns(address)
func (_Stakemanager *StakemanagerCaller) GetValidatorContract(opts *bind.CallOpts, validatorId *big.Int) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "getValidatorContract", validatorId)
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// GetValidatorContract is a free data retrieval call binding the contract method 0x56342d8c.
//
// Solidity: function getValidatorContract(uint256 validatorId) view returns(address)
func (_Stakemanager *StakemanagerSession) GetValidatorContract(validatorId *big.Int) (common.Address, error) {
	return _Stakemanager.Contract.GetValidatorContract(&_Stakemanager.CallOpts, validatorId)
}

// GetValidatorContract is a free data retrieval call binding the contract method 0x56342d8c.
//
// Solidity: function getValidatorContract(uint256 validatorId) view returns(address)
func (_Stakemanager *StakemanagerCallerSession) GetValidatorContract(validatorId *big.Int) (common.Address, error) {
	return _Stakemanager.Contract.GetValidatorContract(&_Stakemanager.CallOpts, validatorId)
}

// GetValidatorId is a free data retrieval call binding the contract method 0x174e6832.
//
// Solidity: function getValidatorId(address user) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) GetValidatorId(opts *bind.CallOpts, user common.Address) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "getValidatorId", user)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// GetValidatorId is a free data retrieval call binding the contract method 0x174e6832.
//
// Solidity: function getValidatorId(address user) view returns(uint256)
func (_Stakemanager *StakemanagerSession) GetValidatorId(user common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.GetValidatorId(&_Stakemanager.CallOpts, user)
}

// GetValidatorId is a free data retrieval call binding the contract method 0x174e6832.
//
// Solidity: function getValidatorId(address user) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) GetValidatorId(user common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.GetValidatorId(&_Stakemanager.CallOpts, user)
}

// Governance is a free data retrieval call binding the contract method 0x5aa6e675.
//
// Solidity: function governance() view returns(address)
func (_Stakemanager *StakemanagerCaller) Governance(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "governance")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Governance is a free data retrieval call binding the contract method 0x5aa6e675.
//
// Solidity: function governance() view returns(address)
func (_Stakemanager *StakemanagerSession) Governance() (common.Address, error) {
	return _Stakemanager.Contract.Governance(&_Stakemanager.CallOpts)
}

// Governance is a free data retrieval call binding the contract method 0x5aa6e675.
//
// Solidity: function governance() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Governance() (common.Address, error) {
	return _Stakemanager.Contract.Governance(&_Stakemanager.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Stakemanager *StakemanagerCaller) IsOwner(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "isOwner")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Stakemanager *StakemanagerSession) IsOwner() (bool, error) {
	return _Stakemanager.Contract.IsOwner(&_Stakemanager.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Stakemanager *StakemanagerCallerSession) IsOwner() (bool, error) {
	return _Stakemanager.Contract.IsOwner(&_Stakemanager.CallOpts)
}

// IsValidator is a free data retrieval call binding the contract method 0x2649263a.
//
// Solidity: function isValidator(uint256 validatorId) view returns(bool)
func (_Stakemanager *StakemanagerCaller) IsValidator(opts *bind.CallOpts, validatorId *big.Int) (bool, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "isValidator", validatorId)
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// IsValidator is a free data retrieval call binding the contract method 0x2649263a.
//
// Solidity: function isValidator(uint256 validatorId) view returns(bool)
func (_Stakemanager *StakemanagerSession) IsValidator(validatorId *big.Int) (bool, error) {
	return _Stakemanager.Contract.IsValidator(&_Stakemanager.CallOpts, validatorId)
}

// IsValidator is a free data retrieval call binding the contract method 0x2649263a.
//
// Solidity: function isValidator(uint256 validatorId) view returns(bool)
func (_Stakemanager *StakemanagerCallerSession) IsValidator(validatorId *big.Int) (bool, error) {
	return _Stakemanager.Contract.IsValidator(&_Stakemanager.CallOpts, validatorId)
}

// LatestSignerUpdateEpoch is a free data retrieval call binding the contract method 0xd7f5549d.
//
// Solidity: function latestSignerUpdateEpoch(uint256 ) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) LatestSignerUpdateEpoch(opts *bind.CallOpts, arg0 *big.Int) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "latestSignerUpdateEpoch", arg0)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// LatestSignerUpdateEpoch is a free data retrieval call binding the contract method 0xd7f5549d.
//
// Solidity: function latestSignerUpdateEpoch(uint256 ) view returns(uint256)
func (_Stakemanager *StakemanagerSession) LatestSignerUpdateEpoch(arg0 *big.Int) (*big.Int, error) {
	return _Stakemanager.Contract.LatestSignerUpdateEpoch(&_Stakemanager.CallOpts, arg0)
}

// LatestSignerUpdateEpoch is a free data retrieval call binding the contract method 0xd7f5549d.
//
// Solidity: function latestSignerUpdateEpoch(uint256 ) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) LatestSignerUpdateEpoch(arg0 *big.Int) (*big.Int, error) {
	return _Stakemanager.Contract.LatestSignerUpdateEpoch(&_Stakemanager.CallOpts, arg0)
}

// Locked is a free data retrieval call binding the contract method 0xcf309012.
//
// Solidity: function locked() view returns(bool)
func (_Stakemanager *StakemanagerCaller) Locked(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "locked")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// Locked is a free data retrieval call binding the contract method 0xcf309012.
//
// Solidity: function locked() view returns(bool)
func (_Stakemanager *StakemanagerSession) Locked() (bool, error) {
	return _Stakemanager.Contract.Locked(&_Stakemanager.CallOpts)
}

// Locked is a free data retrieval call binding the contract method 0xcf309012.
//
// Solidity: function locked() view returns(bool)
func (_Stakemanager *StakemanagerCallerSession) Locked() (bool, error) {
	return _Stakemanager.Contract.Locked(&_Stakemanager.CallOpts)
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Stakemanager *StakemanagerCaller) Logger(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "logger")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Stakemanager *StakemanagerSession) Logger() (common.Address, error) {
	return _Stakemanager.Contract.Logger(&_Stakemanager.CallOpts)
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Logger() (common.Address, error) {
	return _Stakemanager.Contract.Logger(&_Stakemanager.CallOpts)
}

// MinDeposit is a free data retrieval call binding the contract method 0x41b3d185.
//
// Solidity: function minDeposit() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) MinDeposit(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "minDeposit")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// MinDeposit is a free data retrieval call binding the contract method 0x41b3d185.
//
// Solidity: function minDeposit() view returns(uint256)
func (_Stakemanager *StakemanagerSession) MinDeposit() (*big.Int, error) {
	return _Stakemanager.Contract.MinDeposit(&_Stakemanager.CallOpts)
}

// MinDeposit is a free data retrieval call binding the contract method 0x41b3d185.
//
// Solidity: function minDeposit() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) MinDeposit() (*big.Int, error) {
	return _Stakemanager.Contract.MinDeposit(&_Stakemanager.CallOpts)
}

// MinHeimdallFee is a free data retrieval call binding the contract method 0xfba58f34.
//
// Solidity: function minHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) MinHeimdallFee(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "minHeimdallFee")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// MinHeimdallFee is a free data retrieval call binding the contract method 0xfba58f34.
//
// Solidity: function minHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerSession) MinHeimdallFee() (*big.Int, error) {
	return _Stakemanager.Contract.MinHeimdallFee(&_Stakemanager.CallOpts)
}

// MinHeimdallFee is a free data retrieval call binding the contract method 0xfba58f34.
//
// Solidity: function minHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) MinHeimdallFee() (*big.Int, error) {
	return _Stakemanager.Contract.MinHeimdallFee(&_Stakemanager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Stakemanager *StakemanagerCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "owner")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Stakemanager *StakemanagerSession) Owner() (common.Address, error) {
	return _Stakemanager.Contract.Owner(&_Stakemanager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Owner() (common.Address, error) {
	return _Stakemanager.Contract.Owner(&_Stakemanager.CallOpts)
}

// OwnerOf is a free data retrieval call binding the contract method 0x6352211e.
//
// Solidity: function ownerOf(uint256 tokenId) view returns(address)
func (_Stakemanager *StakemanagerCaller) OwnerOf(opts *bind.CallOpts, tokenId *big.Int) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "ownerOf", tokenId)
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// OwnerOf is a free data retrieval call binding the contract method 0x6352211e.
//
// Solidity: function ownerOf(uint256 tokenId) view returns(address)
func (_Stakemanager *StakemanagerSession) OwnerOf(tokenId *big.Int) (common.Address, error) {
	return _Stakemanager.Contract.OwnerOf(&_Stakemanager.CallOpts, tokenId)
}

// OwnerOf is a free data retrieval call binding the contract method 0x6352211e.
//
// Solidity: function ownerOf(uint256 tokenId) view returns(address)
func (_Stakemanager *StakemanagerCallerSession) OwnerOf(tokenId *big.Int) (common.Address, error) {
	return _Stakemanager.Contract.OwnerOf(&_Stakemanager.CallOpts, tokenId)
}

// ProposerBonus is a free data retrieval call binding the contract method 0x34274586.
//
// Solidity: function proposerBonus() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) ProposerBonus(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "proposerBonus")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ProposerBonus is a free data retrieval call binding the contract method 0x34274586.
//
// Solidity: function proposerBonus() view returns(uint256)
func (_Stakemanager *StakemanagerSession) ProposerBonus() (*big.Int, error) {
	return _Stakemanager.Contract.ProposerBonus(&_Stakemanager.CallOpts)
}

// ProposerBonus is a free data retrieval call binding the contract method 0x34274586.
//
// Solidity: function proposerBonus() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) ProposerBonus() (*big.Int, error) {
	return _Stakemanager.Contract.ProposerBonus(&_Stakemanager.CallOpts)
}

// PubToAddress is a free data retrieval call binding the contract method 0xd0110274.
//
// Solidity: function pubToAddress(bytes pub) pure returns(address)
func (_Stakemanager *StakemanagerCaller) PubToAddress(opts *bind.CallOpts, pub []byte) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "pubToAddress", pub)
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// PubToAddress is a free data retrieval call binding the contract method 0xd0110274.
//
// Solidity: function pubToAddress(bytes pub) pure returns(address)
func (_Stakemanager *StakemanagerSession) PubToAddress(pub []byte) (common.Address, error) {
	return _Stakemanager.Contract.PubToAddress(&_Stakemanager.CallOpts, pub)
}

// PubToAddress is a free data retrieval call binding the contract method 0xd0110274.
//
// Solidity: function pubToAddress(bytes pub) pure returns(address)
func (_Stakemanager *StakemanagerCallerSession) PubToAddress(pub []byte) (common.Address, error) {
	return _Stakemanager.Contract.PubToAddress(&_Stakemanager.CallOpts, pub)
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Stakemanager *StakemanagerCaller) Registry(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "registry")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Stakemanager *StakemanagerSession) Registry() (common.Address, error) {
	return _Stakemanager.Contract.Registry(&_Stakemanager.CallOpts)
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Registry() (common.Address, error) {
	return _Stakemanager.Contract.Registry(&_Stakemanager.CallOpts)
}

// ReplacementCoolDown is a free data retrieval call binding the contract method 0x77939d10.
//
// Solidity: function replacementCoolDown() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) ReplacementCoolDown(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "replacementCoolDown")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ReplacementCoolDown is a free data retrieval call binding the contract method 0x77939d10.
//
// Solidity: function replacementCoolDown() view returns(uint256)
func (_Stakemanager *StakemanagerSession) ReplacementCoolDown() (*big.Int, error) {
	return _Stakemanager.Contract.ReplacementCoolDown(&_Stakemanager.CallOpts)
}

// ReplacementCoolDown is a free data retrieval call binding the contract method 0x77939d10.
//
// Solidity: function replacementCoolDown() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) ReplacementCoolDown() (*big.Int, error) {
	return _Stakemanager.Contract.ReplacementCoolDown(&_Stakemanager.CallOpts)
}

// RootChain is a free data retrieval call binding the contract method 0x987ab9db.
//
// Solidity: function rootChain() view returns(address)
func (_Stakemanager *StakemanagerCaller) RootChain(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "rootChain")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// RootChain is a free data retrieval call binding the contract method 0x987ab9db.
//
// Solidity: function rootChain() view returns(address)
func (_Stakemanager *StakemanagerSession) RootChain() (common.Address, error) {
	return _Stakemanager.Contract.RootChain(&_Stakemanager.CallOpts)
}

// RootChain is a free data retrieval call binding the contract method 0x987ab9db.
//
// Solidity: function rootChain() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) RootChain() (common.Address, error) {
	return _Stakemanager.Contract.RootChain(&_Stakemanager.CallOpts)
}

// SignerToValidator is a free data retrieval call binding the contract method 0x3862da0b.
//
// Solidity: function signerToValidator(address ) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) SignerToValidator(opts *bind.CallOpts, arg0 common.Address) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "signerToValidator", arg0)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// SignerToValidator is a free data retrieval call binding the contract method 0x3862da0b.
//
// Solidity: function signerToValidator(address ) view returns(uint256)
func (_Stakemanager *StakemanagerSession) SignerToValidator(arg0 common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.SignerToValidator(&_Stakemanager.CallOpts, arg0)
}

// SignerToValidator is a free data retrieval call binding the contract method 0x3862da0b.
//
// Solidity: function signerToValidator(address ) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) SignerToValidator(arg0 common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.SignerToValidator(&_Stakemanager.CallOpts, arg0)
}

// SignerUpdateLimit is a free data retrieval call binding the contract method 0x4e3c83f1.
//
// Solidity: function signerUpdateLimit() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) SignerUpdateLimit(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "signerUpdateLimit")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// SignerUpdateLimit is a free data retrieval call binding the contract method 0x4e3c83f1.
//
// Solidity: function signerUpdateLimit() view returns(uint256)
func (_Stakemanager *StakemanagerSession) SignerUpdateLimit() (*big.Int, error) {
	return _Stakemanager.Contract.SignerUpdateLimit(&_Stakemanager.CallOpts)
}

// SignerUpdateLimit is a free data retrieval call binding the contract method 0x4e3c83f1.
//
// Solidity: function signerUpdateLimit() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) SignerUpdateLimit() (*big.Int, error) {
	return _Stakemanager.Contract.SignerUpdateLimit(&_Stakemanager.CallOpts)
}

// SupportsHistory is a free data retrieval call binding the contract method 0x7033e4a6.
//
// Solidity: function supportsHistory() pure returns(bool)
func (_Stakemanager *StakemanagerCaller) SupportsHistory(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "supportsHistory")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// SupportsHistory is a free data retrieval call binding the contract method 0x7033e4a6.
//
// Solidity: function supportsHistory() pure returns(bool)
func (_Stakemanager *StakemanagerSession) SupportsHistory() (bool, error) {
	return _Stakemanager.Contract.SupportsHistory(&_Stakemanager.CallOpts)
}

// SupportsHistory is a free data retrieval call binding the contract method 0x7033e4a6.
//
// Solidity: function supportsHistory() pure returns(bool)
func (_Stakemanager *StakemanagerCallerSession) SupportsHistory() (bool, error) {
	return _Stakemanager.Contract.SupportsHistory(&_Stakemanager.CallOpts)
}

// Token is a free data retrieval call binding the contract method 0xfc0c546a.
//
// Solidity: function token() view returns(address)
func (_Stakemanager *StakemanagerCaller) Token(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "token")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Token is a free data retrieval call binding the contract method 0xfc0c546a.
//
// Solidity: function token() view returns(address)
func (_Stakemanager *StakemanagerSession) Token() (common.Address, error) {
	return _Stakemanager.Contract.Token(&_Stakemanager.CallOpts)
}

// Token is a free data retrieval call binding the contract method 0xfc0c546a.
//
// Solidity: function token() view returns(address)
func (_Stakemanager *StakemanagerCallerSession) Token() (common.Address, error) {
	return _Stakemanager.Contract.Token(&_Stakemanager.CallOpts)
}

// TotalHeimdallFee is a free data retrieval call binding the contract method 0x9a8a6243.
//
// Solidity: function totalHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) TotalHeimdallFee(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "totalHeimdallFee")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// TotalHeimdallFee is a free data retrieval call binding the contract method 0x9a8a6243.
//
// Solidity: function totalHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerSession) TotalHeimdallFee() (*big.Int, error) {
	return _Stakemanager.Contract.TotalHeimdallFee(&_Stakemanager.CallOpts)
}

// TotalHeimdallFee is a free data retrieval call binding the contract method 0x9a8a6243.
//
// Solidity: function totalHeimdallFee() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) TotalHeimdallFee() (*big.Int, error) {
	return _Stakemanager.Contract.TotalHeimdallFee(&_Stakemanager.CallOpts)
}

// TotalRewards is a free data retrieval call binding the contract method 0x0e15561a.
//
// Solidity: function totalRewards() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) TotalRewards(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "totalRewards")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// TotalRewards is a free data retrieval call binding the contract method 0x0e15561a.
//
// Solidity: function totalRewards() view returns(uint256)
func (_Stakemanager *StakemanagerSession) TotalRewards() (*big.Int, error) {
	return _Stakemanager.Contract.TotalRewards(&_Stakemanager.CallOpts)
}

// TotalRewards is a free data retrieval call binding the contract method 0x0e15561a.
//
// Solidity: function totalRewards() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) TotalRewards() (*big.Int, error) {
	return _Stakemanager.Contract.TotalRewards(&_Stakemanager.CallOpts)
}

// TotalRewardsLiquidated is a free data retrieval call binding the contract method 0xcd6b8388.
//
// Solidity: function totalRewardsLiquidated() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) TotalRewardsLiquidated(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "totalRewardsLiquidated")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// TotalRewardsLiquidated is a free data retrieval call binding the contract method 0xcd6b8388.
//
// Solidity: function totalRewardsLiquidated() view returns(uint256)
func (_Stakemanager *StakemanagerSession) TotalRewardsLiquidated() (*big.Int, error) {
	return _Stakemanager.Contract.TotalRewardsLiquidated(&_Stakemanager.CallOpts)
}

// TotalRewardsLiquidated is a free data retrieval call binding the contract method 0xcd6b8388.
//
// Solidity: function totalRewardsLiquidated() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) TotalRewardsLiquidated() (*big.Int, error) {
	return _Stakemanager.Contract.TotalRewardsLiquidated(&_Stakemanager.CallOpts)
}

// TotalStaked is a free data retrieval call binding the contract method 0x817b1cd2.
//
// Solidity: function totalStaked() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) TotalStaked(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "totalStaked")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// TotalStaked is a free data retrieval call binding the contract method 0x817b1cd2.
//
// Solidity: function totalStaked() view returns(uint256)
func (_Stakemanager *StakemanagerSession) TotalStaked() (*big.Int, error) {
	return _Stakemanager.Contract.TotalStaked(&_Stakemanager.CallOpts)
}

// TotalStaked is a free data retrieval call binding the contract method 0x817b1cd2.
//
// Solidity: function totalStaked() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) TotalStaked() (*big.Int, error) {
	return _Stakemanager.Contract.TotalStaked(&_Stakemanager.CallOpts)
}

// TotalStakedFor is a free data retrieval call binding the contract method 0x4b341aed.
//
// Solidity: function totalStakedFor(address user) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) TotalStakedFor(opts *bind.CallOpts, user common.Address) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "totalStakedFor", user)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// TotalStakedFor is a free data retrieval call binding the contract method 0x4b341aed.
//
// Solidity: function totalStakedFor(address user) view returns(uint256)
func (_Stakemanager *StakemanagerSession) TotalStakedFor(user common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.TotalStakedFor(&_Stakemanager.CallOpts, user)
}

// TotalStakedFor is a free data retrieval call binding the contract method 0x4b341aed.
//
// Solidity: function totalStakedFor(address user) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) TotalStakedFor(user common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.TotalStakedFor(&_Stakemanager.CallOpts, user)
}

// UserFeeExit is a free data retrieval call binding the contract method 0x78f84a44.
//
// Solidity: function userFeeExit(address ) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) UserFeeExit(opts *bind.CallOpts, arg0 common.Address) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "userFeeExit", arg0)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// UserFeeExit is a free data retrieval call binding the contract method 0x78f84a44.
//
// Solidity: function userFeeExit(address ) view returns(uint256)
func (_Stakemanager *StakemanagerSession) UserFeeExit(arg0 common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.UserFeeExit(&_Stakemanager.CallOpts, arg0)
}

// UserFeeExit is a free data retrieval call binding the contract method 0x78f84a44.
//
// Solidity: function userFeeExit(address ) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) UserFeeExit(arg0 common.Address) (*big.Int, error) {
	return _Stakemanager.Contract.UserFeeExit(&_Stakemanager.CallOpts, arg0)
}

// ValidatorAuction is a free data retrieval call binding the contract method 0x5325e144.
//
// Solidity: function validatorAuction(uint256 ) view returns(uint256 amount, uint256 startEpoch, address user)
func (_Stakemanager *StakemanagerCaller) ValidatorAuction(opts *bind.CallOpts, arg0 *big.Int) (struct {
	Amount     *big.Int
	StartEpoch *big.Int
	User       common.Address
}, error,
) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "validatorAuction", arg0)

	outstruct := new(struct {
		Amount     *big.Int
		StartEpoch *big.Int
		User       common.Address
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Amount = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.StartEpoch = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.User = *abi.ConvertType(out[2], new(common.Address)).(*common.Address)

	return *outstruct, err
}

// ValidatorAuction is a free data retrieval call binding the contract method 0x5325e144.
//
// Solidity: function validatorAuction(uint256 ) view returns(uint256 amount, uint256 startEpoch, address user)
func (_Stakemanager *StakemanagerSession) ValidatorAuction(arg0 *big.Int) (struct {
	Amount     *big.Int
	StartEpoch *big.Int
	User       common.Address
}, error,
) {
	return _Stakemanager.Contract.ValidatorAuction(&_Stakemanager.CallOpts, arg0)
}

// ValidatorAuction is a free data retrieval call binding the contract method 0x5325e144.
//
// Solidity: function validatorAuction(uint256 ) view returns(uint256 amount, uint256 startEpoch, address user)
func (_Stakemanager *StakemanagerCallerSession) ValidatorAuction(arg0 *big.Int) (struct {
	Amount     *big.Int
	StartEpoch *big.Int
	User       common.Address
}, error,
) {
	return _Stakemanager.Contract.ValidatorAuction(&_Stakemanager.CallOpts, arg0)
}

// ValidatorStake is a free data retrieval call binding the contract method 0xeceec1d3.
//
// Solidity: function validatorStake(uint256 validatorId) view returns(uint256)
func (_Stakemanager *StakemanagerCaller) ValidatorStake(opts *bind.CallOpts, validatorId *big.Int) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "validatorStake", validatorId)
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ValidatorStake is a free data retrieval call binding the contract method 0xeceec1d3.
//
// Solidity: function validatorStake(uint256 validatorId) view returns(uint256)
func (_Stakemanager *StakemanagerSession) ValidatorStake(validatorId *big.Int) (*big.Int, error) {
	return _Stakemanager.Contract.ValidatorStake(&_Stakemanager.CallOpts, validatorId)
}

// ValidatorStake is a free data retrieval call binding the contract method 0xeceec1d3.
//
// Solidity: function validatorStake(uint256 validatorId) view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) ValidatorStake(validatorId *big.Int) (*big.Int, error) {
	return _Stakemanager.Contract.ValidatorStake(&_Stakemanager.CallOpts, validatorId)
}

// ValidatorState is a free data retrieval call binding the contract method 0x5c248855.
//
// Solidity: function validatorState(uint256 ) view returns(int256 amount, int256 stakerCount)
func (_Stakemanager *StakemanagerCaller) ValidatorState(opts *bind.CallOpts, arg0 *big.Int) (struct {
	Amount      *big.Int
	StakerCount *big.Int
}, error,
) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "validatorState", arg0)

	outstruct := new(struct {
		Amount      *big.Int
		StakerCount *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Amount = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.StakerCount = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err
}

// ValidatorState is a free data retrieval call binding the contract method 0x5c248855.
//
// Solidity: function validatorState(uint256 ) view returns(int256 amount, int256 stakerCount)
func (_Stakemanager *StakemanagerSession) ValidatorState(arg0 *big.Int) (struct {
	Amount      *big.Int
	StakerCount *big.Int
}, error,
) {
	return _Stakemanager.Contract.ValidatorState(&_Stakemanager.CallOpts, arg0)
}

// ValidatorState is a free data retrieval call binding the contract method 0x5c248855.
//
// Solidity: function validatorState(uint256 ) view returns(int256 amount, int256 stakerCount)
func (_Stakemanager *StakemanagerCallerSession) ValidatorState(arg0 *big.Int) (struct {
	Amount      *big.Int
	StakerCount *big.Int
}, error,
) {
	return _Stakemanager.Contract.ValidatorState(&_Stakemanager.CallOpts, arg0)
}

// ValidatorThreshold is a free data retrieval call binding the contract method 0x4fd101d7.
//
// Solidity: function validatorThreshold() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) ValidatorThreshold(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "validatorThreshold")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ValidatorThreshold is a free data retrieval call binding the contract method 0x4fd101d7.
//
// Solidity: function validatorThreshold() view returns(uint256)
func (_Stakemanager *StakemanagerSession) ValidatorThreshold() (*big.Int, error) {
	return _Stakemanager.Contract.ValidatorThreshold(&_Stakemanager.CallOpts)
}

// ValidatorThreshold is a free data retrieval call binding the contract method 0x4fd101d7.
//
// Solidity: function validatorThreshold() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) ValidatorThreshold() (*big.Int, error) {
	return _Stakemanager.Contract.ValidatorThreshold(&_Stakemanager.CallOpts)
}

// Validators is a free data retrieval call binding the contract method 0x35aa2e44.
//
// Solidity: function validators(uint256 ) view returns(uint256 amount, uint256 reward, uint256 activationEpoch, uint256 deactivationEpoch, uint256 jailTime, address signer, address contractAddress, uint8 status)
func (_Stakemanager *StakemanagerCaller) Validators(opts *bind.CallOpts, arg0 *big.Int) (struct {
	Amount            *big.Int
	Reward            *big.Int
	ActivationEpoch   *big.Int
	DeactivationEpoch *big.Int
	JailTime          *big.Int
	Signer            common.Address
	ContractAddress   common.Address
	Status            uint8
}, error,
) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "validators", arg0)

	outstruct := new(struct {
		Amount            *big.Int
		Reward            *big.Int
		ActivationEpoch   *big.Int
		DeactivationEpoch *big.Int
		JailTime          *big.Int
		Signer            common.Address
		ContractAddress   common.Address
		Status            uint8
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Amount = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.Reward = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.ActivationEpoch = *abi.ConvertType(out[2], new(*big.Int)).(**big.Int)
	outstruct.DeactivationEpoch = *abi.ConvertType(out[3], new(*big.Int)).(**big.Int)
	outstruct.JailTime = *abi.ConvertType(out[4], new(*big.Int)).(**big.Int)
	outstruct.Signer = *abi.ConvertType(out[5], new(common.Address)).(*common.Address)
	outstruct.ContractAddress = *abi.ConvertType(out[6], new(common.Address)).(*common.Address)
	outstruct.Status = *abi.ConvertType(out[7], new(uint8)).(*uint8)

	return *outstruct, err
}

// Validators is a free data retrieval call binding the contract method 0x35aa2e44.
//
// Solidity: function validators(uint256 ) view returns(uint256 amount, uint256 reward, uint256 activationEpoch, uint256 deactivationEpoch, uint256 jailTime, address signer, address contractAddress, uint8 status)
func (_Stakemanager *StakemanagerSession) Validators(arg0 *big.Int) (struct {
	Amount            *big.Int
	Reward            *big.Int
	ActivationEpoch   *big.Int
	DeactivationEpoch *big.Int
	JailTime          *big.Int
	Signer            common.Address
	ContractAddress   common.Address
	Status            uint8
}, error,
) {
	return _Stakemanager.Contract.Validators(&_Stakemanager.CallOpts, arg0)
}

// Validators is a free data retrieval call binding the contract method 0x35aa2e44.
//
// Solidity: function validators(uint256 ) view returns(uint256 amount, uint256 reward, uint256 activationEpoch, uint256 deactivationEpoch, uint256 jailTime, address signer, address contractAddress, uint8 status)
func (_Stakemanager *StakemanagerCallerSession) Validators(arg0 *big.Int) (struct {
	Amount            *big.Int
	Reward            *big.Int
	ActivationEpoch   *big.Int
	DeactivationEpoch *big.Int
	JailTime          *big.Int
	Signer            common.Address
	ContractAddress   common.Address
	Status            uint8
}, error,
) {
	return _Stakemanager.Contract.Validators(&_Stakemanager.CallOpts, arg0)
}

// VerifyConsensus is a free data retrieval call binding the contract method 0xbbcfbbb0.
//
// Solidity: function verifyConsensus(bytes32 voteHash, bytes sigs) view returns(uint256, uint256)
func (_Stakemanager *StakemanagerCaller) VerifyConsensus(opts *bind.CallOpts, voteHash [32]byte, sigs []byte) (*big.Int, *big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "verifyConsensus", voteHash, sigs)
	if err != nil {
		return *new(*big.Int), *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	out1 := *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return out0, out1, err
}

// VerifyConsensus is a free data retrieval call binding the contract method 0xbbcfbbb0.
//
// Solidity: function verifyConsensus(bytes32 voteHash, bytes sigs) view returns(uint256, uint256)
func (_Stakemanager *StakemanagerSession) VerifyConsensus(voteHash [32]byte, sigs []byte) (*big.Int, *big.Int, error) {
	return _Stakemanager.Contract.VerifyConsensus(&_Stakemanager.CallOpts, voteHash, sigs)
}

// VerifyConsensus is a free data retrieval call binding the contract method 0xbbcfbbb0.
//
// Solidity: function verifyConsensus(bytes32 voteHash, bytes sigs) view returns(uint256, uint256)
func (_Stakemanager *StakemanagerCallerSession) VerifyConsensus(voteHash [32]byte, sigs []byte) (*big.Int, *big.Int, error) {
	return _Stakemanager.Contract.VerifyConsensus(&_Stakemanager.CallOpts, voteHash, sigs)
}

// WithdrawalDelay is a free data retrieval call binding the contract method 0xa7ab6961.
//
// Solidity: function withdrawalDelay() view returns(uint256)
func (_Stakemanager *StakemanagerCaller) WithdrawalDelay(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Stakemanager.contract.Call(opts, &out, "withdrawalDelay")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// WithdrawalDelay is a free data retrieval call binding the contract method 0xa7ab6961.
//
// Solidity: function withdrawalDelay() view returns(uint256)
func (_Stakemanager *StakemanagerSession) WithdrawalDelay() (*big.Int, error) {
	return _Stakemanager.Contract.WithdrawalDelay(&_Stakemanager.CallOpts)
}

// WithdrawalDelay is a free data retrieval call binding the contract method 0xa7ab6961.
//
// Solidity: function withdrawalDelay() view returns(uint256)
func (_Stakemanager *StakemanagerCallerSession) WithdrawalDelay() (*big.Int, error) {
	return _Stakemanager.Contract.WithdrawalDelay(&_Stakemanager.CallOpts)
}

// ChangeRootChain is a paid mutator transaction binding the contract method 0xe8afa8e8.
//
// Solidity: function changeRootChain(address newRootChain) returns()
func (_Stakemanager *StakemanagerTransactor) ChangeRootChain(opts *bind.TransactOpts, newRootChain common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "changeRootChain", newRootChain)
}

// ChangeRootChain is a paid mutator transaction binding the contract method 0xe8afa8e8.
//
// Solidity: function changeRootChain(address newRootChain) returns()
func (_Stakemanager *StakemanagerSession) ChangeRootChain(newRootChain common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.ChangeRootChain(&_Stakemanager.TransactOpts, newRootChain)
}

// ChangeRootChain is a paid mutator transaction binding the contract method 0xe8afa8e8.
//
// Solidity: function changeRootChain(address newRootChain) returns()
func (_Stakemanager *StakemanagerTransactorSession) ChangeRootChain(newRootChain common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.ChangeRootChain(&_Stakemanager.TransactOpts, newRootChain)
}

// CheckSignatures is a paid mutator transaction binding the contract method 0x066647a0.
//
// Solidity: function checkSignatures(uint256 blockInterval, bytes32 voteHash, bytes32 stateRoot, address proposer, bytes sigs) returns(uint256)
func (_Stakemanager *StakemanagerTransactor) CheckSignatures(opts *bind.TransactOpts, blockInterval *big.Int, voteHash [32]byte, stateRoot [32]byte, proposer common.Address, sigs []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "checkSignatures", blockInterval, voteHash, stateRoot, proposer, sigs)
}

// CheckSignatures is a paid mutator transaction binding the contract method 0x066647a0.
//
// Solidity: function checkSignatures(uint256 blockInterval, bytes32 voteHash, bytes32 stateRoot, address proposer, bytes sigs) returns(uint256)
func (_Stakemanager *StakemanagerSession) CheckSignatures(blockInterval *big.Int, voteHash [32]byte, stateRoot [32]byte, proposer common.Address, sigs []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.CheckSignatures(&_Stakemanager.TransactOpts, blockInterval, voteHash, stateRoot, proposer, sigs)
}

// CheckSignatures is a paid mutator transaction binding the contract method 0x066647a0.
//
// Solidity: function checkSignatures(uint256 blockInterval, bytes32 voteHash, bytes32 stateRoot, address proposer, bytes sigs) returns(uint256)
func (_Stakemanager *StakemanagerTransactorSession) CheckSignatures(blockInterval *big.Int, voteHash [32]byte, stateRoot [32]byte, proposer common.Address, sigs []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.CheckSignatures(&_Stakemanager.TransactOpts, blockInterval, voteHash, stateRoot, proposer, sigs)
}

// ClaimFee is a paid mutator transaction binding the contract method 0x68cb812a.
//
// Solidity: function claimFee(uint256 accumFeeAmount, uint256 index, bytes proof) returns()
func (_Stakemanager *StakemanagerTransactor) ClaimFee(opts *bind.TransactOpts, accumFeeAmount *big.Int, index *big.Int, proof []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "claimFee", accumFeeAmount, index, proof)
}

// ClaimFee is a paid mutator transaction binding the contract method 0x68cb812a.
//
// Solidity: function claimFee(uint256 accumFeeAmount, uint256 index, bytes proof) returns()
func (_Stakemanager *StakemanagerSession) ClaimFee(accumFeeAmount *big.Int, index *big.Int, proof []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.ClaimFee(&_Stakemanager.TransactOpts, accumFeeAmount, index, proof)
}

// ClaimFee is a paid mutator transaction binding the contract method 0x68cb812a.
//
// Solidity: function claimFee(uint256 accumFeeAmount, uint256 index, bytes proof) returns()
func (_Stakemanager *StakemanagerTransactorSession) ClaimFee(accumFeeAmount *big.Int, index *big.Int, proof []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.ClaimFee(&_Stakemanager.TransactOpts, accumFeeAmount, index, proof)
}

// ConfirmAuctionBid is a paid mutator transaction binding the contract method 0xc8b194a2.
//
// Solidity: function confirmAuctionBid(uint256 validatorId, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactor) ConfirmAuctionBid(opts *bind.TransactOpts, validatorId *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "confirmAuctionBid", validatorId, heimdallFee, acceptDelegation, signerPubkey)
}

// ConfirmAuctionBid is a paid mutator transaction binding the contract method 0xc8b194a2.
//
// Solidity: function confirmAuctionBid(uint256 validatorId, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerSession) ConfirmAuctionBid(validatorId *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.ConfirmAuctionBid(&_Stakemanager.TransactOpts, validatorId, heimdallFee, acceptDelegation, signerPubkey)
}

// ConfirmAuctionBid is a paid mutator transaction binding the contract method 0xc8b194a2.
//
// Solidity: function confirmAuctionBid(uint256 validatorId, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactorSession) ConfirmAuctionBid(validatorId *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.ConfirmAuctionBid(&_Stakemanager.TransactOpts, validatorId, heimdallFee, acceptDelegation, signerPubkey)
}

// DelegationDeposit is a paid mutator transaction binding the contract method 0x6901b253.
//
// Solidity: function delegationDeposit(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerTransactor) DelegationDeposit(opts *bind.TransactOpts, validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "delegationDeposit", validatorId, amount, delegator)
}

// DelegationDeposit is a paid mutator transaction binding the contract method 0x6901b253.
//
// Solidity: function delegationDeposit(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerSession) DelegationDeposit(validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.DelegationDeposit(&_Stakemanager.TransactOpts, validatorId, amount, delegator)
}

// DelegationDeposit is a paid mutator transaction binding the contract method 0x6901b253.
//
// Solidity: function delegationDeposit(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerTransactorSession) DelegationDeposit(validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.DelegationDeposit(&_Stakemanager.TransactOpts, validatorId, amount, delegator)
}

// ForceUnstake is a paid mutator transaction binding the contract method 0x91460149.
//
// Solidity: function forceUnstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactor) ForceUnstake(opts *bind.TransactOpts, validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "forceUnstake", validatorId)
}

// ForceUnstake is a paid mutator transaction binding the contract method 0x91460149.
//
// Solidity: function forceUnstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerSession) ForceUnstake(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.ForceUnstake(&_Stakemanager.TransactOpts, validatorId)
}

// ForceUnstake is a paid mutator transaction binding the contract method 0x91460149.
//
// Solidity: function forceUnstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactorSession) ForceUnstake(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.ForceUnstake(&_Stakemanager.TransactOpts, validatorId)
}

// Lock is a paid mutator transaction binding the contract method 0xf83d08ba.
//
// Solidity: function lock() returns()
func (_Stakemanager *StakemanagerTransactor) Lock(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "lock")
}

// Lock is a paid mutator transaction binding the contract method 0xf83d08ba.
//
// Solidity: function lock() returns()
func (_Stakemanager *StakemanagerSession) Lock() (*types.Transaction, error) {
	return _Stakemanager.Contract.Lock(&_Stakemanager.TransactOpts)
}

// Lock is a paid mutator transaction binding the contract method 0xf83d08ba.
//
// Solidity: function lock() returns()
func (_Stakemanager *StakemanagerTransactorSession) Lock() (*types.Transaction, error) {
	return _Stakemanager.Contract.Lock(&_Stakemanager.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Stakemanager *StakemanagerTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Stakemanager *StakemanagerSession) RenounceOwnership() (*types.Transaction, error) {
	return _Stakemanager.Contract.RenounceOwnership(&_Stakemanager.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Stakemanager *StakemanagerTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _Stakemanager.Contract.RenounceOwnership(&_Stakemanager.TransactOpts)
}

// Restake is a paid mutator transaction binding the contract method 0x28cc4e41.
//
// Solidity: function restake(uint256 validatorId, uint256 amount, bool stakeRewards) returns()
func (_Stakemanager *StakemanagerTransactor) Restake(opts *bind.TransactOpts, validatorId *big.Int, amount *big.Int, stakeRewards bool) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "restake", validatorId, amount, stakeRewards)
}

// Restake is a paid mutator transaction binding the contract method 0x28cc4e41.
//
// Solidity: function restake(uint256 validatorId, uint256 amount, bool stakeRewards) returns()
func (_Stakemanager *StakemanagerSession) Restake(validatorId *big.Int, amount *big.Int, stakeRewards bool) (*types.Transaction, error) {
	return _Stakemanager.Contract.Restake(&_Stakemanager.TransactOpts, validatorId, amount, stakeRewards)
}

// Restake is a paid mutator transaction binding the contract method 0x28cc4e41.
//
// Solidity: function restake(uint256 validatorId, uint256 amount, bool stakeRewards) returns()
func (_Stakemanager *StakemanagerTransactorSession) Restake(validatorId *big.Int, amount *big.Int, stakeRewards bool) (*types.Transaction, error) {
	return _Stakemanager.Contract.Restake(&_Stakemanager.TransactOpts, validatorId, amount, stakeRewards)
}

// SetDelegationEnabled is a paid mutator transaction binding the contract method 0xf28699fa.
//
// Solidity: function setDelegationEnabled(bool enabled) returns()
func (_Stakemanager *StakemanagerTransactor) SetDelegationEnabled(opts *bind.TransactOpts, enabled bool) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "setDelegationEnabled", enabled)
}

// SetDelegationEnabled is a paid mutator transaction binding the contract method 0xf28699fa.
//
// Solidity: function setDelegationEnabled(bool enabled) returns()
func (_Stakemanager *StakemanagerSession) SetDelegationEnabled(enabled bool) (*types.Transaction, error) {
	return _Stakemanager.Contract.SetDelegationEnabled(&_Stakemanager.TransactOpts, enabled)
}

// SetDelegationEnabled is a paid mutator transaction binding the contract method 0xf28699fa.
//
// Solidity: function setDelegationEnabled(bool enabled) returns()
func (_Stakemanager *StakemanagerTransactorSession) SetDelegationEnabled(enabled bool) (*types.Transaction, error) {
	return _Stakemanager.Contract.SetDelegationEnabled(&_Stakemanager.TransactOpts, enabled)
}

// SetToken is a paid mutator transaction binding the contract method 0x144fa6d7.
//
// Solidity: function setToken(address _token) returns()
func (_Stakemanager *StakemanagerTransactor) SetToken(opts *bind.TransactOpts, _token common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "setToken", _token)
}

// SetToken is a paid mutator transaction binding the contract method 0x144fa6d7.
//
// Solidity: function setToken(address _token) returns()
func (_Stakemanager *StakemanagerSession) SetToken(_token common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.SetToken(&_Stakemanager.TransactOpts, _token)
}

// SetToken is a paid mutator transaction binding the contract method 0x144fa6d7.
//
// Solidity: function setToken(address _token) returns()
func (_Stakemanager *StakemanagerTransactorSession) SetToken(_token common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.SetToken(&_Stakemanager.TransactOpts, _token)
}

// Slash is a paid mutator transaction binding the contract method 0x5e47655f.
//
// Solidity: function slash(bytes _slashingInfoList) returns(uint256)
func (_Stakemanager *StakemanagerTransactor) Slash(opts *bind.TransactOpts, _slashingInfoList []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "slash", _slashingInfoList)
}

// Slash is a paid mutator transaction binding the contract method 0x5e47655f.
//
// Solidity: function slash(bytes _slashingInfoList) returns(uint256)
func (_Stakemanager *StakemanagerSession) Slash(_slashingInfoList []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.Slash(&_Stakemanager.TransactOpts, _slashingInfoList)
}

// Slash is a paid mutator transaction binding the contract method 0x5e47655f.
//
// Solidity: function slash(bytes _slashingInfoList) returns(uint256)
func (_Stakemanager *StakemanagerTransactorSession) Slash(_slashingInfoList []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.Slash(&_Stakemanager.TransactOpts, _slashingInfoList)
}

// Stake is a paid mutator transaction binding the contract method 0x028c4c67.
//
// Solidity: function stake(uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactor) Stake(opts *bind.TransactOpts, amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "stake", amount, heimdallFee, acceptDelegation, signerPubkey)
}

// Stake is a paid mutator transaction binding the contract method 0x028c4c67.
//
// Solidity: function stake(uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerSession) Stake(amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.Stake(&_Stakemanager.TransactOpts, amount, heimdallFee, acceptDelegation, signerPubkey)
}

// Stake is a paid mutator transaction binding the contract method 0x028c4c67.
//
// Solidity: function stake(uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactorSession) Stake(amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.Stake(&_Stakemanager.TransactOpts, amount, heimdallFee, acceptDelegation, signerPubkey)
}

// StakeFor is a paid mutator transaction binding the contract method 0x4fdd20f1.
//
// Solidity: function stakeFor(address user, uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactor) StakeFor(opts *bind.TransactOpts, user common.Address, amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "stakeFor", user, amount, heimdallFee, acceptDelegation, signerPubkey)
}

// StakeFor is a paid mutator transaction binding the contract method 0x4fdd20f1.
//
// Solidity: function stakeFor(address user, uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerSession) StakeFor(user common.Address, amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.StakeFor(&_Stakemanager.TransactOpts, user, amount, heimdallFee, acceptDelegation, signerPubkey)
}

// StakeFor is a paid mutator transaction binding the contract method 0x4fdd20f1.
//
// Solidity: function stakeFor(address user, uint256 amount, uint256 heimdallFee, bool acceptDelegation, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactorSession) StakeFor(user common.Address, amount *big.Int, heimdallFee *big.Int, acceptDelegation bool, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.StakeFor(&_Stakemanager.TransactOpts, user, amount, heimdallFee, acceptDelegation, signerPubkey)
}

// StartAuction is a paid mutator transaction binding the contract method 0x4fee13fc.
//
// Solidity: function startAuction(uint256 validatorId, uint256 amount) returns()
func (_Stakemanager *StakemanagerTransactor) StartAuction(opts *bind.TransactOpts, validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "startAuction", validatorId, amount)
}

// StartAuction is a paid mutator transaction binding the contract method 0x4fee13fc.
//
// Solidity: function startAuction(uint256 validatorId, uint256 amount) returns()
func (_Stakemanager *StakemanagerSession) StartAuction(validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.StartAuction(&_Stakemanager.TransactOpts, validatorId, amount)
}

// StartAuction is a paid mutator transaction binding the contract method 0x4fee13fc.
//
// Solidity: function startAuction(uint256 validatorId, uint256 amount) returns()
func (_Stakemanager *StakemanagerTransactorSession) StartAuction(validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.StartAuction(&_Stakemanager.TransactOpts, validatorId, amount)
}

// StopAuctions is a paid mutator transaction binding the contract method 0xf771fc87.
//
// Solidity: function stopAuctions(uint256 forNCheckpoints) returns()
func (_Stakemanager *StakemanagerTransactor) StopAuctions(opts *bind.TransactOpts, forNCheckpoints *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "stopAuctions", forNCheckpoints)
}

// StopAuctions is a paid mutator transaction binding the contract method 0xf771fc87.
//
// Solidity: function stopAuctions(uint256 forNCheckpoints) returns()
func (_Stakemanager *StakemanagerSession) StopAuctions(forNCheckpoints *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.StopAuctions(&_Stakemanager.TransactOpts, forNCheckpoints)
}

// StopAuctions is a paid mutator transaction binding the contract method 0xf771fc87.
//
// Solidity: function stopAuctions(uint256 forNCheckpoints) returns()
func (_Stakemanager *StakemanagerTransactorSession) StopAuctions(forNCheckpoints *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.StopAuctions(&_Stakemanager.TransactOpts, forNCheckpoints)
}

// TopUpForFee is a paid mutator transaction binding the contract method 0x63656798.
//
// Solidity: function topUpForFee(address user, uint256 heimdallFee) returns()
func (_Stakemanager *StakemanagerTransactor) TopUpForFee(opts *bind.TransactOpts, user common.Address, heimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "topUpForFee", user, heimdallFee)
}

// TopUpForFee is a paid mutator transaction binding the contract method 0x63656798.
//
// Solidity: function topUpForFee(address user, uint256 heimdallFee) returns()
func (_Stakemanager *StakemanagerSession) TopUpForFee(user common.Address, heimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.TopUpForFee(&_Stakemanager.TransactOpts, user, heimdallFee)
}

// TopUpForFee is a paid mutator transaction binding the contract method 0x63656798.
//
// Solidity: function topUpForFee(address user, uint256 heimdallFee) returns()
func (_Stakemanager *StakemanagerTransactorSession) TopUpForFee(user common.Address, heimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.TopUpForFee(&_Stakemanager.TransactOpts, user, heimdallFee)
}

// TransferFunds is a paid mutator transaction binding the contract method 0xbc8756a9.
//
// Solidity: function transferFunds(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerTransactor) TransferFunds(opts *bind.TransactOpts, validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "transferFunds", validatorId, amount, delegator)
}

// TransferFunds is a paid mutator transaction binding the contract method 0xbc8756a9.
//
// Solidity: function transferFunds(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerSession) TransferFunds(validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.TransferFunds(&_Stakemanager.TransactOpts, validatorId, amount, delegator)
}

// TransferFunds is a paid mutator transaction binding the contract method 0xbc8756a9.
//
// Solidity: function transferFunds(uint256 validatorId, uint256 amount, address delegator) returns(bool)
func (_Stakemanager *StakemanagerTransactorSession) TransferFunds(validatorId *big.Int, amount *big.Int, delegator common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.TransferFunds(&_Stakemanager.TransactOpts, validatorId, amount, delegator)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Stakemanager *StakemanagerTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Stakemanager *StakemanagerSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.TransferOwnership(&_Stakemanager.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Stakemanager *StakemanagerTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.TransferOwnership(&_Stakemanager.TransactOpts, newOwner)
}

// UnJail is a paid mutator transaction binding the contract method 0x3d02455b.
//
// Solidity: function unJail(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactor) UnJail(opts *bind.TransactOpts, validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "unJail", validatorId)
}

// UnJail is a paid mutator transaction binding the contract method 0x3d02455b.
//
// Solidity: function unJail(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerSession) UnJail(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UnJail(&_Stakemanager.TransactOpts, validatorId)
}

// UnJail is a paid mutator transaction binding the contract method 0x3d02455b.
//
// Solidity: function unJail(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactorSession) UnJail(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UnJail(&_Stakemanager.TransactOpts, validatorId)
}

// Unlock is a paid mutator transaction binding the contract method 0xa69df4b5.
//
// Solidity: function unlock() returns()
func (_Stakemanager *StakemanagerTransactor) Unlock(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "unlock")
}

// Unlock is a paid mutator transaction binding the contract method 0xa69df4b5.
//
// Solidity: function unlock() returns()
func (_Stakemanager *StakemanagerSession) Unlock() (*types.Transaction, error) {
	return _Stakemanager.Contract.Unlock(&_Stakemanager.TransactOpts)
}

// Unlock is a paid mutator transaction binding the contract method 0xa69df4b5.
//
// Solidity: function unlock() returns()
func (_Stakemanager *StakemanagerTransactorSession) Unlock() (*types.Transaction, error) {
	return _Stakemanager.Contract.Unlock(&_Stakemanager.TransactOpts)
}

// Unstake is a paid mutator transaction binding the contract method 0x2e17de78.
//
// Solidity: function unstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactor) Unstake(opts *bind.TransactOpts, validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "unstake", validatorId)
}

// Unstake is a paid mutator transaction binding the contract method 0x2e17de78.
//
// Solidity: function unstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerSession) Unstake(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.Unstake(&_Stakemanager.TransactOpts, validatorId)
}

// Unstake is a paid mutator transaction binding the contract method 0x2e17de78.
//
// Solidity: function unstake(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactorSession) Unstake(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.Unstake(&_Stakemanager.TransactOpts, validatorId)
}

// UnstakeClaim is a paid mutator transaction binding the contract method 0xd86d53e7.
//
// Solidity: function unstakeClaim(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactor) UnstakeClaim(opts *bind.TransactOpts, validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "unstakeClaim", validatorId)
}

// UnstakeClaim is a paid mutator transaction binding the contract method 0xd86d53e7.
//
// Solidity: function unstakeClaim(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerSession) UnstakeClaim(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UnstakeClaim(&_Stakemanager.TransactOpts, validatorId)
}

// UnstakeClaim is a paid mutator transaction binding the contract method 0xd86d53e7.
//
// Solidity: function unstakeClaim(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactorSession) UnstakeClaim(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UnstakeClaim(&_Stakemanager.TransactOpts, validatorId)
}

// UpdateCheckPointBlockInterval is a paid mutator transaction binding the contract method 0xa440ab1e.
//
// Solidity: function updateCheckPointBlockInterval(uint256 _blocks) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateCheckPointBlockInterval(opts *bind.TransactOpts, _blocks *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateCheckPointBlockInterval", _blocks)
}

// UpdateCheckPointBlockInterval is a paid mutator transaction binding the contract method 0xa440ab1e.
//
// Solidity: function updateCheckPointBlockInterval(uint256 _blocks) returns()
func (_Stakemanager *StakemanagerSession) UpdateCheckPointBlockInterval(_blocks *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateCheckPointBlockInterval(&_Stakemanager.TransactOpts, _blocks)
}

// UpdateCheckPointBlockInterval is a paid mutator transaction binding the contract method 0xa440ab1e.
//
// Solidity: function updateCheckPointBlockInterval(uint256 _blocks) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateCheckPointBlockInterval(_blocks *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateCheckPointBlockInterval(&_Stakemanager.TransactOpts, _blocks)
}

// UpdateCheckpointReward is a paid mutator transaction binding the contract method 0xcbf383d5.
//
// Solidity: function updateCheckpointReward(uint256 newReward) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateCheckpointReward(opts *bind.TransactOpts, newReward *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateCheckpointReward", newReward)
}

// UpdateCheckpointReward is a paid mutator transaction binding the contract method 0xcbf383d5.
//
// Solidity: function updateCheckpointReward(uint256 newReward) returns()
func (_Stakemanager *StakemanagerSession) UpdateCheckpointReward(newReward *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateCheckpointReward(&_Stakemanager.TransactOpts, newReward)
}

// UpdateCheckpointReward is a paid mutator transaction binding the contract method 0xcbf383d5.
//
// Solidity: function updateCheckpointReward(uint256 newReward) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateCheckpointReward(newReward *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateCheckpointReward(&_Stakemanager.TransactOpts, newReward)
}

// UpdateConstructor is a paid mutator transaction binding the contract method 0x118bdec9.
//
// Solidity: function updateConstructor(address _registry, address _rootchain, address _NFTContract, address _stakingLogger, address _ValidatorShareFactory) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateConstructor(opts *bind.TransactOpts, _registry common.Address, _rootchain common.Address, _NFTContract common.Address, _stakingLogger common.Address, _ValidatorShareFactory common.Address) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateConstructor", _registry, _rootchain, _NFTContract, _stakingLogger, _ValidatorShareFactory)
}

// UpdateConstructor is a paid mutator transaction binding the contract method 0x118bdec9.
//
// Solidity: function updateConstructor(address _registry, address _rootchain, address _NFTContract, address _stakingLogger, address _ValidatorShareFactory) returns()
func (_Stakemanager *StakemanagerSession) UpdateConstructor(_registry common.Address, _rootchain common.Address, _NFTContract common.Address, _stakingLogger common.Address, _ValidatorShareFactory common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateConstructor(&_Stakemanager.TransactOpts, _registry, _rootchain, _NFTContract, _stakingLogger, _ValidatorShareFactory)
}

// UpdateConstructor is a paid mutator transaction binding the contract method 0x118bdec9.
//
// Solidity: function updateConstructor(address _registry, address _rootchain, address _NFTContract, address _stakingLogger, address _ValidatorShareFactory) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateConstructor(_registry common.Address, _rootchain common.Address, _NFTContract common.Address, _stakingLogger common.Address, _ValidatorShareFactory common.Address) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateConstructor(&_Stakemanager.TransactOpts, _registry, _rootchain, _NFTContract, _stakingLogger, _ValidatorShareFactory)
}

// UpdateDynastyValue is a paid mutator transaction binding the contract method 0xe6692f49.
//
// Solidity: function updateDynastyValue(uint256 newDynasty) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateDynastyValue(opts *bind.TransactOpts, newDynasty *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateDynastyValue", newDynasty)
}

// UpdateDynastyValue is a paid mutator transaction binding the contract method 0xe6692f49.
//
// Solidity: function updateDynastyValue(uint256 newDynasty) returns()
func (_Stakemanager *StakemanagerSession) UpdateDynastyValue(newDynasty *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateDynastyValue(&_Stakemanager.TransactOpts, newDynasty)
}

// UpdateDynastyValue is a paid mutator transaction binding the contract method 0xe6692f49.
//
// Solidity: function updateDynastyValue(uint256 newDynasty) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateDynastyValue(newDynasty *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateDynastyValue(&_Stakemanager.TransactOpts, newDynasty)
}

// UpdateMinAmounts is a paid mutator transaction binding the contract method 0xb1d23f02.
//
// Solidity: function updateMinAmounts(uint256 _minDeposit, uint256 _minHeimdallFee) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateMinAmounts(opts *bind.TransactOpts, _minDeposit *big.Int, _minHeimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateMinAmounts", _minDeposit, _minHeimdallFee)
}

// UpdateMinAmounts is a paid mutator transaction binding the contract method 0xb1d23f02.
//
// Solidity: function updateMinAmounts(uint256 _minDeposit, uint256 _minHeimdallFee) returns()
func (_Stakemanager *StakemanagerSession) UpdateMinAmounts(_minDeposit *big.Int, _minHeimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateMinAmounts(&_Stakemanager.TransactOpts, _minDeposit, _minHeimdallFee)
}

// UpdateMinAmounts is a paid mutator transaction binding the contract method 0xb1d23f02.
//
// Solidity: function updateMinAmounts(uint256 _minDeposit, uint256 _minHeimdallFee) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateMinAmounts(_minDeposit *big.Int, _minHeimdallFee *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateMinAmounts(&_Stakemanager.TransactOpts, _minDeposit, _minHeimdallFee)
}

// UpdateProposerBonus is a paid mutator transaction binding the contract method 0x9b33f434.
//
// Solidity: function updateProposerBonus(uint256 newProposerBonus) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateProposerBonus(opts *bind.TransactOpts, newProposerBonus *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateProposerBonus", newProposerBonus)
}

// UpdateProposerBonus is a paid mutator transaction binding the contract method 0x9b33f434.
//
// Solidity: function updateProposerBonus(uint256 newProposerBonus) returns()
func (_Stakemanager *StakemanagerSession) UpdateProposerBonus(newProposerBonus *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateProposerBonus(&_Stakemanager.TransactOpts, newProposerBonus)
}

// UpdateProposerBonus is a paid mutator transaction binding the contract method 0x9b33f434.
//
// Solidity: function updateProposerBonus(uint256 newProposerBonus) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateProposerBonus(newProposerBonus *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateProposerBonus(&_Stakemanager.TransactOpts, newProposerBonus)
}

// UpdateSigner is a paid mutator transaction binding the contract method 0xf41a9642.
//
// Solidity: function updateSigner(uint256 validatorId, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateSigner(opts *bind.TransactOpts, validatorId *big.Int, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateSigner", validatorId, signerPubkey)
}

// UpdateSigner is a paid mutator transaction binding the contract method 0xf41a9642.
//
// Solidity: function updateSigner(uint256 validatorId, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerSession) UpdateSigner(validatorId *big.Int, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateSigner(&_Stakemanager.TransactOpts, validatorId, signerPubkey)
}

// UpdateSigner is a paid mutator transaction binding the contract method 0xf41a9642.
//
// Solidity: function updateSigner(uint256 validatorId, bytes signerPubkey) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateSigner(validatorId *big.Int, signerPubkey []byte) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateSigner(&_Stakemanager.TransactOpts, validatorId, signerPubkey)
}

// UpdateSignerUpdateLimit is a paid mutator transaction binding the contract method 0x06cfb104.
//
// Solidity: function updateSignerUpdateLimit(uint256 _limit) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateSignerUpdateLimit(opts *bind.TransactOpts, _limit *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateSignerUpdateLimit", _limit)
}

// UpdateSignerUpdateLimit is a paid mutator transaction binding the contract method 0x06cfb104.
//
// Solidity: function updateSignerUpdateLimit(uint256 _limit) returns()
func (_Stakemanager *StakemanagerSession) UpdateSignerUpdateLimit(_limit *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateSignerUpdateLimit(&_Stakemanager.TransactOpts, _limit)
}

// UpdateSignerUpdateLimit is a paid mutator transaction binding the contract method 0x06cfb104.
//
// Solidity: function updateSignerUpdateLimit(uint256 _limit) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateSignerUpdateLimit(_limit *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateSignerUpdateLimit(&_Stakemanager.TransactOpts, _limit)
}

// UpdateValidatorState is a paid mutator transaction binding the contract method 0x9ff11500.
//
// Solidity: function updateValidatorState(uint256 validatorId, int256 amount) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateValidatorState(opts *bind.TransactOpts, validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateValidatorState", validatorId, amount)
}

// UpdateValidatorState is a paid mutator transaction binding the contract method 0x9ff11500.
//
// Solidity: function updateValidatorState(uint256 validatorId, int256 amount) returns()
func (_Stakemanager *StakemanagerSession) UpdateValidatorState(validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateValidatorState(&_Stakemanager.TransactOpts, validatorId, amount)
}

// UpdateValidatorState is a paid mutator transaction binding the contract method 0x9ff11500.
//
// Solidity: function updateValidatorState(uint256 validatorId, int256 amount) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateValidatorState(validatorId *big.Int, amount *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateValidatorState(&_Stakemanager.TransactOpts, validatorId, amount)
}

// UpdateValidatorThreshold is a paid mutator transaction binding the contract method 0x16827b1b.
//
// Solidity: function updateValidatorThreshold(uint256 newThreshold) returns()
func (_Stakemanager *StakemanagerTransactor) UpdateValidatorThreshold(opts *bind.TransactOpts, newThreshold *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "updateValidatorThreshold", newThreshold)
}

// UpdateValidatorThreshold is a paid mutator transaction binding the contract method 0x16827b1b.
//
// Solidity: function updateValidatorThreshold(uint256 newThreshold) returns()
func (_Stakemanager *StakemanagerSession) UpdateValidatorThreshold(newThreshold *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateValidatorThreshold(&_Stakemanager.TransactOpts, newThreshold)
}

// UpdateValidatorThreshold is a paid mutator transaction binding the contract method 0x16827b1b.
//
// Solidity: function updateValidatorThreshold(uint256 newThreshold) returns()
func (_Stakemanager *StakemanagerTransactorSession) UpdateValidatorThreshold(newThreshold *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.UpdateValidatorThreshold(&_Stakemanager.TransactOpts, newThreshold)
}

// WithdrawRewards is a paid mutator transaction binding the contract method 0x9342c8f4.
//
// Solidity: function withdrawRewards(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactor) WithdrawRewards(opts *bind.TransactOpts, validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.contract.Transact(opts, "withdrawRewards", validatorId)
}

// WithdrawRewards is a paid mutator transaction binding the contract method 0x9342c8f4.
//
// Solidity: function withdrawRewards(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerSession) WithdrawRewards(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.WithdrawRewards(&_Stakemanager.TransactOpts, validatorId)
}

// WithdrawRewards is a paid mutator transaction binding the contract method 0x9342c8f4.
//
// Solidity: function withdrawRewards(uint256 validatorId) returns()
func (_Stakemanager *StakemanagerTransactorSession) WithdrawRewards(validatorId *big.Int) (*types.Transaction, error) {
	return _Stakemanager.Contract.WithdrawRewards(&_Stakemanager.TransactOpts, validatorId)
}

// StakemanagerOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the Stakemanager contract.
type StakemanagerOwnershipTransferredIterator struct {
	Event *StakemanagerOwnershipTransferred // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *StakemanagerOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(StakemanagerOwnershipTransferred)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(StakemanagerOwnershipTransferred)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *StakemanagerOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *StakemanagerOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// StakemanagerOwnershipTransferred represents a OwnershipTransferred event raised by the Stakemanager contract.
type StakemanagerOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Stakemanager *StakemanagerFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*StakemanagerOwnershipTransferredIterator, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Stakemanager.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &StakemanagerOwnershipTransferredIterator{contract: _Stakemanager.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Stakemanager *StakemanagerFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *StakemanagerOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Stakemanager.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(StakemanagerOwnershipTransferred)
				if err := _Stakemanager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseOwnershipTransferred is a log parse operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Stakemanager *StakemanagerFilterer) ParseOwnershipTransferred(log types.Log) (*StakemanagerOwnershipTransferred, error) {
	event := new(StakemanagerOwnershipTransferred)
	if err := _Stakemanager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// StakemanagerRootChainChangedIterator is returned from FilterRootChainChanged and is used to iterate over the raw logs and unpacked data for RootChainChanged events raised by the Stakemanager contract.
type StakemanagerRootChainChangedIterator struct {
	Event *StakemanagerRootChainChanged // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *StakemanagerRootChainChangedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(StakemanagerRootChainChanged)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(StakemanagerRootChainChanged)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *StakemanagerRootChainChangedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *StakemanagerRootChainChangedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// StakemanagerRootChainChanged represents a RootChainChanged event raised by the Stakemanager contract.
type StakemanagerRootChainChanged struct {
	PreviousRootChain common.Address
	NewRootChain      common.Address
	Raw               types.Log // Blockchain specific contextual infos
}

// FilterRootChainChanged is a free log retrieval operation binding the contract event 0x211c9015fc81c0dbd45bd99f0f29fc1c143bfd53442d5ffd722bbbef7a887fe9.
//
// Solidity: event RootChainChanged(address indexed previousRootChain, address indexed newRootChain)
func (_Stakemanager *StakemanagerFilterer) FilterRootChainChanged(opts *bind.FilterOpts, previousRootChain []common.Address, newRootChain []common.Address) (*StakemanagerRootChainChangedIterator, error) {
	var previousRootChainRule []interface{}
	for _, previousRootChainItem := range previousRootChain {
		previousRootChainRule = append(previousRootChainRule, previousRootChainItem)
	}
	var newRootChainRule []interface{}
	for _, newRootChainItem := range newRootChain {
		newRootChainRule = append(newRootChainRule, newRootChainItem)
	}

	logs, sub, err := _Stakemanager.contract.FilterLogs(opts, "RootChainChanged", previousRootChainRule, newRootChainRule)
	if err != nil {
		return nil, err
	}
	return &StakemanagerRootChainChangedIterator{contract: _Stakemanager.contract, event: "RootChainChanged", logs: logs, sub: sub}, nil
}

// WatchRootChainChanged is a free log subscription operation binding the contract event 0x211c9015fc81c0dbd45bd99f0f29fc1c143bfd53442d5ffd722bbbef7a887fe9.
//
// Solidity: event RootChainChanged(address indexed previousRootChain, address indexed newRootChain)
func (_Stakemanager *StakemanagerFilterer) WatchRootChainChanged(opts *bind.WatchOpts, sink chan<- *StakemanagerRootChainChanged, previousRootChain []common.Address, newRootChain []common.Address) (event.Subscription, error) {
	var previousRootChainRule []interface{}
	for _, previousRootChainItem := range previousRootChain {
		previousRootChainRule = append(previousRootChainRule, previousRootChainItem)
	}
	var newRootChainRule []interface{}
	for _, newRootChainItem := range newRootChain {
		newRootChainRule = append(newRootChainRule, newRootChainItem)
	}

	logs, sub, err := _Stakemanager.contract.WatchLogs(opts, "RootChainChanged", previousRootChainRule, newRootChainRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(StakemanagerRootChainChanged)
				if err := _Stakemanager.contract.UnpackLog(event, "RootChainChanged", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseRootChainChanged is a log parse operation binding the contract event 0x211c9015fc81c0dbd45bd99f0f29fc1c143bfd53442d5ffd722bbbef7a887fe9.
//
// Solidity: event RootChainChanged(address indexed previousRootChain, address indexed newRootChain)
func (_Stakemanager *StakemanagerFilterer) ParseRootChainChanged(log types.Log) (*StakemanagerRootChainChanged, error) {
	event := new(StakemanagerRootChainChanged)
	if err := _Stakemanager.contract.UnpackLog(event, "RootChainChanged", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
