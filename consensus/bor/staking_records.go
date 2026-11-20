package bor

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/staking"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
)

// StakingContract makes the system calls that deliver Heimdall's staking
// records to the Polygon staking contracts.
type StakingContract interface {
	LastStatusID(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error)
	LastReportID(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error)
	ApplyValidatorStatus(record *staking.ValidatorStatusRecord, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error)
	ApplyParticipationReport(report *staking.ParticipationReport, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error)
	LogStakeUpdates(state vm.StateDB, header *types.Header, chCtx statefull.ChainContext, vmCfg vm.Config) (uint64, error)
}

// stakeUpdateInterval is how often, in blocks, the staking contract is asked
// to log its stake updates. It doesn't depend on the sprint length.
const stakeUpdateInterval = 256

// stakingWork is what the staking calls of one block leave behind for its
// staking transaction and receipt.
type stakingWork struct {
	tx *types.StakingTx
	// logStart is how many logs the block's state held before the first
	// staking call. The staking transaction's logs are the ones from there on.
	logStart int
}

// SystemWork is what FinalizeForPipeline hands to AssembleBlock: the content
// of the block's system transactions. A nil SystemWork means the block has none.
type SystemWork struct {
	stateSync []*types.StateSyncData
	staking   *stakingWork
}

func newSystemWork(stateSync []*types.StateSyncData, staking *stakingWork) *SystemWork {
	if len(stateSync) == 0 && staking == nil {
		return nil
	}

	return &SystemWork{stateSync: stateSync, staking: staking}
}

func (w *SystemWork) stateSyncData() []*types.StateSyncData {
	if w == nil {
		return nil
	}

	return w.stateSync
}

func (w *SystemWork) stakingWork() *stakingWork {
	if w == nil {
		return nil
	}

	return w.staking
}

// commitStaking makes the block's calls to the staking contracts, in a fixed
// order: validator status records, then participation reports, both at a
// sprint start only, then the stake update call on every stakeUpdateInterval
// blocks. It runs after state syncs, so stake that lands by state sync in this
// block is in this block's stake update.
//
// It returns nil when the block has no staking transaction: before the fork,
// and on blocks where no record was delivered and no stake update is due.
func (c *Bor) commitStaking(state vm.StateDB, header *types.Header, chain statefull.ChainContext) (*stakingWork, error) {
	if !c.config.IsNativeStaking(header.Number) {
		return nil, nil
	}

	number := header.Number.Uint64()
	work := &stakingWork{tx: &types.StakingTx{BlockNumber: number}, logStart: len(state.Logs())}

	if c.HeimdallClient != nil && IsSprintStart(number, c.config.CalculateSprint(number)) {
		if err := c.commitStakingRecords(work.tx, state, header, chain); err != nil {
			return nil, err
		}
	}

	if number%stakeUpdateInterval == 0 {
		gasUsed, err := c.StakingContractsClient.LogStakeUpdates(state, header, chain, c.systemTxVMConfig(state))
		if err != nil {
			return nil, fmt.Errorf("logging stake updates: %w", err)
		}

		work.tx.StakeUpdate = true

		log.Info("Logged stake updates", "number", number, "gas", gasUsed)
	}

	if len(work.tx.StatusRecords) == 0 && len(work.tx.Reports) == 0 && !work.tx.StakeUpdate {
		return nil, nil
	}

	return work, nil
}

// commitStakingRecords delivers validator status records and then
// participation reports, and adds each delivered record to tx. It reads each
// contract's last ID from the block's state and asks Heimdall for the records
// after it, the way CommitStates does for state syncs.
func (c *Bor) commitStakingRecords(tx *types.StakingTx, state vm.StateDB, header *types.Header, chain statefull.ChainContext) error {
	number := header.Number.Uint64()
	to := int64(header.Time - c.config.CalculateStateSyncDelay(number))

	c.spanStore.waitUntilHeimdallIsSynced(c.ctx)

	start := time.Now()

	if err := c.deliverStatusRecords(tx, to, state, header, chain); err != nil {
		return err
	}

	if err := c.deliverReports(tx, to, state, header, chain); err != nil {
		return err
	}

	log.Info("Delivered staking records", "number", number, "to", to,
		"statuses", len(tx.StatusRecords), "reports", len(tx.Reports), "elapsed", time.Since(start))

	return nil
}

// deliverStatusRecords delivers the validator status records after the staking
// contract's last ID.
func (c *Bor) deliverStatusRecords(tx *types.StakingTx, to int64, state vm.StateDB, header *types.Header, chain statefull.ChainContext) error {
	vmCfg := c.systemTxVMConfig(state)

	lastID, err := c.StakingContractsClient.LastStatusID(state, header, chain, vmCfg)
	if err != nil {
		return fmt.Errorf("reading last validator status ID: %w", err)
	}

	_, err = deliverRecords(lastID,
		func(fromID uint64) ([]*staking.ValidatorStatusRecord, error) {
			return c.HeimdallClient.ValidatorStatusRecords(c.ctx, fromID, to)
		},
		func(r *staking.ValidatorStatusRecord) uint64 { return r.ID },
		func(r *staking.ValidatorStatusRecord) error {
			if _, err := c.StakingContractsClient.ApplyValidatorStatus(r, state, header, chain, vmCfg); err != nil {
				return err
			}

			tx.StatusRecords = append(tx.StatusRecords, statusTxRecord(r))

			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("delivering validator status records after id %d: %w", lastID, err)
	}

	return nil
}

// deliverReports delivers the participation reports after the distributor's
// last ID.
func (c *Bor) deliverReports(tx *types.StakingTx, to int64, state vm.StateDB, header *types.Header, chain statefull.ChainContext) error {
	vmCfg := c.systemTxVMConfig(state)

	lastID, err := c.StakingContractsClient.LastReportID(state, header, chain, vmCfg)
	if err != nil {
		return fmt.Errorf("reading last participation report ID: %w", err)
	}

	_, err = deliverRecords(lastID,
		func(fromID uint64) ([]*staking.ParticipationReport, error) {
			return c.HeimdallClient.ParticipationReports(c.ctx, fromID, to)
		},
		func(r *staking.ParticipationReport) uint64 { return r.ID },
		func(r *staking.ParticipationReport) error {
			if _, err := c.StakingContractsClient.ApplyParticipationReport(r, state, header, chain, vmCfg); err != nil {
				return err
			}

			tx.Reports = append(tx.Reports, reportTxRecord(r))

			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("delivering participation reports after id %d: %w", lastID, err)
	}

	return nil
}

// statusTxRecord is the record as the block's staking transaction carries it:
// the fields that go into the call, without the record time.
func statusTxRecord(r *staking.ValidatorStatusRecord) types.StakingStatusRecord {
	validators := make([]types.StakingValidatorStatus, len(r.Validators))
	for i, v := range r.Validators {
		validators[i] = types.StakingValidatorStatus{ValidatorID: v.ValidatorID, InSet: v.InSet, AcceptedNonce: v.AcceptedNonce}
	}

	return types.StakingStatusRecord{ID: r.ID, Validators: validators}
}

// reportTxRecord is the report as the block's staking transaction carries it:
// the fields that go into the call, without the record time.
func reportTxRecord(r *staking.ParticipationReport) types.StakingReport {
	validators := make([]types.StakingParticipation, len(r.Validators))
	for i, v := range r.Validators {
		validators[i] = types.StakingParticipation{
			ValidatorID:     v.ValidatorID,
			MilestonesVoted: v.MilestonesVoted,
			EthereumStake:   v.EthereumStake,
			PolygonStake:    v.PolygonStake,
			InTransit:       v.InTransit,
			VotingPower:     v.VotingPower,
		}
	}

	return types.StakingReport{ID: r.ID, StartBlock: r.StartBlock, EndBlock: r.EndBlock, Milestones: r.Milestones, Validators: validators}
}

// deliverRecords fetches one Heimdall page at a time from the ID after lastID
// and applies the records in ID order, until a page brings nothing new. It
// returns how many it applied.
//
// A fetch error is returned: delivering only what was fetched so far would give
// a different block from nodes whose fetch succeeded. A gap in the IDs ends the
// delivery without an error, because every node sees the same gap and the
// contract takes only the next ID.
func deliverRecords[T any](lastID uint64, fetch func(fromID uint64) ([]T, error), id func(T) uint64, apply func(T) error) (int, error) {
	delivered := 0

	for {
		page, err := fetch(lastID + 1)
		if err != nil {
			return delivered, err
		}

		applied, gap, err := applyPage(lastID, page, id, apply)
		delivered += applied
		lastID += uint64(applied)

		if err != nil || gap || applied == 0 {
			return delivered, err
		}
	}
}

// applyPage applies the records of one page that continue from lastID, in
// order, skipping any at or below it. It reports how many it applied and
// whether it stopped at a gap in the IDs.
func applyPage[T any](lastID uint64, page []T, id func(T) uint64, apply func(T) error) (applied int, gap bool, err error) {
	for _, record := range page {
		recordID := id(record)
		if recordID <= lastID {
			continue
		}

		if recordID != lastID+1 {
			log.Error("Gap in staking records from Heimdall", "want", lastID+1, "got", recordID)
			return applied, true, nil
		}

		if err := apply(record); err != nil {
			return applied, false, err
		}

		lastID = recordID
		applied++
	}

	return applied, false, nil
}
