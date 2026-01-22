package bor

import (
	"crypto/ecdsa"
	"math/big"
	"sort"
	"testing"

	"github.com/0xPolygon/crand"
	lru "github.com/hashicorp/golang-lru"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/ethereum/go-ethereum/common"
	unique "github.com/ethereum/go-ethereum/common/set"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const (
	numVals = 100
)

func TestGetSignerSuccessionNumber_ProposerIsSigner(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	validatorSet := valset.NewValidatorSet(validators)
	snap := Snapshot{
		ValidatorSet: validatorSet,
	}

	// proposer is signer
	signerTest := validatorSet.Proposer.Address

	successionNumber, err := snap.GetSignerSuccessionNumber(signerTest)
	if err != nil {
		t.Fatalf("%s", err)
	}

	require.Equal(t, 0, successionNumber)
}

func TestGetSignerSuccessionNumber_SignerIndexIsLarger(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)

	// sort validators by address, which is what NewValidatorSet also does
	sort.Sort(valset.ValidatorsByAddress(validators))

	proposerIndex := 32
	signerIndex := 56
	// give highest ProposerPriority to a particular val, so that they become the proposer
	validators[proposerIndex].VotingPower = 200
	snap := Snapshot{
		ValidatorSet: valset.NewValidatorSet(validators),
	}

	// choose a signer at an index greater than proposer index
	signerTest := snap.ValidatorSet.Validators[signerIndex].Address

	successionNumber, err := snap.GetSignerSuccessionNumber(signerTest)
	if err != nil {
		t.Fatalf("%s", err)
	}

	require.Equal(t, signerIndex-proposerIndex, successionNumber)
}

func TestGetSignerSuccessionNumber_SignerIndexIsSmaller(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	proposerIndex := 98
	signerIndex := 11
	// give highest ProposerPriority to a particular val, so that they become the proposer
	validators[proposerIndex].VotingPower = 200
	snap := Snapshot{
		ValidatorSet: valset.NewValidatorSet(validators),
	}

	// choose a signer at an index greater than proposer index
	signerTest := snap.ValidatorSet.Validators[signerIndex].Address

	successionNumber, err := snap.GetSignerSuccessionNumber(signerTest)
	if err != nil {
		t.Fatalf("%s", err)
	}

	require.Equal(t, signerIndex+numVals-proposerIndex, successionNumber)
}

func TestGetSignerSuccessionNumber_ProposerNotFound(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	snap := Snapshot{
		ValidatorSet: valset.NewValidatorSet(validators),
	}

	require.Len(t, snap.ValidatorSet.Validators, numVals)

	dummyProposerAddress := randomAddress(toAddresses(validators)...)
	snap.ValidatorSet.Proposer = &valset.Validator{Address: dummyProposerAddress}

	// choose any signer
	signerTest := snap.ValidatorSet.Validators[3].Address

	_, err := snap.GetSignerSuccessionNumber(signerTest)
	assertUnauthorizedProposerError(t, err, dummyProposerAddress)
}

func TestGetSignerSuccessionNumber_SignerNotFound(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	snap := Snapshot{
		ValidatorSet: valset.NewValidatorSet(validators),
	}

	dummySignerAddress := randomAddress(toAddresses(validators)...)
	_, err := snap.GetSignerSuccessionNumber(dummySignerAddress)
	assertUnauthorizedSignerError(t, err, 0, dummySignerAddress)
}

func TestGetSignerSuccessionNumber_WithValidatorOverride(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	validatorSet := valset.NewValidatorSet(validators)
	overrideValidator := randomAddress(toAddresses(validators)...)

	tests := []struct {
		name      string
		block     uint64
		expectErr bool
	}{
		{"within range", 150, false},
		{"outside range", 250, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := Snapshot{
				ValidatorSet: validatorSet,
				Number:       tt.block,
				chainConfig: &params.ChainConfig{
					Bor: &params.BorConfig{
						OverrideValidatorSetInRange: []params.BlockRangeOverrideValidatorSet{
							{
								StartBlock: 100,
								EndBlock:   200,
								Validators: []common.Address{overrideValidator},
							},
						},
					},
				},
			}

			succession, err := snap.GetSignerSuccessionNumber(overrideValidator)
			if tt.expectErr {
				assertUnauthorizedSignerError(t, err, tt.block, overrideValidator)
			} else {
				require.NoError(t, err)
				require.GreaterOrEqual(t, succession, -1)
			}
		})
	}
}

func TestIsAllowedByValidatorSetOverride_EdgeCases(t *testing.T) {
	testAddr := addr("0x1")

	tests := []struct {
		name string
		snap *Snapshot
	}{
		{"no config", &Snapshot{Number: 100}},
		{"empty overrides", newSnapshotWithOverrides(nil, 100)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := tt.snap.isAllowedByValidatorSetOverride(testAddr, tt.snap.Number)
			require.False(t, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_SingleRange(t *testing.T) {
	overrideAddr := addr("0x41018795fA95783117242244303fd7e26e964eE8")
	otherAddr := addr("0x000000000000000000000000000000000000dead")

	overrides := []params.BlockRangeOverrideValidatorSet{
		{
			StartBlock: 100,
			EndBlock:   200,
			Validators: []common.Address{overrideAddr},
		},
	}

	tests := []struct {
		name      string
		validator common.Address
		block     uint64
		expected  bool
	}{
		{"allowed at start boundary", overrideAddr, 100, true},
		{"allowed in middle", overrideAddr, 150, true},
		{"allowed at end boundary", overrideAddr, 200, true},
		{"not allowed before start", overrideAddr, 99, false},
		{"not allowed after end", overrideAddr, 201, false},
		{"not allowed - wrong validator", otherAddr, 150, false},
		{"not allowed - outside range", overrideAddr, 250, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := newSnapshotWithOverrides(overrides, tt.block)
			ok := snap.isAllowedByValidatorSetOverride(tt.validator, tt.block)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_MultipleValidators(t *testing.T) {
	validator1 := addr("0x1111111111111111111111111111111111111111")
	validator2 := addr("0x2222222222222222222222222222222222222222")
	validator3 := addr("0x3333333333333333333333333333333333333333")

	snap := newSnapshotWithOverrides(
		[]params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 100,
				EndBlock:   200,
				Validators: []common.Address{validator1, validator2},
			},
		},
		150,
	)

	tests := []struct {
		name      string
		validator common.Address
		expected  bool
	}{
		{"validator1 allowed", validator1, true},
		{"validator2 allowed", validator2, true},
		{"validator3 not in list", validator3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := snap.isAllowedByValidatorSetOverride(tt.validator, snap.Number)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_MultipleRanges(t *testing.T) {
	validator1 := addr("0x1111111111111111111111111111111111111111")
	validator2 := addr("0x2222222222222222222222222222222222222222")

	snap := newSnapshotWithOverrides(
		[]params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 100,
				EndBlock:   200,
				Validators: []common.Address{validator1},
			},
			{
				StartBlock: 300,
				EndBlock:   400,
				Validators: []common.Address{validator2},
			},
		},
		150,
	)

	tests := []struct {
		name      string
		validator common.Address
		block     uint64
		expected  bool
	}{
		{"validator1 in first range", validator1, 150, true},
		{"validator1 not in second range", validator1, 350, false},
		{"validator2 not in first range", validator2, 150, false},
		{"validator2 in second range", validator2, 350, true},
		{"validator1 outside both ranges", validator1, 250, false},
		{"validator2 outside both ranges", validator2, 250, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := snap.isAllowedByValidatorSetOverride(tt.validator, tt.block)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_RangeScenarios(t *testing.T) {
	t.Parallel()

	validator := addr("0x1111111111111111111111111111111111111111")
	mainnetValidator := addr("0x41018795fa95783117242244303fd7e26e964ee8")

	tests := []struct {
		name      string
		validator common.Address
		overrides []params.BlockRangeOverrideValidatorSet
		testCases []struct {
			block    uint64
			expected bool
		}
	}{
		{
			name:      "single block range",
			validator: validator,
			overrides: []params.BlockRangeOverrideValidatorSet{
				{StartBlock: 100, EndBlock: 100, Validators: []common.Address{validator}},
			},
			testCases: []struct {
				block    uint64
				expected bool
			}{
				{99, false},
				{100, true},
				{101, false},
			},
		},
		{
			name:      "real world mainnet scenario",
			validator: mainnetValidator,
			overrides: []params.BlockRangeOverrideValidatorSet{
				{StartBlock: 80440819, EndBlock: 80440834, Validators: []common.Address{mainnetValidator}},
			},
			testCases: []struct {
				block    uint64
				expected bool
			}{
				{80440818, false},
				{80440819, true},
				{80440826, true},
				{80440834, true},
				{80440835, false},
			},
		},
		{
			name:      "consecutive ranges",
			validator: validator,
			overrides: []params.BlockRangeOverrideValidatorSet{
				{StartBlock: 100, EndBlock: 200, Validators: []common.Address{validator}},
				{StartBlock: 201, EndBlock: 300, Validators: []common.Address{validator}},
			},
			testCases: []struct {
				block    uint64
				expected bool
			}{
				{100, true},
				{200, true},
				{201, true},
				{300, true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, tc := range tt.testCases {
				snap := newSnapshotWithOverrides(tt.overrides, tc.block)
				ok := snap.isAllowedByValidatorSetOverride(tt.validator, tc.block)
				require.Equal(t, tc.expected, ok, "block %d", tc.block)
			}
		})
	}
}

func TestIsAllowedByValidatorSetOverride_OverlappingRanges(t *testing.T) {
	// IMPORTANT: This test documents the current behavior with overlapping ranges.
	// The implementation returns false after checking the FIRST matching range
	// if the validator is not in that range's list. This means only the first
	// matching range is effective for overlapping ranges.
	validator1 := addr("0x1111111111111111111111111111111111111111")
	validator2 := addr("0x2222222222222222222222222222222222222222")

	snap := newSnapshotWithOverrides(
		[]params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 100,
				EndBlock:   200,
				Validators: []common.Address{validator1},
			},
			{
				StartBlock: 150, // Overlapping
				EndBlock:   250,
				Validators: []common.Address{validator2},
			},
		},
		175,
	)

	tests := []struct {
		name      string
		validator common.Address
		block     uint64
		expected  bool
	}{
		{"validator1 in overlap - first range wins", validator1, 175, true},
		{"validator2 in overlap - first range blocks", validator2, 175, false},
		{"validator2 after first range ends", validator2, 225, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := snap.isAllowedByValidatorSetOverride(tt.validator, tt.block)
			require.Equal(t, tt.expected, ok)
		})
	}
}

// nolint:unparam
func buildRandomValidatorSet(numVals int) []*valset.Validator {
	validators := make([]*valset.Validator, numVals)
	valAddrs := randomAddresses(numVals)

	for i := 0; i < numVals; i++ {
		power := crand.BigInt(big.NewInt(99))
		powerN := power.Int64() + 1

		validators[i] = &valset.Validator{
			Address: valAddrs[i],
			// cannot process validators with voting power 0, hence +1
			VotingPower: powerN,
		}
	}

	// sort validators by address, which is what NewValidatorSet also does
	sort.Sort(valset.ValidatorsByAddress(validators))

	return validators
}

func randomAddress(exclude ...common.Address) common.Address {
	excl := make(map[common.Address]struct{}, len(exclude))

	for _, addr := range exclude {
		excl[addr] = struct{}{}
	}

	r := crand.NewRand()

	for {
		addr := r.Address()
		if _, ok := excl[addr]; ok {
			continue
		}

		return addr
	}
}

func randomAddresses(n int) []common.Address {
	if n <= 0 {
		return []common.Address{}
	}

	addrs := make([]common.Address, 0, n)
	addrsSet := make(map[common.Address]struct{}, n)

	var exist bool

	r := crand.NewRand()

	for {
		addr := r.Address()

		_, exist = addrsSet[addr]
		if !exist {
			addrs = append(addrs, addr)

			addrsSet[addr] = struct{}{}
		}

		if len(addrs) == n {
			return addrs
		}
	}
}

func TestRandomAddresses(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		length := rapid.IntMax(300).AsAny().Draw(t, "length").(int)

		addrs := randomAddresses(length)
		addressSet := unique.New(addrs)

		if len(addrs) != len(addressSet) {
			t.Fatalf("length of unique addresses %d, expected %d", len(addressSet), len(addrs))
		}
	})
}

func toAddresses(vals []*valset.Validator) []common.Address {
	addrs := make([]common.Address, len(vals))

	for i, val := range vals {
		addrs[i] = val.Address
	}

	return addrs
}

func addr(hex string) common.Address {
	return common.HexToAddress(hex)
}

func newSnapshotWithOverrides(overrides []params.BlockRangeOverrideValidatorSet, block uint64) *Snapshot {
	return &Snapshot{
		Number: block,
		chainConfig: &params.ChainConfig{
			Bor: &params.BorConfig{
				OverrideValidatorSetInRange: overrides,
			},
		},
	}
}

// applyTestSetup contains all setup for Apply tests
type applyTestSetup struct {
	privKey      *ecdsa.PrivateKey
	signer       common.Address
	validatorSet *valset.ValidatorSet
	snapshot     *Snapshot
	borConfig    *params.BorConfig
	bor          *Bor
}

// setupApplyTest creates a complete test setup for Snapshot.apply tests
func setupApplyTest(t *testing.T, snapshotBlock uint64, overrideRange *params.BlockRangeOverrideValidatorSet) *applyTestSetup {
	t.Helper()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	signer := crypto.PubkeyToAddress(privKey.PublicKey)
	normalValidators := buildRandomValidatorSet(5)
	validatorSet := valset.NewValidatorSet(normalValidators)

	sigcache, err := lru.NewARC(10)
	require.NoError(t, err)

	borConfig := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 1},
	}

	if overrideRange != nil {
		borConfig.OverrideValidatorSetInRange = []params.BlockRangeOverrideValidatorSet{*overrideRange}
	}

	snap := &Snapshot{
		Number:       snapshotBlock,
		Hash:         common.Hash{},
		ValidatorSet: validatorSet,
		Recents:      make(map[uint64]common.Address),
		sigcache:     sigcache,
		chainConfig: &params.ChainConfig{
			Bor: borConfig,
		},
	}

	bor := &Bor{
		config: borConfig,
	}

	return &applyTestSetup{
		privKey:      privKey,
		signer:       signer,
		validatorSet: validatorSet,
		snapshot:     snap,
		borConfig:    borConfig,
		bor:          bor,
	}
}

// createSignedHeader creates and signs a header for testing
func createSignedHeader(t *testing.T, blockNum uint64, privKey *ecdsa.PrivateKey, config *params.BorConfig) *types.Header {
	t.Helper()

	header := &types.Header{
		Number:     big.NewInt(int64(blockNum)),
		Time:       blockNum,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65),
	}

	sigHash := SealHash(header, config)
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-65:], sig)

	return header
}

// assertUnauthorizedSignerError checks if error is UnauthorizedSignerError with expected values
func assertUnauthorizedSignerError(t *testing.T, err error, expectedNumber uint64, expectedSigner common.Address) {
	t.Helper()
	require.Error(t, err)
	var authErr *UnauthorizedSignerError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, expectedNumber, authErr.Number)
	require.Equal(t, expectedSigner.Bytes(), authErr.Signer)
}

// assertUnauthorizedProposerError checks if error is UnauthorizedProposerError with expected values
func assertUnauthorizedProposerError(t *testing.T, err error, expectedProposer common.Address) {
	t.Helper()
	require.Error(t, err)
	var authErr *UnauthorizedProposerError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, expectedProposer.Bytes(), authErr.Proposer)
}

func TestSnapshot_Apply_WithValidatorOverride(t *testing.T) {
	t.Parallel()

	// First create setup without override to get the signer address
	setup := setupApplyTest(t, 0, nil)

	// Now update the config with the override using the actual signer
	override := &params.BlockRangeOverrideValidatorSet{
		StartBlock: 0,
		EndBlock:   10,
		Validators: []common.Address{setup.signer},
	}
	setup.borConfig.OverrideValidatorSetInRange = []params.BlockRangeOverrideValidatorSet{*override}
	setup.snapshot.chainConfig.Bor = setup.borConfig

	header := createSignedHeader(t, 1, setup.privKey, setup.borConfig)

	// Apply the header - this should succeed because the override validator is allowed
	// This tests snapshot.go lines 139-140:
	// if !snap.ValidatorSet.HasAddress(signer) && !snap.isAllowedByValidatorSetOverride(signer, number) {
	//     return nil, &UnauthorizedSignerError{number, signer.Bytes(), snap.ValidatorSet.Validators}
	// }
	newSnap, err := setup.snapshot.apply([]*types.Header{header}, setup.bor)
	require.NoError(t, err)
	require.NotNil(t, newSnap)
	require.Equal(t, uint64(1), newSnap.Number)
	require.Equal(t, setup.signer, newSnap.Recents[1])
}

func TestSnapshot_Apply_WithValidatorOverride_OutsideRange(t *testing.T) {
	t.Parallel()

	// Create setup with override range 1-10, but snapshot at block 10
	setup := setupApplyTest(t, 10, nil)

	override := &params.BlockRangeOverrideValidatorSet{
		StartBlock: 1,
		EndBlock:   10,
		Validators: []common.Address{setup.signer},
	}
	setup.borConfig.OverrideValidatorSetInRange = []params.BlockRangeOverrideValidatorSet{*override}
	setup.snapshot.chainConfig.Bor = setup.borConfig

	// Create a header at block 11 (outside the override range)
	header := createSignedHeader(t, 11, setup.privKey, setup.borConfig)

	// Apply the header - this should FAIL because block 11 is outside the override range
	// This tests that the override check at snapshot.go:139 properly rejects
	newSnap, err := setup.snapshot.apply([]*types.Header{header}, setup.bor)
	require.Nil(t, newSnap)
	assertUnauthorizedSignerError(t, err, 11, setup.signer)
}

func TestSnapshot_Apply_UnauthorizedSigner(t *testing.T) {
	t.Parallel()

	// Create setup with NO override configuration
	setup := setupApplyTest(t, 0, nil)

	// Ensure the signer is not in the validator set
	for _, v := range setup.validatorSet.Validators {
		require.NotEqual(t, setup.signer, v.Address)
	}

	// Create a header at block 1 signed by the unauthorized signer
	header := createSignedHeader(t, 1, setup.privKey, setup.borConfig)

	// Apply the header - this should FAIL at snapshot.go:140
	// Because:
	// 1. !snap.ValidatorSet.HasAddress(signer) = true (not in validator set)
	// 2. !snap.isAllowedByValidatorSetOverride(signer, number) = true (no override config)
	// Therefore the condition at line 139 is true and line 140 executes
	newSnap, err := setup.snapshot.apply([]*types.Header{header}, setup.bor)
	require.Nil(t, newSnap)

	// Verify it's an UnauthorizedSignerError from line 140
	var authErr *UnauthorizedSignerError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, uint64(1), authErr.Number)
	require.Equal(t, setup.signer.Bytes(), authErr.Signer)
	require.Equal(t, setup.validatorSet.Validators, authErr.AllowedSigners)
}
