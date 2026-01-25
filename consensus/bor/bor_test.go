package bor

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil" //nolint:typecheck
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/triedb"
	lru "github.com/hashicorp/golang-lru"
	ttlcache "github.com/jellydator/ttlcache/v3"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// fakeSpanner implements Spanner for tests
type fakeSpanner struct {
	vals             []*valset.Validator
	shouldFailCommit bool
	spanEndBlock     uint64
	spanID           uint64
}

func (s *fakeSpanner) GetCurrentSpan(ctx context.Context, headerHash common.Hash, st *state.StateDB) (*borTypes.Span, error) {
	endBlock := s.spanEndBlock
	if endBlock == 0 {
		endBlock = 255
	}
	spanID := s.spanID
	return &borTypes.Span{Id: spanID, StartBlock: 0, EndBlock: endBlock}, nil
}
func (s *fakeSpanner) GetCurrentValidatorsByHash(ctx context.Context, headerHash common.Hash, blockNumber uint64) ([]*valset.Validator, error) {
	return s.vals, nil
}
func (s *fakeSpanner) GetCurrentValidatorsByBlockNrOrHash(ctx context.Context, _ rpc.BlockNumberOrHash, _ uint64) ([]*valset.Validator, error) {
	return s.vals, nil
}
func (s *fakeSpanner) CommitSpan(ctx context.Context, _ borTypes.Span, _ []stakeTypes.MinimalVal, _ []stakeTypes.MinimalVal, _ vm.StateDB, _ *types.Header, _ core.ChainContext) error {
	if s.shouldFailCommit {
		return errors.New("span commit failed")
	}
	return nil
}

// failingHeimdallClient simulates HeimdallClient failures
type failingHeimdallClient struct{}

// failingGenesisContract simulates GenesisContract failures
type failingGenesisContract struct{}

func (f *failingGenesisContract) CommitState(event *clerk.EventRecordWithTime, state vm.StateDB, header *types.Header, chCtx statefull.ChainContext) (uint64, error) {
	return 0, errors.New("commit state failed")
}

func (f *failingGenesisContract) LastStateId(arg0 *state.StateDB, number uint64, hash common.Hash) (*big.Int, error) {
	return nil, errors.New("last state id failed")
}

func (f *failingHeimdallClient) Close() {}
func (f *failingHeimdallClient) FetchStateSyncEvents(ctx context.Context, fromID uint64, to int64, limit int) ([]*types.StateSyncData, error) {
	return nil, errors.New("state sync failed")
}
func (f *failingHeimdallClient) FetchStateSyncEvent(ctx context.Context, id uint64) (*types.StateSyncData, error) {
	return nil, errors.New("state sync failed")
}
func (f *failingHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return nil, errors.New("state sync failed")
}
func (f *failingHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*borTypes.Span, error) {
	return nil, errors.New("get span failed")
}
func (f *failingHeimdallClient) GetLatestSpan(ctx context.Context) (*borTypes.Span, error) {
	return nil, errors.New("get latest span failed")
}
func (f *failingHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return nil, errors.New("fetch checkpoint failed")
}
func (f *failingHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return 0, errors.New("fetch checkpoint count failed")
}
func (f *failingHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return nil, errors.New("fetch milestone failed")
}
func (f *failingHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return 0, errors.New("fetch milestone count failed")
}
func (f *failingHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return nil, errors.New("fetch status failed")
}

// newChainAndBorForTest centralizes common Bor + HeaderChain initialization for tests
func newChainAndBorForTest(t *testing.T, sp Spanner, borCfg *params.BorConfig, devFake bool, signerAddr common.Address, genesisTime uint64) (*core.BlockChain, *Bor) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1), Bor: borCfg}

	b := &Bor{chainConfig: cfg, config: cfg.Bor, DevFakeAuthor: devFake}
	b.db = rawdb.NewMemoryDatabase()
	b.recents = ttlcache.New(
		ttlcache.WithTTL[common.Hash, *Snapshot](veblopBlockTimeout),
		ttlcache.WithCapacity[common.Hash, *Snapshot](inmemorySnapshots),
		ttlcache.WithDisableTouchOnHit[common.Hash, *Snapshot](),
	)
	sig, _ := lru.NewARC(inmemorySignatures)
	b.signatures = sig
	b.recentVerifiedHeaders = ttlcache.New[common.Hash, *types.Header](
		ttlcache.WithTTL[common.Hash, *types.Header](veblopBlockTimeout),
		ttlcache.WithCapacity[common.Hash, *types.Header](inmemorySignatures),
		ttlcache.WithDisableTouchOnHit[common.Hash, *types.Header](),
	)
	b.spanStore = NewSpanStore(nil, sp, cfg.ChainID.String())
	b.SetSpanner(sp)
	// set a default authorized signer to prevent nil deref in snapshot
	b.authorizedSigner.Store(&signer{signer: common.Address{}, signFn: func(_ accounts.Account, _ string, _ []byte) ([]byte, error) {
		return nil, &UnauthorizedSignerError{0, common.Address{}.Bytes(), []*valset.Validator{}}
	}})

	if devFake && signerAddr != (common.Address{}) {
		b.authorizedSigner.Store(&signer{signer: signerAddr})
	}
	b.parentActualTimeCache, _ = lru.New(10)

	genspec := &core.Genesis{Config: cfg, Timestamp: genesisTime}
	db := rawdb.NewMemoryDatabase()
	_ = genspec.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults))
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genspec, b, core.DefaultConfig())
	require.NoError(t, err)
	return chain, b
}

func TestGenesisContractChange(t *testing.T) {
	t.Parallel()

	addr0 := common.Address{0x1}

	b := &Bor{
		config: &params.BorConfig{
			Sprint: map[string]uint64{
				"0": 10,
			}, // skip sprint transactions in sprint
			BlockAlloc: map[string]interface{}{
				// write as interface since that is how it is decoded in genesis
				"2": map[string]interface{}{
					addr0.Hex(): map[string]interface{}{
						"code":    hexutil.Bytes{0x1, 0x2},
						"balance": "0",
					},
				},
				"4": map[string]interface{}{
					addr0.Hex(): map[string]interface{}{
						"code":    hexutil.Bytes{0x1, 0x3},
						"balance": "0x1000",
					},
				},
				"6": map[string]interface{}{
					addr0.Hex(): map[string]interface{}{
						"code":    hexutil.Bytes{0x1, 0x4},
						"balance": "0x2000",
					},
				},
			},
		},
	}

	genspec := &core.Genesis{
		Alloc: map[common.Address]types.Account{
			addr0: {
				Balance: big.NewInt(0),
				Code:    []byte{0x1, 0x1},
			},
		},
		Config: &params.ChainConfig{},
	}

	db := rawdb.NewMemoryDatabase()

	genesis := genspec.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults))

	statedb, err := state.New(genesis.Root(), state.NewDatabase(triedb.NewDatabase(db, triedb.HashDefaults), nil))
	require.NoError(t, err)

	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genspec, b, core.DefaultConfig())
	require.NoError(t, err)

	addBlock := func(root common.Hash, num int64) (common.Hash, *state.StateDB) {
		h := &types.Header{
			ParentHash: root,
			Number:     big.NewInt(num),
		}
		b.Finalize(chain.HeaderChain(), h, statedb, &types.Body{Withdrawals: nil, Transactions: nil, Uncles: nil}, nil)

		// write state to database
		root, err := statedb.Commit(0, false, true)
		require.NoError(t, err)
		require.NoError(t, statedb.Database().TrieDB().Commit(root, true))

		statedb, err := state.New(root, state.NewDatabase(triedb.NewDatabase(db, triedb.HashDefaults), nil))
		require.NoError(t, err)

		return root, statedb
	}

	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x1})

	root := genesis.Root()

	// code does not change, balance remains 0
	root, statedb = addBlock(root, 1)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x1})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(0))

	// code changes 1st time, balance remains 0
	root, statedb = addBlock(root, 2)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x2})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(0))

	// code same as 1st change, balance remains 0
	root, statedb = addBlock(root, 3)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x2})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(0))

	// code changes 2nd time, balance updates to 4096
	root, statedb = addBlock(root, 4)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x3})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(4096))

	// code same as 2nd change, balance remains 4096
	root, statedb = addBlock(root, 5)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x3})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(4096))

	// code changes 3rd time, balance remains 4096
	_, statedb = addBlock(root, 6)
	require.Equal(t, statedb.GetCode(addr0), []byte{0x1, 0x4})
	require.Equal(t, statedb.GetBalance(addr0), uint256.NewInt(4096))
}

func TestEncodeSigHeaderJaipur(t *testing.T) {
	t.Parallel()

	// As part of the EIP-1559 fork in mumbai, an incorrect seal hash
	// was used for Bor that did not included the BaseFee. The Jaipur
	// block is a hard fork to fix that.
	h := &types.Header{
		Difficulty: new(big.Int),
		Number:     big.NewInt(1),
		Extra:      make([]byte, 32+65),
	}

	var (
		// hash for the block without the BaseFee
		hashWithoutBaseFee = common.HexToHash("0x1be13e83939b3c4701ee57a34e10c9290ce07b0e53af0fe90b812c6881826e36")
		// hash for the block with the baseFee
		hashWithBaseFee = common.HexToHash("0xc55b0cac99161f71bde1423a091426b1b5b4d7598e5981ad802cce712771965b")
	)

	// Jaipur NOT enabled and BaseFee not set
	hash := SealHash(h, &params.BorConfig{JaipurBlock: big.NewInt(10)})
	require.Equal(t, hash, hashWithoutBaseFee)

	// Jaipur enabled (Jaipur=0) and BaseFee not set
	hash = SealHash(h, &params.BorConfig{JaipurBlock: common.Big0})
	require.Equal(t, hash, hashWithoutBaseFee)

	h.BaseFee = big.NewInt(2)

	// Jaipur enabled (Jaipur=Header block) and BaseFee set
	hash = SealHash(h, &params.BorConfig{JaipurBlock: common.Big1})
	require.Equal(t, hash, hashWithBaseFee)

	// Jaipur NOT enabled and BaseFee set
	hash = SealHash(h, &params.BorConfig{JaipurBlock: big.NewInt(10)})
	require.Equal(t, hash, hashWithoutBaseFee)
}

func TestCalcProducerDelayRio(t *testing.T) {
	t.Parallel()

	// Test cases for VeBlop condition in CalcProducerDelay
	testCases := []struct {
		name        string
		blockNumber uint64
		succession  int
		config      *params.BorConfig
		expected    uint64
		description string
	}{
		{
			name:        "VeBlop enabled - early return with period only",
			blockNumber: 100,
			succession:  2,
			config: &params.BorConfig{
				Period: map[string]uint64{
					"0": 5, // 5 second period
				},
				Sprint: map[string]uint64{
					"0": 10,
				},
				ProducerDelay: map[string]uint64{
					"0": 3,
				},
				BackupMultiplier: map[string]uint64{
					"0": 2,
				},
				RioBlock: big.NewInt(50), // VeBlop enabled at block 50
			},
			expected:    5, // Should return period (5) without additional calculations
			description: "When VeBlop is enabled, should return period without producer delay or backup multiplier",
		},
		{
			name:        "VeBlop enabled - genesis block",
			blockNumber: 0,
			succession:  1,
			config: &params.BorConfig{
				Period: map[string]uint64{
					"0": 3,
				},
				Sprint: map[string]uint64{
					"0": 10,
				},
				ProducerDelay: map[string]uint64{
					"0": 5,
				},
				BackupMultiplier: map[string]uint64{
					"0": 4,
				},
				RioBlock: big.NewInt(0), // VeBlop enabled from genesis
			},
			expected:    3, // Should return period (3) only
			description: "When VeBlop is enabled from genesis, should return period without additional calculations",
		},
		{
			name:        "VeBlop not enabled - sprint start with succession",
			blockNumber: 100, // Sprint start (100 % 10 == 0)
			succession:  2,
			config: &params.BorConfig{
				Period: map[string]uint64{
					"0": 5,
				},
				Sprint: map[string]uint64{
					"0": 10,
				},
				ProducerDelay: map[string]uint64{
					"0": 3,
				},
				BackupMultiplier: map[string]uint64{
					"0": 2,
				},
				RioBlock: big.NewInt(200), // VeBlop enabled at block 200 (after current block)
			},
			expected:    7, // producer delay (3) + succession (2) * backup multiplier (2) = 3 + 4 = 7
			description: "When VeBlop is not enabled and it's sprint start, should use producer delay plus backup multiplier",
		},
		{
			name:        "VeBlop not enabled - non-sprint start with succession",
			blockNumber: 25, // Not sprint start (25 % 10 != 0)
			succession:  1,
			config: &params.BorConfig{
				Period: map[string]uint64{
					"0": 4,
				},
				Sprint: map[string]uint64{
					"0": 10,
				},
				ProducerDelay: map[string]uint64{
					"0": 6,
				},
				BackupMultiplier: map[string]uint64{
					"0": 3,
				},
				RioBlock: big.NewInt(100), // VeBlop not enabled yet
			},
			expected:    7, // period (4) + succession (1) * backup multiplier (3) = 4 + 3 = 7
			description: "When VeBlop is not enabled and it's not sprint start, should use period plus backup multiplier",
		},
		{
			name:        "VeBlop nil - sprint start without succession",
			blockNumber: 50, // Sprint start (50 % 10 == 0)
			succession:  0,
			config: &params.BorConfig{
				Period: map[string]uint64{
					"0": 4,
				},
				Sprint: map[string]uint64{
					"0": 10,
				},
				ProducerDelay: map[string]uint64{
					"0": 7,
				},
				BackupMultiplier: map[string]uint64{
					"0": 2,
				},
				RioBlock: nil, // VeBlop not configured (nil)
			},
			expected:    7, // producer delay since it's sprint start, no succession multiplier
			description: "When VeBlop is nil and it's sprint start without succession, should use producer delay",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := CalcProducerDelay(tc.blockNumber, tc.succession, tc.config)
			require.Equal(t, tc.expected, result, tc.description)
		})
	}
}

func TestPerformSpanCheck(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")
	addr2 := common.HexToAddress("0x2")

	type testCase struct {
		name               string
		targetNum          uint64
		setMilestone       *uint64
		provideParent      bool
		parentHasSignature bool
		sameAuthorAsParent bool
		recentVerified     bool
		expectErr          error
	}

	cases := []testCase{
		{name: "early return for block number 1", targetNum: 1},
		{name: "early return when milestone reached", targetNum: 50, setMilestone: uint64Ptr(100)},
		{name: "parent header nil triggers span wait", targetNum: 100, provideParent: false, parentHasSignature: false},
		{name: "missing parent signature returns error", targetNum: 10, provideParent: true, parentHasSignature: false, expectErr: errMissingSignature},
		{name: "same author without recent verification triggers span wait", targetNum: 43, provideParent: true, parentHasSignature: true, sameAuthorAsParent: true},
		{name: "different author no wait", targetNum: 20, provideParent: true, parentHasSignature: true, sameAuthorAsParent: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr2, VotingPower: 1}}}
			borCfg := &params.BorConfig{Sprint: map[string]uint64{"0": 64}, Period: map[string]uint64{"0": 2}}
			chain, b := newChainAndBorForTest(t, sp, borCfg, false, common.Address{}, uint64(time.Now().Unix()))

			var parents []*types.Header
			var parentHash common.Hash
			if c.provideParent {
				parent := &types.Header{Number: big.NewInt(int64(c.targetNum - 1))}
				parentHash = parent.Hash()
				if c.parentHasSignature {
					if c.sameAuthorAsParent {
						b.signatures.Add(parent.Hash(), addr1)
					} else {
						b.signatures.Add(parent.Hash(), addr2)
					}
				}
				parents = []*types.Header{parent}
			} else {
				parentHash = common.HexToHash("0xdead")
			}

			target := &types.Header{Number: big.NewInt(int64(c.targetNum)), ParentHash: parentHash}
			b.signatures.Add(target.Hash(), addr1)

			if c.recentVerified {
				b.recentVerifiedHeaders.Set(parentHash, target, ttlcache.DefaultTTL)
			}
			if c.setMilestone != nil {
				b.latestMilestoneBlock.Store(*c.setMilestone)
			}

			err := b.performSpanCheck(chain.HeaderChain(), target, parents)
			if c.expectErr != nil {
				require.Error(t, err)
				require.Equal(t, c.expectErr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }

func TestGetVeBlopSnapshot(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")
	addr2 := common.HexToAddress("0x2")

	type testCase struct {
		name         string
		spVals       []*valset.Validator
		targetNum    uint64
		expectAddrs  []common.Address
		checkNewSpan bool
	}

	cases := []testCase{
		{
			name:         "veblop snapshot with checkNewSpan=true",
			spVals:       []*valset.Validator{{Address: addr1, VotingPower: 1}, {Address: addr2, VotingPower: 2}},
			targetNum:    42,
			expectAddrs:  []common.Address{addr1, addr2},
			checkNewSpan: true,
		},
		{
			name:         "veblop snapshot with checkNewSpan=false",
			spVals:       []*valset.Validator{{Address: addr1, VotingPower: 1}, {Address: addr2, VotingPower: 2}},
			targetNum:    43,
			expectAddrs:  []common.Address{addr1, addr2},
			checkNewSpan: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &fakeSpanner{vals: c.spVals}
			borCfg := &params.BorConfig{Sprint: map[string]uint64{"0": 64}, Period: map[string]uint64{"0": 2}, RioBlock: big.NewInt(0)}
			chain, b := newChainAndBorForTest(t, sp, borCfg, false, common.Address{}, uint64(time.Now().Unix()))
			h := &types.Header{Number: big.NewInt(int64(c.targetNum))}
			snap, err := b.getVeBlopSnapshot(chain.HeaderChain(), h, nil, c.checkNewSpan)
			require.NoError(t, err)
			require.NotNil(t, snap)
			require.Equal(t, h.Number.Uint64(), snap.Number)
			require.Equal(t, h.Hash(), snap.Hash)

			seen := map[common.Address]bool{}
			for _, v := range snap.ValidatorSet.Validators {
				seen[v.Address] = true
			}
			for _, exp := range c.expectAddrs {
				require.True(t, seen[exp])
			}
		})
	}
}

func TestSnapshot(t *testing.T) {
	// Only consider case when c.config.IsRio(targetHeader.Number) != true
	t.Parallel()

	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	type testCase struct {
		name        string
		spVals      []*valset.Validator
		targetNum   uint64
		expectAddrs []common.Address
	}

	cases := []testCase{
		{
			name:        "snapshot uses non-VeBlop path and includes validators",
			spVals:      []*valset.Validator{{Address: addr1, VotingPower: 1}, {Address: addr2, VotingPower: 2}},
			targetNum:   2,
			expectAddrs: []common.Address{addr1, addr2},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &fakeSpanner{vals: c.spVals}
			// Configure RioBlock far in the future so IsRio(header.Number) == false
			borCfg := &params.BorConfig{Sprint: map[string]uint64{"0": 64}, Period: map[string]uint64{"0": 2}, RioBlock: big.NewInt(1_000_000)}
			chain, b := newChainAndBorForTest(t, sp, borCfg, false, common.Address{}, uint64(time.Now().Unix()))
			gen := chain.HeaderChain().GetHeaderByNumber(0)
			require.NotNil(t, gen)
			target := &types.Header{Number: big.NewInt(1), ParentHash: gen.Hash()}
			snap, err := b.snapshot(chain.HeaderChain(), target, []*types.Header{gen}, true)
			require.NoError(t, err)
			require.NotNil(t, snap)

			seen := map[common.Address]bool{}
			for _, v := range snap.ValidatorSet.Validators {
				seen[v.Address] = true
			}
			for _, exp := range c.expectAddrs {
				require.True(t, seen[exp])
			}
		})
	}
}

func TestCustomBlockTimeValidation(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")

	testCases := []struct {
		name            string
		blockTime       time.Duration
		consensusPeriod uint64
		blockNumber     uint64
		expectError     bool
		description     string
	}{
		{
			name:            "blockTime is zero (default) - should succeed",
			blockTime:       0,
			consensusPeriod: 2,
			blockNumber:     1,
			expectError:     false,
			description:     "Default blockTime of 0 should use standard consensus delay",
		},
		{
			name:            "blockTime equals consensus period - should succeed",
			blockTime:       2 * time.Second,
			consensusPeriod: 2,
			blockNumber:     1,
			expectError:     false,
			description:     "Custom blockTime equal to consensus period should be valid",
		},
		{
			name:            "blockTime greater than consensus period - should succeed",
			blockTime:       5 * time.Second,
			consensusPeriod: 2,
			blockNumber:     1,
			expectError:     false,
			description:     "Custom blockTime greater than consensus period should be valid",
		},
		{
			name:            "blockTime less than consensus period - should fail",
			blockTime:       1 * time.Second,
			consensusPeriod: 2,
			blockNumber:     1,
			expectError:     true,
			description:     "Custom blockTime less than consensus period should be invalid",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
			borCfg := &params.BorConfig{
				Sprint:   map[string]uint64{"0": 64},
				Period:   map[string]uint64{"0": tc.consensusPeriod},
				RioBlock: big.NewInt(0), // Enable Rio from genesis
			}
			chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(time.Now().Unix()))
			b.blockTime = tc.blockTime

			// Get genesis block as parent
			genesis := chain.HeaderChain().GetHeaderByNumber(0)
			require.NotNil(t, genesis)

			header := &types.Header{
				Number:     big.NewInt(int64(tc.blockNumber)),
				ParentHash: genesis.Hash(),
			}

			err := b.Prepare(chain.HeaderChain(), header)

			if tc.expectError {
				require.Error(t, err, tc.description)
				require.Contains(t, err.Error(), "less than the consensus block time", tc.description)
			} else {
				require.NoError(t, err, tc.description)
			}
		})
	}
}

func TestCustomBlockTimeCalculation(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")

	t.Run("sequential blocks with custom blockTime", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		borCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(0),
		}
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(time.Now().Unix()))
		b.blockTime = 5 * time.Second

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesis)
		baseTime := genesis.Time

		header1 := &types.Header{
			Number:     big.NewInt(1),
			ParentHash: genesis.Hash(),
		}
		err := b.Prepare(chain.HeaderChain(), header1)
		require.NoError(t, err)

		require.False(t, header1.ActualTime.IsZero(), "ActualTime should be set")
		expectedTime := time.Unix(int64(baseTime), 0).Add(5 * time.Second)
		require.Equal(t, expectedTime.Unix(), header1.ActualTime.Unix())
	})

	t.Run("lastMinedBlockTime is zero (first block)", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		borCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(0),
		}
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(time.Now().Unix()))
		b.blockTime = 3 * time.Second

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesis)
		baseTime := genesis.Time

		header := &types.Header{
			Number:     big.NewInt(1),
			ParentHash: genesis.Hash(),
		}

		err := b.Prepare(chain.HeaderChain(), header)
		require.NoError(t, err)

		expectedTime := time.Unix(int64(baseTime), 0).Add(3 * time.Second)
		require.Equal(t, expectedTime.Unix(), header.ActualTime.Unix())
	})

	t.Run("lastMinedBlockTime before parent time (fallback)", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		borCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(0),
		}
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(time.Now().Unix()))
		b.blockTime = 4 * time.Second

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesis)
		baseTime := genesis.Time
		parentHash := genesis.Hash()

		if baseTime > 10 {
			b.parentActualTimeCache.Add(parentHash, time.Unix(int64(baseTime-10), 0))
		} else {
			b.parentActualTimeCache.Add(parentHash, time.Unix(0, 0))
		}

		header := &types.Header{
			Number:     big.NewInt(1),
			ParentHash: parentHash,
		}

		err := b.Prepare(chain.HeaderChain(), header)
		require.NoError(t, err)

		expectedTime := time.Unix(int64(baseTime), 0).Add(4 * time.Second)
		require.Equal(t, expectedTime.Unix(), header.ActualTime.Unix())
	})
}

func TestCustomBlockTimeBackwardCompatibility(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")

	t.Run("blockTime is zero uses standard CalcProducerDelay", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		borCfg := &params.BorConfig{
			Sprint:           map[string]uint64{"0": 64},
			Period:           map[string]uint64{"0": 2},
			ProducerDelay:    map[string]uint64{"0": 3},
			BackupMultiplier: map[string]uint64{"0": 2},
			RioBlock:         big.NewInt(0),
		}
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(time.Now().Unix()))
		b.blockTime = 0

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesis)

		header := &types.Header{
			Number:     big.NewInt(1),
			ParentHash: genesis.Hash(),
		}

		err := b.Prepare(chain.HeaderChain(), header)
		require.NoError(t, err)

		require.True(t, header.ActualTime.IsZero(), "ActualTime should not be set when blockTime is 0")
	})
}

func TestCustomBlockTimeClampsToNowAlsoUpdatesActualTime(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")
	// Force parent time far in the past so that after adding blockTime, header.Time is still < now
	// and the "clamp to now + blockTime" block triggers.
	pastParentTime := time.Now().Add(-10 * time.Minute).Unix()

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
	borCfg := &params.BorConfig{
		Sprint:   map[string]uint64{"0": 64},
		Period:   map[string]uint64{"0": 2},
		RioBlock: big.NewInt(0), // Rio enabled from genesis
	}
	chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(pastParentTime))

	// Enable custom block time (must be >= Period to avoid validation error)
	b.blockTime = 5 * time.Second

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)

	header := &types.Header{
		Number:     big.NewInt(1),
		ParentHash: genesis.Hash(),
	}

	before := time.Now()
	err := b.Prepare(chain.HeaderChain(), header)
	after := time.Now()

	require.NoError(t, err)

	// With the late block fix, header.Time should be "now + blockTime", not just "now"
	// This gives the block builder sufficient time to include transactions
	expectedMinTime := before.Add(b.blockTime).Unix()
	expectedMaxTime := after.Add(b.blockTime).Unix() + 1 // +1 for timing tolerance

	require.GreaterOrEqual(t, int64(header.Time), expectedMinTime,
		"header.Time should be at least now + blockTime to provide build time")
	require.LessOrEqual(t, int64(header.Time), expectedMaxTime,
		"header.Time should be approximately now + blockTime")

	// Critical regression assertion:
	// When custom blockTime is enabled for Rio, clamping header.Time must also set ActualTime = now + blockTime.
	require.False(t, header.ActualTime.IsZero(), "ActualTime should be set when blockTime > 0 and Rio is enabled")
	require.GreaterOrEqual(t, header.ActualTime.Unix(), expectedMinTime,
		"ActualTime should be at least now + blockTime when clamping occurs")
	require.LessOrEqual(t, header.ActualTime.Unix(), expectedMaxTime,
		"ActualTime should be approximately now + blockTime when clamping occurs")

	// Since clamping sets both from the same calculation, they should match on Unix seconds.
	require.Equal(t, int64(header.Time), header.ActualTime.Unix(),
		"header.Time and ActualTime should align after clamping")
}

func TestVerifySealRejectsOversizedDifficulty(t *testing.T) {
	t.Parallel()

	// real key so ecrecover works
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	sp := &fakeSpanner{
		vals: []*valset.Validator{
			{Address: signerAddr, VotingPower: 1},
		},
	}

	borCfg := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 2},
	}

	// devFake=false, we need real signatures for the sake of this test
	chain, b := newChainAndBorForTest(t, sp, borCfg, false, common.Address{}, uint64(time.Now().Unix()))

	parent := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, parent)

	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     big.NewInt(1),
		Time:       parent.Time + borCfg.Period["0"],
	}

	// Build snapshot so we can compute the expected difficulty
	snap, err := b.snapshot(chain.HeaderChain(), header, []*types.Header{parent}, true)
	require.NoError(t, err)
	require.NotNil(t, snap)

	expected := Difficulty(snap.ValidatorSet, signerAddr)

	// Craft a huge difficulty whose low 64 bits match the expected
	hugeDiff := new(big.Int).Add(
		new(big.Int).SetUint64(expected),
		new(big.Int).Lsh(big.NewInt(1), 64),
	)
	header.Difficulty = hugeDiff

	// 32 bytes vanity + 65 bytes for the signature
	header.Extra = make([]byte, 32+65)

	// Compute the seal hash over the header
	sigHash := SealHash(header, borCfg)

	// Sign the seal hash
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	require.Len(t, sig, 65)

	// Put the signature in the last 65 bytes of Extra
	copy(header.Extra[len(header.Extra)-65:], sig)

	// verify the seal: we expect the difficulty validation to reject it
	err = b.verifySeal(chain.HeaderChain(), header, []*types.Header{parent})
	if err == nil {
		t.Fatalf("expected verifySeal to reject oversized difficulty, got nil")
	}

	var diffErr *WrongDifficultyError
	ok := errors.As(err, &diffErr)
	if !ok {
		t.Fatalf("expected WrongDifficultyError, got %T (%v)", err, err)
	}
	if diffErr.Number != header.Number.Uint64() {
		t.Fatalf("unexpected Number in WrongDifficultyError: got %d, want %d",
			diffErr.Number, header.Number.Uint64())
	}
	if diffErr.Expected != expected {
		t.Fatalf("unexpected Expected in WrongDifficultyError: got %d, want %d",
			diffErr.Expected, expected)
	}
	if diffErr.Actual != math.MaxUint64 {
		t.Fatalf("unexpected Actual in WrongDifficultyError: got %d, want %d",
			diffErr.Actual, uint64(math.MaxUint64))
	}
}

// TestLateBlockTimestampFix verifies that late blocks get sufficient build time
// by setting header.Time = now + blockPeriod instead of just clamping to now.
func TestLateBlockTimestampFix(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")
	borCfg := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 2},
	}

	t.Run("late parent gets future timestamp", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		oldParentTime := time.Now().Add(-4 * time.Second).Unix()
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(oldParentTime))

		header := &types.Header{Number: big.NewInt(1), ParentHash: chain.HeaderChain().GetHeaderByNumber(0).Hash()}

		before := time.Now()
		require.NoError(t, b.Prepare(chain.HeaderChain(), header))

		// Should give full 2s build time from now, not from parent
		expectedMin := before.Add(2 * time.Second).Unix()
		require.GreaterOrEqual(t, int64(header.Time), expectedMin)
		// Add upper bound check to ensure timestamp is within reasonable range (allow 100ms execution time)
		expectedMax := before.Add(2*time.Second + 100*time.Millisecond).Unix()
		require.LessOrEqual(t, int64(header.Time), expectedMax)
	})

	t.Run("on-time parent uses normal calculation", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		recentParentTime := time.Now().Unix()
		chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr1, uint64(recentParentTime))

		header := &types.Header{Number: big.NewInt(1), ParentHash: chain.HeaderChain().GetHeaderByNumber(0).Hash()}

		require.NoError(t, b.Prepare(chain.HeaderChain(), header))

		// Should use parent.Time + period
		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		require.GreaterOrEqual(t, header.Time, genesis.Time+borCfg.Period["0"])
	})

	t.Run("custom blockTime with Rio", func(t *testing.T) {
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr1, VotingPower: 1}}}
		rioCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(0),
		}

		oldParentTime := time.Now().Add(-4 * time.Second).Unix()
		chain, b := newChainAndBorForTest(t, sp, rioCfg, true, addr1, uint64(oldParentTime))
		b.blockTime = 3 * time.Second

		header := &types.Header{Number: big.NewInt(1), ParentHash: chain.HeaderChain().GetHeaderByNumber(0).Hash()}

		before := time.Now()
		require.NoError(t, b.Prepare(chain.HeaderChain(), header))

		expectedMin := before.Add(3 * time.Second).Unix()
		require.GreaterOrEqual(t, int64(header.Time), expectedMin)
		require.False(t, header.ActualTime.IsZero())
		require.GreaterOrEqual(t, header.ActualTime.Unix(), expectedMin)
	})
}

// setupFinalizeTest creates a test environment for FinalizeAndAssemble tests
func setupFinalizeTest(t *testing.T, borCfg *params.BorConfig, addr common.Address) (*core.BlockChain, *Bor, *types.Header, *state.StateDB) {
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
	chain, b := newChainAndBorForTest(t, sp, borCfg, true, addr, uint64(time.Now().Unix()))

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)

	db := rawdb.NewMemoryDatabase()
	statedb, err := state.New(genesis.Root, state.NewDatabase(triedb.NewDatabase(db, triedb.HashDefaults), nil))
	require.NoError(t, err)

	return chain, b, genesis, statedb
}

// createTestHeader creates a test header with the given parameters
func createTestHeader(genesis *types.Header, blockNum uint64, period uint64) *types.Header {
	return &types.Header{
		Number:     big.NewInt(int64(blockNum)),
		ParentHash: genesis.Hash(),
		Time:       genesis.Time + period*blockNum,
		GasLimit:   genesis.GasLimit,
	}
}

func TestFinalizeAndAssembleReturnsCommitTime(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")

	t.Run("commit time increases with state size", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(1000000),
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		// Add some state changes to increase commit time
		testAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
		for i := 0; i < 100; i++ {
			statedb.SetState(testAddr, common.BigToHash(big.NewInt(int64(i))), common.BigToHash(big.NewInt(int64(i*2))))
		}
		statedb.AddBalance(testAddr, uint256.NewInt(1000000), 0)

		header := createTestHeader(genesis, 1, borCfg.Period["0"])

		// Call FinalizeAndAssemble and ensure commit time is measured
		_, _, commitTime, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.NoError(t, err)
		require.Greater(t, commitTime, time.Duration(0), "commitTime should be positive with state changes")
	})

	t.Run("rejects withdrawals", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint: map[string]uint64{"0": 64},
			Period: map[string]uint64{"0": 2},
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		header := createTestHeader(genesis, 1, borCfg.Period["0"])

		// Try to finalize with withdrawals - should fail
		_, _, _, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{
				Transactions: nil,
				Uncles:       nil,
				Withdrawals:  []*types.Withdrawal{{Validator: 1, Address: addr1, Amount: 100}},
			},
			nil,
		)

		require.Error(t, err)
		require.ErrorIs(t, err, consensus.ErrUnexpectedWithdrawals)
	})

	t.Run("rejects withdrawals hash in header", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint: map[string]uint64{"0": 64},
			Period: map[string]uint64{"0": 2},
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		withdrawalsHash := common.Hash{0x01}
		header := createTestHeader(genesis, 1, borCfg.Period["0"])
		header.WithdrawalsHash = &withdrawalsHash

		// Try to finalize with withdrawals hash - should fail
		_, _, _, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.Error(t, err)
		require.ErrorIs(t, err, consensus.ErrUnexpectedWithdrawals)
	})

	t.Run("rejects requests hash in header", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint: map[string]uint64{"0": 64},
			Period: map[string]uint64{"0": 2},
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		requestsHash := common.Hash{0x02}
		header := createTestHeader(genesis, 1, borCfg.Period["0"])
		header.RequestsHash = &requestsHash

		// Try to finalize with requests hash - should fail
		_, _, _, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.Error(t, err)
		require.ErrorIs(t, err, consensus.ErrUnexpectedRequests)
	})

	t.Run("non-sprint block skips span check", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 16}, // Sprint of 16 blocks
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(1000000),
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		// Block 15 is NOT a sprint start (15 % 16 != 0), so span check is skipped
		header := createTestHeader(genesis, 15, borCfg.Period["0"])

		// Call FinalizeAndAssemble - should skip span check
		_, _, commitTime, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.NoError(t, err)
		require.GreaterOrEqual(t, commitTime, time.Duration(0))
	})

	t.Run("madhugiri fork processes blocks", func(t *testing.T) {
		borCfg := &params.BorConfig{
			Sprint:         map[string]uint64{"0": 64},
			Period:         map[string]uint64{"0": 2},
			MadhugiriBlock: big.NewInt(0), // Enable Madhugiri from start
			RioBlock:       big.NewInt(1000000),
		}
		chain, b, genesis, statedb := setupFinalizeTest(t, borCfg, addr1)

		header := createTestHeader(genesis, 1, borCfg.Period["0"])

		// Provide empty receipts (non-nil)
		inputReceipts := []*types.Receipt{}

		// Call FinalizeAndAssemble with Madhugiri enabled
		block, outputReceipts, commitTime, err := b.FinalizeAndAssemble(
			chain,
			header,
			statedb,
			&types.Body{Transactions: nil, Uncles: nil},
			inputReceipts,
		)

		require.NoError(t, err)
		require.NotNil(t, block)
		require.NotNil(t, outputReceipts)
		require.GreaterOrEqual(t, commitTime, time.Duration(0))
	})

	t.Run("span commit failure triggers error path", func(t *testing.T) {
		// Test line 1219: checkAndCommitSpan error
		configData := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 64},
			Period:   map[string]uint64{"0": 2},
			RioBlock: big.NewInt(math.MaxInt64), // Rio disabled
		}

		spannerWithError := &fakeSpanner{
			vals:             []*valset.Validator{{Address: addr1, VotingPower: 100}},
			shouldFailCommit: true,
			spanEndBlock:     127, // EndBlock - 64 + 1 == 64 => EndBlock == 127 to trigger needToCommitSpan
			spanID:           1,   // Use Id: 1 to avoid the 0th span skip logic
		}
		blockchain, borInstance := newChainAndBorForTest(t, spannerWithError, configData, true, addr1, uint64(time.Now().Unix()))

		genesisHdr := blockchain.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesisHdr)

		memDB := rawdb.NewMemoryDatabase()
		stateDatabase, initErr := state.New(genesisHdr.Root, state.NewDatabase(triedb.NewDatabase(memDB, triedb.HashDefaults), nil))
		require.NoError(t, initErr)

		// Block 64 is sprint start (64 % 64 == 0)
		testHeader := createTestHeader(genesisHdr, 64, configData.Period["0"])

		// FinalizeAndAssemble should fail due to span commit error
		_, _, _, finalizeErr := borInstance.FinalizeAndAssemble(
			blockchain,
			testHeader,
			stateDatabase,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.Error(t, finalizeErr)
		require.Contains(t, finalizeErr.Error(), "span commit failed")
	})

	t.Run("state sync commit failure returns error", func(t *testing.T) {
		// Test line 1228: CommitStates error
		cfg := &params.BorConfig{
			Sprint:   map[string]uint64{"0": 16},
			Period:   map[string]uint64{"0": 1},
			RioBlock: big.NewInt(math.MaxInt64),
		}

		validatorAddr := common.HexToAddress("0x9")
		spannerObj := &fakeSpanner{vals: []*valset.Validator{{Address: validatorAddr, VotingPower: 100}}}

		// Create Bor with failing genesis contract
		ch, borEngine := newChainAndBorForTest(t, spannerObj, cfg, true, validatorAddr, uint64(time.Now().Unix()))
		borEngine.HeimdallClient = &failingHeimdallClient{}
		borEngine.GenesisContractsClient = &failingGenesisContract{}

		genesisBlock := ch.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesisBlock)

		database := rawdb.NewMemoryDatabase()
		stateObj, stateErr := state.New(genesisBlock.Root, state.NewDatabase(triedb.NewDatabase(database, triedb.HashDefaults), nil))
		require.NoError(t, stateErr)

		// Block 16 is sprint start
		hdr := createTestHeader(genesisBlock, 16, cfg.Period["0"])

		// Should fail during CommitStates when calling LastStateId
		_, _, _, executionErr := borEngine.FinalizeAndAssemble(
			ch,
			hdr,
			stateObj,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.Error(t, executionErr)
		require.Contains(t, executionErr.Error(), "last state id failed")
	})

	t.Run("contract code change failure halts finalization", func(t *testing.T) {
		// Test line 1235: changeContractCodeIfNeeded error
		borConfiguration := &params.BorConfig{
			Sprint: map[string]uint64{"0": 64},
			Period: map[string]uint64{"0": 2},
			BlockAlloc: map[string]interface{}{
				"5": "invalid-json-data", // This will cause decode error
			},
			RioBlock: big.NewInt(math.MaxInt64),
		}

		accountAddr := common.HexToAddress("0xBEEF")
		sp := &fakeSpanner{vals: []*valset.Validator{{Address: accountAddr, VotingPower: 50}}}
		blockchainObj, borObj := newChainAndBorForTest(t, sp, borConfiguration, true, accountAddr, uint64(time.Now().Unix()))

		genesisHeader := blockchainObj.HeaderChain().GetHeaderByNumber(0)
		require.NotNil(t, genesisHeader)

		db := rawdb.NewMemoryDatabase()
		stateDatabase, dbErr := state.New(genesisHeader.Root, state.NewDatabase(triedb.NewDatabase(db, triedb.HashDefaults), nil))
		require.NoError(t, dbErr)

		// Block 5 has invalid BlockAlloc which triggers decode error
		headerObj := createTestHeader(genesisHeader, 5, borConfiguration.Period["0"])

		// FinalizeAndAssemble should fail during changeContractCodeIfNeeded
		_, _, _, processingErr := borObj.FinalizeAndAssemble(
			blockchainObj,
			headerObj,
			stateDatabase,
			&types.Body{Transactions: nil, Uncles: nil},
			nil,
		)

		require.Error(t, processingErr)
		require.Contains(t, processingErr.Error(), "failed to decode genesis alloc")
	})
}
