package contract

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	borabi "github.com/ethereum/go-ethereum/consensus/bor/abi"
	"github.com/ethereum/go-ethereum/consensus/bor/staking"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

const (
	lastStatusIDMethod             = "lastStatusId"
	applyValidatorStatusMethod     = "applyValidatorStatus"
	logStakeUpdatesMethod          = "logStakeUpdates"
	lastReportIDMethod             = "lastReportId"
	applyParticipationReportMethod = "applyParticipationReport"
)

var errLastIDOverflow = errors.New("last record ID does not fit in 64 bits")

// StakingContractsClient makes Bor's system calls to the Polygon staking
// contract and the distributor.
type StakingContractsClient struct {
	stakingABI     abi.ABI
	distributorABI abi.ABI
	staking        common.Address
	distributor    common.Address
	chainConfig    *params.ChainConfig
}

func NewStakingContractsClient(chainConfig *params.ChainConfig) *StakingContractsClient {
	return &StakingContractsClient{
		stakingABI:     borabi.Staking(),
		distributorABI: borabi.Distributor(),
		staking:        common.HexToAddress(chainConfig.Bor.StakingContract),
		distributor:    common.HexToAddress(chainConfig.Bor.DistributorContract),
		chainConfig:    chainConfig,
	}
}

type validatorStatusArg struct {
	ValidatorID   *big.Int `abi:"validatorId"`
	InSet         bool     `abi:"inSet"`
	AcceptedNonce *big.Int `abi:"acceptedNonce"`
}

type participationArg struct {
	ValidatorID     *big.Int `abi:"validatorId"`
	MilestonesVoted *big.Int `abi:"milestonesVoted"`
	EthereumStake   *big.Int `abi:"ethereumStake"`
	PolygonStake    *big.Int `abi:"polygonStake"`
	InTransit       *big.Int `abi:"inTransit"`
	VotingPower     *big.Int `abi:"votingPower"`
}

// PackValidatorStatus builds the applyValidatorStatus call data. The record's
// fields go through unchanged.
func PackValidatorStatus(record *staking.ValidatorStatusRecord) ([]byte, error) {
	validators := make([]validatorStatusArg, len(record.Validators))
	for i, v := range record.Validators {
		validators[i] = validatorStatusArg{
			ValidatorID:   new(big.Int).SetUint64(v.ValidatorID),
			InSet:         v.InSet,
			AcceptedNonce: new(big.Int).SetUint64(v.AcceptedNonce),
		}
	}

	return borabi.Staking().Pack(applyValidatorStatusMethod, new(big.Int).SetUint64(record.ID), validators)
}

// PackParticipationReport builds the applyParticipationReport call data. The
// report's fields go through unchanged.
func PackParticipationReport(report *staking.ParticipationReport) ([]byte, error) {
	validators := make([]participationArg, len(report.Validators))
	for i, v := range report.Validators {
		if v.EthereumStake == nil || v.PolygonStake == nil || v.InTransit == nil {
			return nil, fmt.Errorf("participation report %d: validator %d has a missing stake amount", report.ID, v.ValidatorID)
		}

		validators[i] = participationArg{
			ValidatorID:     new(big.Int).SetUint64(v.ValidatorID),
			MilestonesVoted: new(big.Int).SetUint64(v.MilestonesVoted),
			EthereumStake:   v.EthereumStake,
			PolygonStake:    v.PolygonStake,
			InTransit:       v.InTransit,
			VotingPower:     new(big.Int).SetUint64(v.VotingPower),
		}
	}

	return borabi.Distributor().Pack(
		applyParticipationReportMethod,
		new(big.Int).SetUint64(report.ID),
		new(big.Int).SetUint64(report.StartBlock),
		new(big.Int).SetUint64(report.EndBlock),
		new(big.Int).SetUint64(report.Milestones),
		validators,
	)
}

// LastStatusID reads the ID of the last validator status record the staking
// contract took.
func (sc *StakingContractsClient) LastStatusID(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	return sc.readLastID(sc.stakingABI, lastStatusIDMethod, sc.staking, state, header, chCtx, vmCfg)
}

// LastReportID reads the ID of the last participation report the distributor took.
func (sc *StakingContractsClient) LastReportID(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	return sc.readLastID(sc.distributorABI, lastReportIDMethod, sc.distributor, state, header, chCtx, vmCfg)
}

// ApplyValidatorStatus delivers one validator status record to the staking contract.
func (sc *StakingContractsClient) ApplyValidatorStatus(record *staking.ValidatorStatusRecord, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	data, err := PackValidatorStatus(record)
	if err != nil {
		return 0, fmt.Errorf("packing validator status record %d: %w", record.ID, err)
	}

	return sc.systemCall(sc.staking, data, state, header, chCtx, vmCfg)
}

// ApplyParticipationReport delivers one participation report to the distributor.
func (sc *StakingContractsClient) ApplyParticipationReport(report *staking.ParticipationReport, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	data, err := PackParticipationReport(report)
	if err != nil {
		return 0, fmt.Errorf("packing participation report %d: %w", report.ID, err)
	}

	return sc.systemCall(sc.distributor, data, state, header, chCtx, vmCfg)
}

// LogStakeUpdates has the staking contract log one stake update for each
// validator whose stake changed since its last one.
func (sc *StakingContractsClient) LogStakeUpdates(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	data, err := sc.stakingABI.Pack(logStakeUpdatesMethod)
	if err != nil {
		return 0, fmt.Errorf("packing %s: %w", logStakeUpdatesMethod, err)
	}

	return sc.systemCall(sc.staking, data, state, header, chCtx, vmCfg)
}

// systemCall runs one call from the system address. Like a state sync, a call
// that reverts or runs out of gas does not fail the block.
func (sc *StakingContractsClient) systemCall(to common.Address, data []byte, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	statefull.PrepareStateSyncContext(state, sc.chainConfig, header.Number, header.Time, header.Coinbase, to)

	return statefull.ApplyMessage(context.Background(), statefull.GetSystemMessage(to, data), state, header, sc.chainConfig, chCtx, vmCfg)
}

// readLastID runs a view call on the block's working state. A contract with no
// code returns nothing, which fails here, so the block fails instead of
// delivering from ID 1 again.
func (sc *StakingContractsClient) readLastID(contractABI abi.ABI, method string, to common.Address, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error) {
	data, err := contractABI.Pack(method)
	if err != nil {
		return 0, fmt.Errorf("packing %s: %w", method, err)
	}

	statefull.PrepareStateSyncContext(state, sc.chainConfig, header.Number, header.Time, header.Coinbase, to)

	evm := vm.NewEVM(core.NewEVMBlockContext(header, chCtx, &header.Coinbase), state, sc.chainConfig, vmCfg)
	evm.SetTxContext(core.NewEVMTxContextForStateSync())

	ret, _, err := evm.StaticCall(params.BorSystemAddress, to, data, params.MaxTxGas)
	if err != nil {
		return 0, fmt.Errorf("calling %s on %s: %w", method, to, err)
	}

	lastID := new(*big.Int)
	if err := contractABI.UnpackIntoInterface(lastID, method, ret); err != nil {
		return 0, fmt.Errorf("unpacking %s from %s: %w", method, to, err)
	}

	if !(*lastID).IsUint64() {
		return 0, fmt.Errorf("%s on %s: %w", method, to, errLastIDOverflow)
	}

	return (*lastID).Uint64(), nil
}
