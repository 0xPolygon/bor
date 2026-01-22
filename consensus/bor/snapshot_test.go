package bor

import (
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
	require.NotNil(t, err)

	e, ok := err.(*UnauthorizedProposerError)
	require.True(t, ok)
	require.Equal(t, dummyProposerAddress.Bytes(), e.Proposer)
}

func TestGetSignerSuccessionNumber_SignerNotFound(t *testing.T) {
	t.Parallel()

	validators := buildRandomValidatorSet(numVals)
	snap := Snapshot{
		ValidatorSet: valset.NewValidatorSet(validators),
	}

	dummySignerAddress := randomAddress(toAddresses(validators)...)
	_, err := snap.GetSignerSuccessionNumber(dummySignerAddress)
	require.NotNil(t, err)

	e, ok := err.(*UnauthorizedSignerError)
	require.True(t, ok)

	require.Equal(t, dummySignerAddress.Bytes(), e.Signer)
}

func TestGetSignerSuccessionNumber_WithValidatorOverride(t *testing.T) {
	t.Parallel()

	// Create normal validator set
	validators := buildRandomValidatorSet(numVals)
	validatorSet := valset.NewValidatorSet(validators)

	// Override validator that is NOT in the normal validator set
	overrideValidator := randomAddress(toAddresses(validators)...)

	snap := Snapshot{
		ValidatorSet: validatorSet,
		Number:       150,
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

	// Test that the override validator can get succession number
	// This tests the critical path in GetSignerSuccessionNumber where:
	// - signerIndex == -1 (not in normal validator set)
	// - isAllowedByValidatorSetOverride returns true
	// - tempIndex is set to -1 (snapshot.go:198)
	// - succession number is calculated with tempIndex = -1 (snapshot.go:199-205)
	succession, err := snap.GetSignerSuccessionNumber(overrideValidator)
	require.NoError(t, err)
	// When signerIndex is -1 (not in normal set), tempIndex is -1
	// succession = tempIndex - proposerIndex = -1 - proposerIndex
	// Since proposerIndex is always >= 0, succession will be negative or at boundary
	require.GreaterOrEqual(t, succession, -1)
}

func TestGetSignerSuccessionNumber_WithValidatorOverride_OutsideRange(t *testing.T) {
	t.Parallel()

	// Create normal validator set
	validators := buildRandomValidatorSet(numVals)
	validatorSet := valset.NewValidatorSet(validators)

	// Override validator that is NOT in the normal validator set
	overrideValidator := randomAddress(toAddresses(validators)...)

	snap := Snapshot{
		ValidatorSet: validatorSet,
		Number:       250, // Outside the override range
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

	// Test that the override validator is rejected outside the range
	_, err := snap.GetSignerSuccessionNumber(overrideValidator)
	require.NotNil(t, err)

	e, ok := err.(*UnauthorizedSignerError)
	require.True(t, ok)
	require.Equal(t, overrideValidator.Bytes(), e.Signer)
}

func TestIsAllowedByValidatorSetOverride_NoConfig(t *testing.T) {
	snap := &Snapshot{
		Number: 100,
	}

	ok := snap.isAllowedByValidatorSetOverride(addr("0x1"), snap.Number)
	require.False(t, ok)
}

func TestIsAllowedByValidatorSetOverride_EmptyOverrides(t *testing.T) {
	snap := newSnapshotWithOverrides(nil, 100)

	ok := snap.isAllowedByValidatorSetOverride(addr("0x1"), snap.Number)
	require.False(t, ok)
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

func TestIsAllowedByValidatorSetOverride_SingleBlockRange(t *testing.T) {
	validator := addr("0x1111111111111111111111111111111111111111")

	overrides := []params.BlockRangeOverrideValidatorSet{
		{
			StartBlock: 100,
			EndBlock:   100, // Single block
			Validators: []common.Address{validator},
		},
	}

	tests := []struct {
		name     string
		block    uint64
		expected bool
	}{
		{"allowed at single block", 100, true},
		{"not allowed before", 99, false},
		{"not allowed after", 101, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := newSnapshotWithOverrides(overrides, tt.block)
			ok := snap.isAllowedByValidatorSetOverride(validator, snap.Number)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_RealWorldScenario(t *testing.T) {
	// Simulate the actual mainnet override scenario
	overrideValidator := addr("0x41018795fa95783117242244303fd7e26e964ee8")

	snap := newSnapshotWithOverrides(
		[]params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 80440819,
				EndBlock:   80440834,
				Validators: []common.Address{overrideValidator},
			},
		},
		80440819,
	)

	tests := []struct {
		name     string
		block    uint64
		expected bool
	}{
		{"allowed at start block", 80440819, true},
		{"allowed in middle of range", 80440826, true},
		{"allowed at end block", 80440834, true},
		{"not allowed before start", 80440818, false},
		{"not allowed after end", 80440835, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := snap.isAllowedByValidatorSetOverride(overrideValidator, tt.block)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestIsAllowedByValidatorSetOverride_ConsecutiveRanges(t *testing.T) {
	validator := addr("0x1111111111111111111111111111111111111111")

	snap := newSnapshotWithOverrides(
		[]params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 100,
				EndBlock:   200,
				Validators: []common.Address{validator},
			},
			{
				StartBlock: 201, // Consecutive
				EndBlock:   300,
				Validators: []common.Address{validator},
			},
		},
		100,
	)

	tests := []struct {
		name  string
		block uint64
	}{
		{"first range start", 100},
		{"first range end", 200},
		{"second range start", 201},
		{"second range end", 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := snap.isAllowedByValidatorSetOverride(validator, tt.block)
			require.True(t, ok)
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

func TestSnapshot_Apply_WithValidatorOverride(t *testing.T) {
	t.Parallel()

	// Create a private key for signing
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	overrideValidator := crypto.PubkeyToAddress(privKey.PublicKey)

	// Create normal validators (without the override validator)
	normalValidators := buildRandomValidatorSet(5)
	validatorSet := valset.NewValidatorSet(normalValidators)

	// Create signature cache
	sigcache, err := lru.NewARC(10)
	require.NoError(t, err)

	borConfig := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 1},
		OverrideValidatorSetInRange: []params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 0,
				EndBlock:   10,
				Validators: []common.Address{overrideValidator},
			},
		},
	}

	// Create initial snapshot at block 0
	// Note: GetSignerSuccessionNumber uses s.Number for the override check,
	// so the snapshot's block number needs to be within the override range
	snap := &Snapshot{
		Number:       0,
		Hash:         common.Hash{},
		ValidatorSet: validatorSet,
		Recents:      make(map[uint64]common.Address),
		sigcache:     sigcache,
		chainConfig: &params.ChainConfig{
			Bor: borConfig,
		},
	}

	// Create a header at block 1 signed by the override validator
	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       1,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65), // 32 bytes vanity + 65 bytes signature
	}

	// Sign the header
	sigHash := SealHash(header, borConfig)
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-65:], sig)

	// Create a mock Bor instance (minimal, just for the apply function)
	bor := &Bor{
		config: borConfig,
	}

	// Apply the header - this should succeed because the override validator is allowed
	// This tests snapshot.go lines 139-140:
	// if !snap.ValidatorSet.HasAddress(signer) && !snap.isAllowedByValidatorSetOverride(signer, number) {
	//     return nil, &UnauthorizedSignerError{number, signer.Bytes(), snap.ValidatorSet.Validators}
	// }
	newSnap, err := snap.apply([]*types.Header{header}, bor)
	require.NoError(t, err)
	require.NotNil(t, newSnap)
	require.Equal(t, uint64(1), newSnap.Number)
	require.Equal(t, overrideValidator, newSnap.Recents[1])
}

func TestSnapshot_Apply_WithValidatorOverride_OutsideRange(t *testing.T) {
	t.Parallel()

	// Create a private key for signing
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	overrideValidator := crypto.PubkeyToAddress(privKey.PublicKey)

	// Create normal validators (without the override validator)
	normalValidators := buildRandomValidatorSet(5)
	validatorSet := valset.NewValidatorSet(normalValidators)

	// Create signature cache
	sigcache, err := lru.NewARC(10)
	require.NoError(t, err)

	borConfig := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 1},
		OverrideValidatorSetInRange: []params.BlockRangeOverrideValidatorSet{
			{
				StartBlock: 1,
				EndBlock:   10,
				Validators: []common.Address{overrideValidator},
			},
		},
	}

	// Create initial snapshot at block 10
	snap := &Snapshot{
		Number:       10,
		Hash:         common.Hash{},
		ValidatorSet: validatorSet,
		Recents:      make(map[uint64]common.Address),
		sigcache:     sigcache,
		chainConfig: &params.ChainConfig{
			Bor: borConfig,
		},
	}

	// Create a header at block 11 (outside the override range) signed by the override validator
	header := &types.Header{
		Number:     big.NewInt(11),
		Time:       11,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65), // 32 bytes vanity + 65 bytes signature
	}

	// Sign the header
	sigHash := SealHash(header, borConfig)
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-65:], sig)

	// Create a mock Bor instance
	bor := &Bor{
		config: borConfig,
	}

	// Apply the header - this should FAIL because block 11 is outside the override range
	// This tests that the override check at snapshot.go:139 properly rejects
	newSnap, err := snap.apply([]*types.Header{header}, bor)
	require.Error(t, err)
	require.Nil(t, newSnap)

	// Verify it's an UnauthorizedSignerError
	var authErr *UnauthorizedSignerError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, uint64(11), authErr.Number)
	require.Equal(t, overrideValidator.Bytes(), authErr.Signer)
}

func TestSnapshot_Apply_UnauthorizedSigner(t *testing.T) {
	t.Parallel()

	// Create a private key for an unauthorized signer
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	unauthorizedSigner := crypto.PubkeyToAddress(privKey.PublicKey)

	// Create normal validators (WITHOUT the unauthorized signer)
	normalValidators := buildRandomValidatorSet(5)
	validatorSet := valset.NewValidatorSet(normalValidators)

	// Ensure the unauthorized signer is not in the validator set
	for _, v := range normalValidators {
		require.NotEqual(t, unauthorizedSigner, v.Address)
	}

	// Create signature cache
	sigcache, err := lru.NewARC(10)
	require.NoError(t, err)

	borConfig := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 1},
		// NO override configuration - this is key!
		OverrideValidatorSetInRange: nil,
	}

	// Create initial snapshot at block 0
	snap := &Snapshot{
		Number:       0,
		Hash:         common.Hash{},
		ValidatorSet: validatorSet,
		Recents:      make(map[uint64]common.Address),
		sigcache:     sigcache,
		chainConfig: &params.ChainConfig{
			Bor: borConfig,
		},
	}

	// Create a header at block 1 signed by the unauthorized signer
	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       1,
		Difficulty: big.NewInt(1),
		Extra:      make([]byte, 32+65), // 32 bytes vanity + 65 bytes signature
	}

	// Sign the header
	sigHash := SealHash(header, borConfig)
	sig, err := crypto.Sign(sigHash.Bytes(), privKey)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-65:], sig)

	// Create a mock Bor instance
	bor := &Bor{
		config: borConfig,
	}

	// Apply the header - this should FAIL at snapshot.go:140
	// Because:
	// 1. !snap.ValidatorSet.HasAddress(signer) = true (not in validator set)
	// 2. !snap.isAllowedByValidatorSetOverride(signer, number) = true (no override config)
	// Therefore the condition at line 139 is true and line 140 executes
	newSnap, err := snap.apply([]*types.Header{header}, bor)
	require.Error(t, err)
	require.Nil(t, newSnap)

	// Verify it's an UnauthorizedSignerError from line 140
	var authErr *UnauthorizedSignerError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, uint64(1), authErr.Number)
	require.Equal(t, unauthorizedSigner.Bytes(), authErr.Signer)
	require.Equal(t, validatorSet.Validators, authErr.AllowedSigners)
}
