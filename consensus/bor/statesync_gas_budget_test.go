package bor

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// gasBoundBorConfig returns an Indore configuration with both state-sync budgets enabled.
func gasBoundBorConfig(gasBoundBlock *big.Int) *params.BorConfig {
	cfg := indoreBorConfig()
	cfg.ValenciaBlock = big.NewInt(0)
	cfg.AustinBlock = gasBoundBlock
	cfg.StateReceiverContract = "0x0000000000000000000000000000000000001001"
	return cfg
}

// gasBoundEvents builds a contiguous state-sync backlog accepted by validateEventRecord.
func gasBoundEvents(n int, eventTime time.Time) []*clerk.EventRecordWithTime {
	events := make([]*clerk.EventRecordWithTime, n)
	for i := range events {
		events[i] = &clerk.EventRecordWithTime{
			EventRecord: clerk.EventRecord{
				ID:       uint64(i + 1),
				Contract: common.HexToAddress("0x1001"),
				Data:     []byte{0x01},
				ChainID:  "1",
			},
			Time: eventTime,
		}
	}
	return events
}

// TestCommitStates_StateSyncGasBudget verifies the threshold, progress guarantee,
// zero-gas behavior, and unchanged pre-activation behavior.
func TestCommitStates_StateSyncGasBudget(t *testing.T) {
	t.Parallel()
	const numEvents = 20
	budget := params.MaxStateSyncGasPerBlock

	// The threshold is checked before the next record. The record that reaches or
	// crosses it remains included, and any following record is deferred.
	cases := []struct {
		name          string
		gasBoundBlock *big.Int
		gasUsed       uint64
		want          int
	}{
		{"crosses threshold", big.NewInt(0), 4_000_000, 8},
		{"exact threshold", big.NewInt(0), 5_000_000, 6},
		{"single record exceeds threshold", big.NewInt(0), budget + 1, 1},
		{"zero gas", big.NewInt(0), 0, numEvents},
		{"before activation", big.NewInt(17), 5_000_000, numEvents},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := common.HexToAddress("0x1")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
			chain, b := newChainAndBorForTest(t, sp, gasBoundBorConfig(tc.gasBoundBlock), true, addr, uint64(time.Now().Unix())-200)
			b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{lastStateID: 0, gasUsed: tc.gasUsed}

			now := time.Now()
			b.SetHeimdallClient(&mockHeimdallClient{
				span: &borTypes.Span{
					Id: 0, StartBlock: 0, EndBlock: 255, BorChainId: "1",
					ValidatorSet:      stakeTypes.ValidatorSet{Validators: []*stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}}},
					SelectedProducers: []stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}},
				},
				events: gasBoundEvents(numEvents, now.Add(-60*time.Second)),
			})

			hc := chain.HeaderChain()
			genesis := hc.GetHeaderByNumber(0)
			statedb := newStateDBForTest(t, genesis.Root)
			h := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			result, err := b.CommitStates(statedb, h, statefull.ChainContext{Chain: hc, Bor: b})
			require.NoError(t, err)
			// CommitStates must return exactly the contiguous prefix admitted by the budget.
			require.Len(t, result, tc.want)
		})
	}
}

// TestCommitStates_FetchesPagesOnlyWhileAdmitting verifies that Heimdall is
// asked for the next page only while every record so far was admitted.
func TestCommitStates_FetchesPagesOnlyWhileAdmitting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		gasBoundBlock *big.Int
		numEvents     int
		gasUsed       uint64
		failFromID    uint64
		dropID        uint64
		override      map[string]int
		rangeOverride int
		pageSize      int
		wantRecords   int
		wantFetches   int
	}{
		{"budget fills on first page", big.NewInt(0), 1000, 5_000_000, 0, 0, nil, -1, 50, 6, 1},
		{"budget fills on second page", big.NewInt(0), 1000, 400_000, 0, 0, nil, -1, 50, 75, 2},
		{"no budget drains the backlog", big.NewInt(1000), 120, 5_000_000, 0, 0, nil, -1, 50, 120, 4},
		{"empty backlog", big.NewInt(0), 0, 5_000_000, 0, 0, nil, -1, 50, 0, 1},
		{"id gap stops fetching", big.NewInt(1000), 120, 5_000_000, 0, 30, nil, -1, 50, 29, 1},
		{"budget fills on the last record of a page", big.NewInt(0), 1000, 5_000_000, 0, 0, nil, -1, 6, 6, 1},
		{"fetch error fails the block", big.NewInt(1000), 120, 5_000_000, 1, 0, nil, -1, 50, 0, 1},
		{"fetch error after a page fails the block", big.NewInt(1000), 120, 5_000_000, 51, 0, nil, -1, 50, 0, 2},
		{"override spans pages", big.NewInt(1000), 120, 5_000_000, 0, 0, map[string]int{"16": 70}, -1, 50, 70, 2},
		{"override zero fetches nothing", big.NewInt(1000), 120, 5_000_000, 0, 0, map[string]int{"16": 0}, -1, 50, 0, 0},
		{"range override spans pages", big.NewInt(1000), 120, 5_000_000, 0, 0, nil, 70, 50, 70, 2},
		{"range override wins over a smaller map override", big.NewInt(1000), 120, 5_000_000, 0, 0, map[string]int{"16": 0}, 70, 50, 70, 2},
		{"single-record pages keep paging", big.NewInt(1000), 3, 5_000_000, 0, 0, nil, -1, 1, 3, 4},
		{"range override wins over a larger map override", big.NewInt(1000), 120, 5_000_000, 0, 0, map[string]int{"16": 70}, 30, 50, 30, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := common.HexToAddress("0x1")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
			cfg := gasBoundBorConfig(tc.gasBoundBlock)
			cfg.ValenciaBlock = tc.gasBoundBlock
			cfg.OverrideStateSyncRecords = tc.override
			if tc.rangeOverride >= 0 {
				cfg.OverrideStateSyncRecordsInRange = []params.BlockRangeOverride{{StartBlock: 16, EndBlock: 16, Value: tc.rangeOverride}}
			}
			chain, b := newChainAndBorForTest(t, sp, cfg, true, addr, uint64(time.Now().Unix())-200)
			b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{lastStateID: 0, gasUsed: tc.gasUsed}

			now := time.Now()
			events := gasBoundEvents(tc.numEvents, now.Add(-60*time.Second))
			if tc.dropID != 0 {
				events = append(events[:tc.dropID-1], events[tc.dropID:]...)
			}
			fetches := 0
			b.SetHeimdallClient(&mockHeimdallClient{
				span: &borTypes.Span{
					Id: 0, StartBlock: 0, EndBlock: 255, BorChainId: "1",
					ValidatorSet:      stakeTypes.ValidatorSet{Validators: []*stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}}},
					SelectedProducers: []stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}},
				},
				stateSyncEventFn: func(ctx context.Context, fromID uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
					fetches++

					if tc.failFromID != 0 && fromID >= tc.failFromID {
						return nil, errors.New("heimdall unavailable")
					}

					page := make([]*clerk.EventRecordWithTime, 0, tc.pageSize)
					for _, e := range events {
						if e.ID >= fromID && len(page) < tc.pageSize {
							page = append(page, e)
						}
					}

					return page, nil
				},
			})

			hc := chain.HeaderChain()
			genesis := hc.GetHeaderByNumber(0)
			statedb := newStateDBForTest(t, genesis.Root)
			h := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			result, err := b.CommitStates(statedb, h, statefull.ChainContext{Chain: hc, Bor: b})
			require.Equal(t, tc.failFromID != 0, err != nil, "err: %v", err)
			require.Len(t, result, tc.wantRecords)
			require.Equal(t, tc.wantFetches, fetches)
		})
	}
}

// logCapture records the first occurrence of each log message and its attributes.
type logCapture struct {
	mu      sync.Mutex
	records map[string]map[string]any
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logCapture) WithGroup(string) slog.Handler            { return l }

func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, seen := l.records[r.Message]; !seen {
		l.records[r.Message] = attrs
	}

	return nil
}

// TestCommitStates_LogsWhyItStopped swaps the global logger, so it must not run in parallel.
func TestCommitStates_LogsWhyItStopped(t *testing.T) {
	big30k := make([]byte, params.MaxStateSyncRecordBytes+1)
	atCap := make([]byte, params.MaxStateSyncRecordBytes)

	cases := []struct {
		name      string
		gasUsed   uint64
		data      []byte
		failFrom  uint64
		dropID    uint64
		wantMsg   string
		wantAttrs map[string]any // nil means the message must not be logged
	}{
		{"fetch error", 1, nil, 51, 0, "Error occurred when fetching state sync events", map[string]any{"fromID": uint64(51)}},
		{"invalid record", 1, nil, 0, 30, "while validating event record", map[string]any{"lastStateID": uint64(29)}},
		{"gas budget", 5_000_000, nil, 0, 0, "state-sync gas budget reached, deferring remaining records", map[string]any{"deferredFromID": uint64(7)}},
		{"byte budget", 1, big30k, 0, 0, "state-sync byte budget reached, deferring remaining records", map[string]any{"deferredFromID": uint64(35)}},
		{"oversized record", 1, big30k, 0, 0, "state-sync record exceeds expected per-record cap", map[string]any{"id": uint64(1)}},
		{"record at the cap", 1, atCap, 0, 0, "state-sync record exceeds expected per-record cap", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &logCapture{records: map[string]map[string]any{}}
			prev := log.Root()
			log.SetDefault(log.NewLogger(capture))
			t.Cleanup(func() { log.SetDefault(prev) })

			addr := common.HexToAddress("0x1")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
			chain, b := newChainAndBorForTest(t, sp, gasBoundBorConfig(big.NewInt(0)), true, addr, uint64(time.Now().Unix())-200)
			b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{gasUsed: tc.gasUsed}

			now := time.Now()
			events := gasBoundEvents(120, now.Add(-60*time.Second))
			for _, e := range events {
				if tc.data != nil {
					e.Data = tc.data
				}
			}

			if tc.dropID != 0 {
				events = append(events[:tc.dropID-1], events[tc.dropID:]...)
			}

			b.SetHeimdallClient(&mockHeimdallClient{
				span: &borTypes.Span{
					Id: 0, StartBlock: 0, EndBlock: 255, BorChainId: "1",
					ValidatorSet:      stakeTypes.ValidatorSet{Validators: []*stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}}},
					SelectedProducers: []stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}},
				},
				stateSyncEventFn: func(_ context.Context, fromID uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
					if tc.failFrom != 0 && fromID >= tc.failFrom {
						return nil, errors.New("heimdall unavailable")
					}

					page := make([]*clerk.EventRecordWithTime, 0, 50)
					for _, e := range events {
						if e.ID >= fromID && len(page) < 50 {
							page = append(page, e)
						}
					}

					return page, nil
				},
			})

			hc := chain.HeaderChain()
			genesis := hc.GetHeaderByNumber(0)
			h := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			_, err := b.CommitStates(newStateDBForTest(t, genesis.Root), h, statefull.ChainContext{Chain: hc, Bor: b})
			require.Equal(t, tc.failFrom != 0, err != nil, "err: %v", err)

			capture.mu.Lock()
			defer capture.mu.Unlock()

			_, summarized := capture.records["StateSyncData"]
			require.Equal(t, tc.failFrom == 0, summarized, "StateSyncData log")

			attrs, ok := capture.records[tc.wantMsg]
			require.Equal(t, tc.wantAttrs != nil, ok, "log %q", tc.wantMsg)

			for k, v := range tc.wantAttrs {
				require.Equal(t, v, attrs[k], k)
			}
		})
	}
}

func TestCommitStates_CommitErrorFailsBlock(t *testing.T) {
	t.Parallel()
	addr := common.HexToAddress("0x1")
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
	chain, b := newChainAndBorForTest(t, sp, gasBoundBorConfig(big.NewInt(0)), true, addr, uint64(time.Now().Unix())-200)
	b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{commitErr: errors.New("commit failed")}

	now := time.Now()
	b.SetHeimdallClient(&mockHeimdallClient{
		span: &borTypes.Span{
			Id: 0, StartBlock: 0, EndBlock: 255, BorChainId: "1",
			ValidatorSet:      stakeTypes.ValidatorSet{Validators: []*stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}}},
			SelectedProducers: []stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}},
		},
		events: gasBoundEvents(3, now.Add(-60*time.Second)),
	})

	hc := chain.HeaderChain()
	genesis := hc.GetHeaderByNumber(0)
	h := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

	result, err := b.CommitStates(newStateDBForTest(t, genesis.Root), h, statefull.ChainContext{Chain: hc, Bor: b})
	require.Error(t, err)
	require.Nil(t, result)
}

// stateSyncContextContract observes the transaction-scoped state presented to
// each callback and then repopulates it for the following callback.
type stateSyncContextContract struct {
	t              *testing.T
	gasUsed        uint64
	calls          int
	wantPrepared   bool
	stateReceiver  common.Address
	staleAddress   common.Address
	transientKey   common.Hash
	transientValue common.Hash
}

func (m *stateSyncContextContract) CommitState(_ *clerk.EventRecordWithTime, state vm.StateDB, _ *types.Header, _ statefull.ChainContext, _ vm.Config) (uint64, error) {
	m.t.Helper()
	if m.wantPrepared {
		// Preparation removes stale state while warming the canonical sender and destination.
		require.False(m.t, state.AddressInAccessList(m.staleAddress))
		require.True(m.t, state.AddressInAccessList(params.BorSystemAddress))
		require.True(m.t, state.AddressInAccessList(m.stateReceiver))
		require.Zero(m.t, state.GetTransientState(m.staleAddress, m.transientKey))
	} else {
		// Before activation, CommitStates preserves the historical transaction context.
		require.True(m.t, state.AddressInAccessList(m.staleAddress))
		require.Equal(m.t, m.transientValue, state.GetTransientState(m.staleAddress, m.transientKey))
	}

	// Reintroduce stale values so the next callback proves preparation occurs per record.
	state.AddAddressToAccessList(m.staleAddress)
	state.SetTransientState(m.staleAddress, m.transientKey, m.transientValue)
	m.calls++
	return m.gasUsed, nil
}

func (m *stateSyncContextContract) LastStateId(*state.StateDB, uint64, common.Hash) (*big.Int, error) {
	return new(big.Int), nil
}

// prepareTrackingState records every Prepare call while preserving StateDB behavior.
type prepareTrackingState struct {
	vm.StateDB
	calls []params.Rules
}

func (s *prepareTrackingState) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, accesses types.AccessList) {
	s.calls = append(s.calls, rules)
	s.StateDB.Prepare(rules, sender, coinbase, dest, precompiles, accesses)
}

// TestCommitStates_StateSyncContextPreparation verifies that only admitted
// post-activation records receive a fresh transaction context.
func TestCommitStates_StateSyncContextPreparation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		gasBoundBlock    *big.Int
		gasUsed          uint64
		events           int
		wantIncluded     int
		wantPrepareCalls int
		wantPrepared     bool
	}{
		{"before activation", big.NewInt(17), 1, 2, 2, 0, false},
		{"each admitted record", big.NewInt(0), params.MaxStateSyncGasPerBlock / 4, 2, 2, 2, true},
		{"deferred record", big.NewInt(0), params.MaxStateSyncGasPerBlock + 1, 2, 1, 1, true},
		{"empty backlog", big.NewInt(0), 1, 0, 0, 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			now := time.Now()
			signer := common.HexToAddress("0x1")
			stateReceiver := common.HexToAddress("0x1001")
			staleAddress := common.HexToAddress("0x2002")
			transientKey := common.HexToHash("0x01")
			transientValue := common.HexToHash("0x02")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: signer, VotingPower: 1}}}
			borConfig := gasBoundBorConfig(test.gasBoundBlock)
			chainConfig := newAllForksChainConfig(borConfig)
			chain, b := newChainAndBorForTestWithConfig(t, sp, chainConfig, true, signer, uint64(now.Unix())-200)
			contract := &stateSyncContextContract{
				t:              t,
				gasUsed:        test.gasUsed,
				wantPrepared:   test.wantPrepared,
				stateReceiver:  stateReceiver,
				staleAddress:   staleAddress,
				transientKey:   transientKey,
				transientValue: transientValue,
			}
			b.GenesisContractsClient = contract
			b.SetHeimdallClient(&mockHeimdallClient{events: gasBoundEvents(test.events, now.Add(-60*time.Second))})

			chainContext := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
			genesis := chainContext.GetHeaderByNumber(0)
			statedb := newStateDBForTest(t, genesis.Root)
			rootBefore := statedb.IntermediateRoot(params.Rules{})
			// Seed transaction-scoped values and a prior log. Prepare should clear only
			// the transaction-scoped values and leave consensus state intact.
			existingLog := &types.Log{Address: staleAddress, Data: []byte{0x01}}
			statedb.AddLog(existingLog)
			statedb.AddAddressToAccessList(staleAddress)
			statedb.SetTransientState(staleAddress, transientKey, transientValue)
			trackedState := &prepareTrackingState{StateDB: statedb}
			header := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			result, err := b.CommitStates(trackedState, header, chainContext)
			require.NoError(t, err)
			// Each included record executes exactly once and each eligible execution
			// receives exactly one Prepare call.
			require.Len(t, result, test.wantIncluded)
			require.Equal(t, test.wantIncluded, contract.calls)
			require.Len(t, trackedState.calls, test.wantPrepareCalls)
			for _, rules := range trackedState.calls {
				// Bor state-sync calls use non-merge execution rules.
				require.False(t, rules.IsMerge)
			}
			// Prepare must not remove existing logs or modify the persistent state root.
			require.Equal(t, []*types.Log{existingLog}, statedb.Logs())
			require.Equal(t, rootBefore, statedb.IntermediateRoot(params.Rules{}))
		})
	}
}
