package deneb

import (
	"github.com/protolambda/ztyp/view"

	"github.com/protolambda/zrnt/eth2/beacon/capella"
	"github.com/protolambda/zrnt/eth2/beacon/common"
)

func UpgradeToDeneb(spec *common.Spec, epc *common.EpochsContext, pre *capella.BeaconStateView) (*BeaconStateView, error) {
	// yes, super ugly code, but it does transfer compatible subtrees without duplicating data or breaking caches
	slot, err := pre.Slot()
	if err != nil {
		return nil, err
	}
	epoch := spec.SlotToEpoch(slot)
	genesisTime, err := pre.GenesisTime()
	if err != nil {
		return nil, err
	}
	genesisValidatorsRoot, err := pre.GenesisValidatorsRoot()
	if err != nil {
		return nil, err
	}
	preFork, err := pre.Fork()
	if err != nil {
		return nil, err
	}
	fork := common.Fork{
		PreviousVersion: preFork.CurrentVersion,
		CurrentVersion:  spec.DENEB_FORK_VERSION,
		Epoch:           epoch,
	}
	latestBlockHeader, err := pre.LatestBlockHeader()
	if err != nil {
		return nil, err
	}
	blockRoots, err := pre.BlockRoots()
	if err != nil {
		return nil, err
	}
	stateRoots, err := pre.StateRoots()
	if err != nil {
		return nil, err
	}
	historicalRoots, err := pre.HistoricalRoots()
	if err != nil {
		return nil, err
	}
	eth1Data, err := pre.Eth1Data()
	if err != nil {
		return nil, err
	}
	eth1DataVotes, err := pre.Eth1DataVotes()
	if err != nil {
		return nil, err
	}
	eth1DepositIndex, err := pre.Eth1DepositIndex()
	if err != nil {
		return nil, err
	}
	validators, err := pre.Validators()
	if err != nil {
		return nil, err
	}
	balances, err := pre.Balances()
	if err != nil {
		return nil, err
	}
	randaoMixes, err := pre.RandaoMixes()
	if err != nil {
		return nil, err
	}
	slashings, err := pre.Slashings()
	if err != nil {
		return nil, err
	}
	previousEpochParticipation, err := pre.PreviousEpochParticipation()
	if err != nil {
		return nil, err
	}
	currentEpochParticipation, err := pre.CurrentEpochParticipation()
	if err != nil {
		return nil, err
	}
	justBits, err := pre.JustificationBits()
	if err != nil {
		return nil, err
	}
	prevJustCh, err := pre.PreviousJustifiedCheckpoint()
	if err != nil {
		return nil, err
	}
	currJustCh, err := pre.CurrentJustifiedCheckpoint()
	if err != nil {
		return nil, err
	}
	finCh, err := pre.FinalizedCheckpoint()
	if err != nil {
		return nil, err
	}
	inactivityScores, err := pre.InactivityScores()
	if err != nil {
		return nil, err
	}
	currentSyncCommitteeView, err := pre.CurrentSyncCommittee()
	if err != nil {
		return nil, err
	}
	nextSyncCommitteeView, err := pre.NextSyncCommittee()
	if err != nil {
		return nil, err
	}
	latestExecutionPayloadHeader, err := pre.LatestExecutionPayloadHeader()
	if err != nil {
		return nil, err
	}
	oldExecutionHeader, err := latestExecutionPayloadHeader.Raw()
	if err != nil {
		return nil, err
	}
	updatedExecutionPayloadHeader := &ExecutionPayloadHeader{
		ParentHash:       oldExecutionHeader.ParentHash,
		FeeRecipient:     oldExecutionHeader.FeeRecipient,
		StateRoot:        oldExecutionHeader.StateRoot,
		ReceiptsRoot:     oldExecutionHeader.ReceiptsRoot,
		LogsBloom:        oldExecutionHeader.LogsBloom,
		PrevRandao:       oldExecutionHeader.PrevRandao,
		BlockNumber:      oldExecutionHeader.BlockNumber,
		GasLimit:         oldExecutionHeader.GasLimit,
		GasUsed:          oldExecutionHeader.GasUsed,
		Timestamp:        oldExecutionHeader.Timestamp,
		ExtraData:        oldExecutionHeader.ExtraData,
		BaseFeePerGas:    oldExecutionHeader.BaseFeePerGas,
		BlockHash:        oldExecutionHeader.BlockHash,
		TransactionsRoot: oldExecutionHeader.TransactionsRoot,
		WithdrawalsRoot:  oldExecutionHeader.WithdrawalsRoot,
		BlobGasUsed:      0, // new in Deneb
		ExcessBlobGas:    0, // new in Deneb
	}
	nextWithdrawalIndex, err := pre.NextWithdrawalIndex()
	if err != nil {
		return nil, err
	}
	nextWithdrawalValidatorIndex, err := pre.NextWithdrawalValidatorIndex()
	if err != nil {
		return nil, err
	}
	nextHistoricalSummaries, err := pre.HistoricalSummaries()
	if err != nil {
		return nil, err
	}

	return AsBeaconStateView(BeaconStateType(spec).FromFields(
		(*view.Uint64View)(&genesisTime),
		(*view.RootView)(&genesisValidatorsRoot),
		(*view.Uint64View)(&slot),
		fork.View(),
		latestBlockHeader.View(),
		blockRoots.(view.View),
		stateRoots.(view.View),
		historicalRoots.(view.View),
		eth1Data.View(),
		eth1DataVotes.(view.View),
		(*view.Uint64View)(&eth1DepositIndex),
		validators.(view.View),
		balances.(view.View),
		randaoMixes.(view.View),
		slashings.(view.View),
		previousEpochParticipation,
		currentEpochParticipation,
		justBits.View(),
		prevJustCh.View(),
		currJustCh.View(),
		finCh.View(),
		inactivityScores,
		currentSyncCommitteeView,
		nextSyncCommitteeView,
		updatedExecutionPayloadHeader.View(),
		(*view.Uint64View)(&nextWithdrawalIndex),
		(*view.Uint64View)(&nextWithdrawalValidatorIndex),
		nextHistoricalSummaries.(*capella.HistoricalSummariesView),
	))
}
