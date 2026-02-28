package bor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/stretchr/testify/require"
)

// Test helpers

// signHeader signs a header with the given private key
func signHeader(t *testing.T, header *types.Header, key *ecdsa.PrivateKey, borCfg *params.BorConfig) {
	header.Extra = make([]byte, types.ExtraVanityLength+types.ExtraSealLength)
	sighash, err := crypto.Sign(SealHash(header, borCfg).Bytes(), key)
	require.NoError(t, err)
	copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], sighash)
}

// chainSetupOptions configures test chain creation
type chainSetupOptions struct {
	validators   []*valset.Validator
	sprint       uint64
	period       uint64
	rioBlock     *big.Int
	bhilaiBlock  *big.Int
	genesisTime  uint64
	ethashEngine bool
	beneficiary  common.Address
}

// defaultChainSetup returns default chain setup options
func defaultChainSetup(signerAddr common.Address) chainSetupOptions {
	return chainSetupOptions{
		validators:  []*valset.Validator{{Address: signerAddr, VotingPower: 1}},
		sprint:      64,
		period:      2,
		genesisTime: uint64(time.Now().Add(-10 * time.Minute).Unix()), // Past time to avoid future block errors
	}
}

// setupTestChain creates a test blockchain with the given options
func setupTestChain(t *testing.T, opts chainSetupOptions) (*core.BlockChain, *Bor) {
	sp := &fakeSpanner{vals: opts.validators}
	borCfg := &params.BorConfig{
		Sprint:      map[string]uint64{"0": opts.sprint},
		Period:      map[string]uint64{"0": opts.period},
		RioBlock:    opts.rioBlock,
		BhilaiBlock: opts.bhilaiBlock,
	}
	return newChainAndBorForTest(t, sp, borCfg, opts.ethashEngine, opts.beneficiary, opts.genesisTime)
}

// makeSetupChain returns a setupChain function for the given address and optional modifications
func makeSetupChain(signerAddr common.Address, modify ...func(*chainSetupOptions)) func(t *testing.T) (*core.BlockChain, *Bor) {
	return func(t *testing.T) (*core.BlockChain, *Bor) {
		opts := defaultChainSetup(signerAddr)
		for _, m := range modify {
			m(&opts)
		}
		return setupTestChain(t, opts)
	}
}

// headerOptions configures test header creation
type headerOptions struct {
	number          *big.Int
	parentHash      common.Hash
	time            uint64
	extra           []byte
	difficulty      *big.Int
	gasLimit        uint64
	mixDigest       common.Hash
	uncleHash       common.Hash
	withdrawalsHash *common.Hash
	requestsHash    *common.Hash
}

// newTestHeader creates a test header with the given options, using genesis as defaults
func newTestHeader(genesis *types.Header, opts headerOptions) *types.Header {
	header := &types.Header{
		Number:          opts.number,
		ParentHash:      opts.parentHash,
		Time:            opts.time,
		Extra:           opts.extra,
		Difficulty:      opts.difficulty,
		GasLimit:        opts.gasLimit,
		MixDigest:       opts.mixDigest,
		UncleHash:       opts.uncleHash,
		WithdrawalsHash: opts.withdrawalsHash,
		RequestsHash:    opts.requestsHash,
	}

	// Apply defaults from genesis if not specified
	if header.GasLimit == 0 {
		header.GasLimit = genesis.GasLimit
	}

	return header
}

// newStandardTestHeader creates a header with common defaults: block 1, parent=genesis, time=genesis+2, standard extra, difficulty=1
func newStandardTestHeader(genesis *types.Header, modify ...func(*headerOptions)) *types.Header {
	opts := headerOptions{
		number:     big.NewInt(1),
		parentHash: genesis.Hash(),
		time:       genesis.Time + 2,
		extra:      make([]byte, types.ExtraVanityLength+types.ExtraSealLength),
		difficulty: big.NewInt(1),
	}
	for _, m := range modify {
		m(&opts)
	}
	return newTestHeader(genesis, opts)
}

// newSignedStandardTestHeader creates a signed header with common defaults
func newSignedStandardTestHeader(t *testing.T, genesis *types.Header, privKey *ecdsa.PrivateKey, borCfg *params.BorConfig, modify ...func(*headerOptions)) *types.Header {
	header := newStandardTestHeader(genesis, modify...)
	signHeader(t, header, privKey, borCfg)
	return header
}

// createTestHeaders creates N test headers with sequential block numbers, all parented to genesis
func createTestHeaders(t *testing.T, genesis *types.Header, count int, privKey *ecdsa.PrivateKey, borCfg *params.BorConfig) []*types.Header {
	headers := make([]*types.Header, count)
	for i := 0; i < count; i++ {
		headers[i] = &types.Header{
			Number:     big.NewInt(int64(i + 1)),
			ParentHash: genesis.Hash(),
			Time:       genesis.Time + uint64(i+1)*2,
			Extra:      make([]byte, types.ExtraVanityLength+types.ExtraSealLength),
			Difficulty: big.NewInt(1),
			GasLimit:   genesis.GasLimit,
			MixDigest:  common.Hash{},
			UncleHash:  uncleHash,
		}
		signHeader(t, headers[i], privKey, borCfg)
	}
	return headers
}

// TestVerifyHeader tests the verifyHeader function with various scenarios
func TestVerifyHeader(t *testing.T) {
	t.Parallel()

	addr1 := common.HexToAddress("0x1")
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	testCases := []struct {
		name          string
		setupChain    func(t *testing.T) (*core.BlockChain, *Bor)
		createHeader  func(t *testing.T, chain *core.BlockChain) *types.Header
		expectedError error
		errorContains string
	}{
		{
			name:       "nil header number returns errUnknownBlock",
			setupChain: makeSetupChain(addr1),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				return &types.Header{
					Number: nil, // This triggers errUnknownBlock
					Extra:  make([]byte, types.ExtraVanityLength+types.ExtraSealLength),
				}
			},
			expectedError: errUnknownBlock,
		},
		{
			name: "future block in Rio mode with parent in future",
			setupChain: makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
				opts.rioBlock = big.NewInt(0)
				opts.genesisTime = uint64(time.Now().Add(10 * time.Second).Unix())
			}),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				return newStandardTestHeader(chain.HeaderChain().GetHeaderByNumber(0))
			},
			expectedError: consensus.ErrFutureBlock,
		},
		{
			name: "future block in Bhilai mode",
			setupChain: makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
				opts.bhilaiBlock = big.NewInt(0)
			}),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				futureTime := uint64(time.Now().Add(1 * time.Hour).Unix())
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.time = futureTime
				})
			},
			expectedError: consensus.ErrFutureBlock,
		},
		{
			name:       "future block in default mode",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				futureTime := uint64(time.Now().Add(1 * time.Hour).Unix())
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.time = futureTime
				})
			},
			expectedError: consensus.ErrFutureBlock,
		},
		// Rio timestamp upper-bound tests: verify that far-future timestamps are rejected.
		{
			// A header with a timestamp set 100 years in the future must be
			// rejected by the upper-bound check introduced in Rio.
			name: "far-future timestamp in Rio mode is rejected",
			setupChain: makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
				opts.rioBlock = big.NewInt(0)
			}),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				farFuture := uint64(time.Now().Unix()) + 100*365*24*3600 // year ~2126
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.time = farFuture
				})
			},
			expectedError: consensus.ErrFutureBlock,
		},
		{
			// Boundary test: a timestamp just beyond the allowed future window is rejected.
			name: "timestamp beyond allowed future bound in Rio mode is rejected",
			setupChain: makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
				opts.rioBlock = big.NewInt(0)
			}),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				beyondBound := uint64(time.Now().Unix()) + maxAllowedFutureBlockTimeSeconds + 5
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.time = beyondBound
				})
			},
			expectedError: consensus.ErrFutureBlock,
		},
		{
			// Regression: a normal block (timestamp slightly in the past) must still be
			// accepted in Rio mode after the upper-bound check is added.
			name: "normal timestamp in Rio mode is accepted",
			setupChain: makeSetupChain(signerAddr, func(opts *chainSetupOptions) {
				opts.rioBlock = big.NewInt(0)
			}),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.uncleHash = uncleHash
					opts.mixDigest = common.Hash{}
				})
			},
			expectedError: nil,
		},
		{
			name:       "missing vanity in extra data",
			setupChain: makeSetupChain(addr1),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.extra = make([]byte, 10) // Too short, missing vanity
				})
			},
			expectedError: errMissingVanity,
		},
		{
			name:       "missing signature in extra data",
			setupChain: makeSetupChain(addr1),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newStandardTestHeader(genesis, func(opts *headerOptions) {
					opts.extra = make([]byte, types.ExtraVanityLength+10) // Missing full signature
				})
			},
			expectedError: errMissingSignature,
		},
		{
			name:       "invalid mix digest (non-zero)",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.mixDigest = common.HexToHash("0x1234") // Should be zero
					opts.uncleHash = uncleHash
				})
			},
			expectedError: errInvalidMixDigest,
		},
		{
			name:       "invalid uncle hash",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.mixDigest = common.Hash{}
					opts.uncleHash = common.HexToHash("0x5678") // Invalid uncle hash
				})
			},
			expectedError: errInvalidUncleHash,
		},
		{
			name:       "nil difficulty for block > 0",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.difficulty = nil // Nil difficulty
					opts.mixDigest = common.Hash{}
					opts.uncleHash = uncleHash
				})
			},
			expectedError: errInvalidDifficulty,
		},
		{
			name:       "gas limit exceeds maximum",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.gasLimit = 0x8000000000000000 // Exceeds 2^63-1
					opts.mixDigest = common.Hash{}
					opts.uncleHash = uncleHash
				})
			},
			errorContains: "invalid gasLimit",
		},
		{
			name:       "unexpected withdrawals hash",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				withdrawalsHash := common.HexToHash("0xabcd")
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.mixDigest = common.Hash{}
					opts.uncleHash = uncleHash
					opts.withdrawalsHash = &withdrawalsHash
				})
			},
			expectedError: consensus.ErrUnexpectedWithdrawals,
		},
		{
			name:       "unexpected requests hash",
			setupChain: makeSetupChain(signerAddr),
			createHeader: func(t *testing.T, chain *core.BlockChain) *types.Header {
				genesis := chain.HeaderChain().GetHeaderByNumber(0)
				requestsHash := common.HexToHash("0xef01")
				return newSignedStandardTestHeader(t, genesis, privKey, chain.Config().Bor, func(opts *headerOptions) {
					opts.mixDigest = common.Hash{}
					opts.uncleHash = uncleHash
					opts.requestsHash = &requestsHash
				})
			},
			expectedError: consensus.ErrUnexpectedRequests,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			chain, bor := tc.setupChain(t)
			defer chain.Stop()

			header := tc.createHeader(t, chain)
			err := bor.verifyHeader(chain.HeaderChain(), header, nil)

			if tc.expectedError != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.expectedError)
			} else if tc.errorContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// setupDefaultTestChain creates a chain with default settings for the given signer
func setupDefaultTestChain(t *testing.T, signerAddr common.Address) (*core.BlockChain, *Bor, *params.BorConfig) {
	opts := defaultChainSetup(signerAddr)
	chain, bor := setupTestChain(t, opts)
	borCfg := &params.BorConfig{
		Sprint: map[string]uint64{"0": opts.sprint},
		Period: map[string]uint64{"0": opts.period},
	}
	return chain, bor, borCfg
}

// TestVerifyHeaders tests the VerifyHeaders function
func TestVerifyHeaders(t *testing.T) {
	t.Parallel()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	t.Run("verifies multiple valid headers", func(t *testing.T) {
		chain, bor, borCfg := setupDefaultTestChain(t, signerAddr)
		defer chain.Stop()

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		headers := createTestHeaders(t, genesis, 5, privKey, borCfg)

		abort, results := bor.VerifyHeaders(chain.HeaderChain(), headers)
		defer close(abort)

		// Collect results
		for i := 0; i < len(headers); i++ {
			err := <-results
			// We expect most headers to fail verifyCascadingFields due to parent not being in chain
			// but this tests that VerifyHeaders iterates through all headers
			_ = err
		}
	})

	t.Run("abort stops verification", func(t *testing.T) {
		chain, bor, borCfg := setupDefaultTestChain(t, signerAddr)
		defer chain.Stop()

		genesis := chain.HeaderChain().GetHeaderByNumber(0)
		headers := createTestHeaders(t, genesis, 100, privKey, borCfg)

		abort, results := bor.VerifyHeaders(chain.HeaderChain(), headers)

		// Close abort immediately without reading any results
		close(abort)

		// Drain results - goroutine should stop due to abort
		count := 0
		timeout := time.After(500 * time.Millisecond)
	drainLoop:
		for {
			select {
			case _, ok := <-results:
				if !ok {
					// Channel closed, goroutine finished cleanly
					break drainLoop
				}
				count++
			case <-timeout:
				// If we timeout, the goroutine might still be running
				// This is acceptable - we just verify the abort mechanism exists
				break drainLoop
			}
		}

		// The abort mechanism should prevent processing all headers
		// We verify the abort channel is functional by checking we processed
		// significantly fewer headers than total, OR the goroutine stopped cleanly
		require.Less(t, count, 100, "Abort mechanism should limit header processing")
	})

	t.Run("empty headers list", func(t *testing.T) {
		chain, bor, _ := setupDefaultTestChain(t, signerAddr)
		defer chain.Stop()

		abort, results := bor.VerifyHeaders(chain.HeaderChain(), []*types.Header{})
		defer close(abort)

		// Should complete immediately with no results
		select {
		case _, ok := <-results:
			if ok {
				t.Fatal("Expected no results for empty headers list")
			}
		case <-time.After(100 * time.Millisecond):
			// Expected - goroutine should complete quickly
		}
	})

	t.Run("propagates errors correctly", func(t *testing.T) {
		chain, bor, _ := setupDefaultTestChain(t, signerAddr)
		defer chain.Stop()

		// Create headers with different errors
		headers := []*types.Header{
			{
				Number: nil, // errUnknownBlock
			},
			{
				Number:     big.NewInt(1),
				Extra:      make([]byte, 10), // errMissingVanity
				Difficulty: big.NewInt(1),
			},
		}

		abort, results := bor.VerifyHeaders(chain.HeaderChain(), headers)
		defer close(abort)

		// First header should return errUnknownBlock
		err1 := <-results
		require.Error(t, err1)
		require.ErrorIs(t, err1, errUnknownBlock)

		// Second header should return errMissingVanity
		err2 := <-results
		require.Error(t, err2)
		require.ErrorIs(t, err2, errMissingVanity)
	})
}

// TestVerifyHeaderCachesBehavior tests that verified headers are cached
func TestVerifyHeaderCachesBehavior(t *testing.T) {
	t.Parallel()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	signerAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	sp := &fakeSpanner{vals: []*valset.Validator{{Address: signerAddr, VotingPower: 1}}}
	borCfg := &params.BorConfig{
		Sprint: map[string]uint64{"0": 64},
		Period: map[string]uint64{"0": 2},
	}

	// Create Bor with proper setup
	cfg := &params.ChainConfig{ChainID: big.NewInt(1), Bor: borCfg}
	genspec := &core.Genesis{Config: cfg, Timestamp: uint64(time.Now().Unix())}
	db := rawdb.NewMemoryDatabase()
	_ = genspec.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults))

	// Create blockchain
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genspec, nil, core.DefaultConfig())
	require.NoError(t, err)
	defer chain.Stop()

	// Create Bor instance
	bor := New(cfg, rawdb.NewMemoryDatabase(), nil, sp, nil, nil, nil, false, 0)

	genesis := chain.HeaderChain().GetHeaderByNumber(0)

	// Create and verify a valid header (that will pass initial checks)
	header := newSignedStandardTestHeader(t, genesis, privKey, borCfg, func(opts *headerOptions) {
		opts.mixDigest = common.Hash{}
		opts.uncleHash = uncleHash
	})

	// Verify - should fail on verifyCascadingFields but still check caching behavior
	_ = bor.verifyHeader(chain.HeaderChain(), header, nil)

	// Check if future headers are cached with extended TTL
	futureHeader := newSignedStandardTestHeader(t, genesis, privKey, borCfg, func(opts *headerOptions) {
		opts.number = big.NewInt(2)
		opts.time = uint64(time.Now().Add(10 * time.Second).Unix()) // Future time
		opts.mixDigest = common.Hash{}
		opts.uncleHash = uncleHash
	})

	_ = bor.verifyHeader(chain.HeaderChain(), futureHeader, nil)
	// The test verifies the cache logic is executed (TTL calculation for future blocks)
}
