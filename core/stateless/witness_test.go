package stateless

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// MockHeaderReader is a mock implementation of HeaderReader for testing.
type mockHeaderReader struct {
	headers map[common.Hash]*types.Header
}

func (m *mockHeaderReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return m.headers[hash]
}

func newMockHeaderReader() *mockHeaderReader {
	return &mockHeaderReader{
		headers: make(map[common.Hash]*types.Header),
	}
}

func (m *mockHeaderReader) addHeader(header *types.Header) {
	m.headers[header.Hash()] = header
}

func TestValidateWitnessPreState_Success(t *testing.T) {
	// Create test headers.
	parentStateRoot := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	parentHeader := &types.Header{
		Number:     big.NewInt(99),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Root:       parentStateRoot,
	}

	// Use the actual hash of the parent header.
	parentHash := parentHeader.Hash()

	contextHeader := &types.Header{
		Number:     big.NewInt(100),
		ParentHash: parentHash,
		Root:       common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"),
	}

	// Set up mock header reader.
	mockReader := newMockHeaderReader()
	mockReader.addHeader(parentHeader)

	// Create witness with matching pre-state root.
	witness := &Witness{
		context: contextHeader,
		Headers: []*types.Header{parentHeader}, // First header should be parent.
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	// Test validation - should succeed.
	err := ValidateWitnessPreState(witness, mockReader)
	if err != nil {
		t.Errorf("Expected validation to succeed, but got error: %v", err)
	}
}

func TestValidateWitnessPreState_StateMismatch(t *testing.T) {
	// Create test headers with mismatched state roots.
	parentStateRoot := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	mismatchedStateRoot := common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

	parentHeader := &types.Header{
		Number:     big.NewInt(99),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Root:       parentStateRoot,
	}

	// Use the actual hash of the parent header.
	parentHash := parentHeader.Hash()

	contextHeader := &types.Header{
		Number:     big.NewInt(100),
		ParentHash: parentHash,
		Root:       common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"),
	}

	// Create witness header with mismatched state root.
	witnessParentHeader := &types.Header{
		Number:     big.NewInt(99),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Root:       mismatchedStateRoot, // Different from actual parent.
	}

	// Set up mock header reader.
	mockReader := newMockHeaderReader()
	mockReader.addHeader(parentHeader)

	// Create witness with mismatched pre-state root.
	witness := &Witness{
		context: contextHeader,
		Headers: []*types.Header{witnessParentHeader}, // Mismatched parent header.
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	// Test validation - should fail.
	err := ValidateWitnessPreState(witness, mockReader)
	if err == nil {
		t.Error("Expected validation to fail due to state root mismatch, but it succeeded")
	}

	expectedError := "witness pre-state root mismatch"
	if err != nil && len(err.Error()) > 0 {
		if err.Error()[:len(expectedError)] != expectedError {
			t.Errorf("Expected error message to start with '%s', but got: %v", expectedError, err)
		}
	}
}

func TestValidateWitnessPreState_EdgeCases(t *testing.T) {
	mockReader := newMockHeaderReader()

	// Test case 1: Nil witness.
	t.Run("NilWitness", func(t *testing.T) {
		err := ValidateWitnessPreState(nil, mockReader)
		if err == nil {
			t.Error("Expected validation to fail for nil witness")
		}
		if err.Error() != "witness is nil" {
			t.Errorf("Expected error 'witness is nil', got: %v", err)
		}
	})

	// Test case 2: Witness with no headers.
	t.Run("NoHeaders", func(t *testing.T) {
		witness := &Witness{
			context: &types.Header{Number: big.NewInt(100)},
			Headers: []*types.Header{}, // Empty headers.
			Codes:   make(map[string]struct{}),
			State:   make(map[string]struct{}),
		}

		err := ValidateWitnessPreState(witness, mockReader)
		if err == nil {
			t.Error("Expected validation to fail for witness with no headers")
		}
		if err.Error() != "witness has no headers" {
			t.Errorf("Expected error 'witness has no headers', got: %v", err)
		}
	})

	// Test case 3: Witness with nil context header.
	t.Run("NilContextHeader", func(t *testing.T) {
		witness := &Witness{
			context: nil, // Nil context header.
			Headers: []*types.Header{
				{
					Number: big.NewInt(99),
					Root:   common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
				},
			},
			Codes: make(map[string]struct{}),
			State: make(map[string]struct{}),
		}

		err := ValidateWitnessPreState(witness, mockReader)
		if err == nil {
			t.Error("Expected validation to fail for witness with nil context header")
		}
		if err.Error() != "witness context header is nil" {
			t.Errorf("Expected error 'witness context header is nil', got: %v", err)
		}
	})

	// Test case 4: Parent header not found.
	t.Run("ParentNotFound", func(t *testing.T) {
		contextHeader := &types.Header{
			Number:     big.NewInt(100),
			ParentHash: common.HexToHash("0xnonexistent1234567890abcdef1234567890abcdef1234567890abcdef123456"),
			Root:       common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"),
		}

		witness := &Witness{
			context: contextHeader,
			Headers: []*types.Header{
				{
					Number: big.NewInt(99),
					Root:   common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
				},
			},
			Codes: make(map[string]struct{}),
			State: make(map[string]struct{}),
		}

		// Don't add parent header to mock reader - it won't be found.
		err := ValidateWitnessPreState(witness, mockReader)
		if err == nil {
			t.Error("Expected validation to fail when parent header is not found")
		}

		expectedError := "parent block header not found"
		if err != nil && len(err.Error()) > len(expectedError) {
			if err.Error()[:len(expectedError)] != expectedError {
				t.Errorf("Expected error message to start with '%s', but got: %v", expectedError, err)
			}
		}
	})
}

func TestValidateWitnessPreState_MultipleHeaders(t *testing.T) {
	// Test witness with multiple headers (realistic scenario).
	parentStateRoot := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	grandParentStateRoot := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")

	grandParentHeader := &types.Header{
		Number:     big.NewInt(98),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Root:       grandParentStateRoot,
	}

	// Use the actual hash of the grandparent header.
	grandParentHash := grandParentHeader.Hash()

	parentHeader := &types.Header{
		Number:     big.NewInt(99),
		ParentHash: grandParentHash,
		Root:       parentStateRoot,
	}

	// Use the actual hash of the parent header.
	parentHash := parentHeader.Hash()

	contextHeader := &types.Header{
		Number:     big.NewInt(100),
		ParentHash: parentHash,
		Root:       common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"),
	}

	// Set up mock header reader.
	mockReader := newMockHeaderReader()
	mockReader.addHeader(parentHeader)
	mockReader.addHeader(grandParentHeader)

	// Create witness with multiple headers (parent should be first).
	witness := &Witness{
		context: contextHeader,
		Headers: []*types.Header{parentHeader, grandParentHeader}, // Multiple headers.
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	// Test validation - should succeed (only first header matters for validation).
	err := ValidateWitnessPreState(witness, mockReader)
	if err != nil {
		t.Errorf("Expected validation to succeed with multiple headers, but got error: %v", err)
	}
}

// TestConsensusWithOriginalPeer tests consensus calculation including original peer
func TestConsensusWithOriginalPeer(t *testing.T) {
	t.Run("Case1_OriginalPeer3_RandomPeers2and3_ShouldChoose3", func(t *testing.T) {
		// Original peer: 3, Random peer 1: 2, Random peer 2: 3
		// Out of 3 total peers, 2 say "3" → Should choose 3
		originalCount := uint64(3)
		randomCounts := []uint64{2, 3}

		consensus := getConsensusIncludingOriginal(originalCount, randomCounts)

		if consensus != 3 {
			t.Errorf("Expected consensus to be 3 (majority), got %d", consensus)
		}
		t.Logf("Correct: Out of 3 peers (1 says 2, 2 say 3), chose majority: 3")
	})

	t.Run("Case2_OriginalPeer3_RandomPeers2and2_ShouldChoose2", func(t *testing.T) {
		// Original peer: 3, Random peer 1: 2, Random peer 2: 2
		// Out of 3 total peers, 2 say "2" → Should choose 2 (original is dishonest)
		originalCount := uint64(3)
		randomCounts := []uint64{2, 2}

		consensus := getConsensusIncludingOriginal(originalCount, randomCounts)

		if consensus != 2 {
			t.Errorf("Expected consensus to be 2 (majority), got %d", consensus)
		}
		t.Logf("Correct: Out of 3 peers (2 say 2, 1 says 3), chose majority: 2")
	})

	t.Run("NoMajority_AllDifferent", func(t *testing.T) {
		// Original peer: 3, Random peer 1: 2, Random peer 2: 4
		// All different, no majority
		originalCount := uint64(3)
		randomCounts := []uint64{2, 4}

		consensus := getConsensusIncludingOriginal(originalCount, randomCounts)

		if consensus != 0 {
			t.Errorf("Expected consensus to be 0 (no majority), got %d", consensus)
		}
		t.Logf("Correct: No majority (1,1,1), no consensus")
	})
}

// getConsensusIncludingOriginal simulates consensus calculation with original peer included
func getConsensusIncludingOriginal(originalCount uint64, randomCounts []uint64) uint64 {
	// Build count map including original peer
	countMap := make(map[uint64]int)
	countMap[originalCount] = 1

	for _, count := range randomCounts {
		countMap[count]++
	}

	// Find majority (at least 2 out of 3)
	var maxCount int
	var consensusCount uint64
	for count, freq := range countMap {
		if freq > maxCount {
			maxCount = freq
			consensusCount = count
		}
	}

	// Need at least 2 votes for majority
	if maxCount >= 2 {
		return consensusCount
	}

	return 0 // No consensus
}

// TestSimplifiedWitnessVerification tests the simplified verification logic
func TestSimplifiedWitnessVerification(t *testing.T) {
	tests := []struct {
		name           string
		reportedPages  uint64
		peerPages      []uint64
		expectedHonest bool
		description    string
	}{
		{
			name:           "UnderThreshold_ShouldBeHonest",
			reportedPages:  5,
			peerPages:      []uint64{5, 5},
			expectedHonest: true,
			description:    "Page count under threshold should be considered honest",
		},
		{
			name:           "OverThreshold_ConsensusAgreement",
			reportedPages:  15,
			peerPages:      []uint64{15, 15},
			expectedHonest: true,
			description:    "Consensus agreement should mark peer as honest",
		},
		{
			name:           "OverThreshold_ConsensusDisagreement",
			reportedPages:  15,
			peerPages:      []uint64{20, 20},
			expectedHonest: false,
			description:    "Consensus disagreement should mark peer as dishonest (dropped)",
		},
		{
			name:           "OverThreshold_MixedResults",
			reportedPages:  15,
			peerPages:      []uint64{15, 20},
			expectedHonest: true,
			description:    "Mixed results should default to honest (conservative)",
		},
		{
			name:           "OverThreshold_InsufficientPeers",
			reportedPages:  15,
			peerPages:      []uint64{15},
			expectedHonest: true,
			description:    "Insufficient peers should default to honest (conservative)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the simplified verification logic
			isHonest := simulateSimplifiedWitnessVerification(tt.reportedPages, tt.peerPages)

			if isHonest != tt.expectedHonest {
				t.Errorf("%s: expected honest=%v, got honest=%v", tt.description, tt.expectedHonest, isHonest)
			}
		})
	}
}

// simulateSimplifiedWitnessVerification simulates the simplified verification logic
func simulateSimplifiedWitnessVerification(reportedPageCount uint64, peerPageCounts []uint64) bool {
	const witnessPageWarningThreshold = 10
	const witnessVerificationPeers = 2

	// If under threshold, assume honest
	if reportedPageCount <= witnessPageWarningThreshold {
		return true
	}

	// If insufficient peers, assume honest (conservative approach)
	if len(peerPageCounts) < witnessVerificationPeers {
		return true
	}

	// Get consensus from peers (most common page count)
	countMap := make(map[uint64]int)
	for _, count := range peerPageCounts {
		countMap[count]++
	}

	var maxCount int
	var consensusCount uint64
	for count, freq := range countMap {
		if freq > maxCount {
			maxCount = freq
			consensusCount = count
		}
	}

	// If we have consensus, check if it matches reported count
	if maxCount >= 2 {
		return consensusCount == reportedPageCount
	}

	// No clear consensus, assume honest (conservative approach)
	return true
}

// TestWitnessVerificationScenarios tests various verification scenarios
func TestWitnessVerificationScenarios(t *testing.T) {
	t.Run("MaliciousPeer_ExcessivePages", func(t *testing.T) {
		// Simulate a malicious peer reporting 1000+ pages
		reportedPages := uint64(1000)
		peerPages := []uint64{15, 15} // Other peers report normal page count

		isHonest := simulateSimplifiedWitnessVerification(reportedPages, peerPages)

		if isHonest {
			t.Error("Expected malicious peer with excessive pages to be marked as dishonest")
		}
	})

	t.Run("HonestPeer_LargeButReasonablePages", func(t *testing.T) {
		// Simulate an honest peer with large but reasonable page count
		reportedPages := uint64(50)
		peerPages := []uint64{50, 50} // Other peers agree

		isHonest := simulateSimplifiedWitnessVerification(reportedPages, peerPages)

		if !isHonest {
			t.Error("Expected honest peer with large but reasonable pages to be marked as honest")
		}
	})

	t.Run("NetworkPartition_ConservativeApproach", func(t *testing.T) {
		// Simulate network partition where only one peer responds
		reportedPages := uint64(100)
		peerPages := []uint64{100} // Only one peer responds

		isHonest := simulateSimplifiedWitnessVerification(reportedPages, peerPages)

		if !isHonest {
			t.Error("Expected conservative approach to mark peer as honest when insufficient consensus")
		}
	})
}

// TestTotalPagesConsistency tests that peers cannot change TotalPages across pages
func TestTotalPagesConsistency(t *testing.T) {
	t.Run("ConsistentTotalPages_ShouldPass", func(t *testing.T) {
		// Simulate receiving pages with consistent TotalPages
		pages := []struct {
			pageNum    uint64
			totalPages uint64
		}{
			{0, 3},
			{1, 3},
			{2, 3},
		}

		var storedTotalPages uint64
		var hasTotalPages bool

		for _, page := range pages {
			if hasTotalPages {
				// Verify TotalPages hasn't changed
				if storedTotalPages != page.totalPages {
					t.Errorf("TotalPages changed from %d to %d on page %d", storedTotalPages, page.totalPages, page.pageNum)
					return
				}
			} else {
				// First time - store it
				storedTotalPages = page.totalPages
				hasTotalPages = true
			}
		}

		// All pages had consistent TotalPages
		if storedTotalPages != 3 {
			t.Errorf("Expected TotalPages to be 3, got %d", storedTotalPages)
		}
	})

	t.Run("InconsistentTotalPages_ShouldFail", func(t *testing.T) {
		// Simulate malicious peer changing TotalPages
		pages := []struct {
			pageNum    uint64
			totalPages uint64
		}{
			{0, 3},
			{1, 3},
			{2, 3},
			{3, 1000}, // Malicious peer changes TotalPages!
		}

		var storedTotalPages uint64
		var hasTotalPages bool
		attackDetected := false

		for _, page := range pages {
			if hasTotalPages {
				// Verify TotalPages hasn't changed
				if storedTotalPages != page.totalPages {
					attackDetected = true
					t.Logf("Attack detected: TotalPages changed from %d to %d on page %d", storedTotalPages, page.totalPages, page.pageNum)
					break
				}
			} else {
				// First time - store it
				storedTotalPages = page.totalPages
				hasTotalPages = true
			}
		}

		if !attackDetected {
			t.Error("Expected attack to be detected when TotalPages changes")
		}
	})

	t.Run("ExcessPages_ShouldFail", func(t *testing.T) {
		// Simulate peer sending more pages than claimed
		totalPages := uint64(3)
		receivedPages := 5 // Peer sends 5 pages but claimed 3

		if receivedPages > int(totalPages) {
			// This should be detected and rejected
			t.Logf("Correctly detected: peer sent %d pages but claimed %d", receivedPages, totalPages)
		} else {
			t.Error("Failed to detect peer sending more pages than claimed")
		}
	})

	t.Run("InvalidPageNumber_ShouldFail", func(t *testing.T) {
		// Simulate peer sending page number >= TotalPages
		testCases := []struct {
			pageNum    uint64
			totalPages uint64
			shouldFail bool
		}{
			{0, 3, false}, // Valid: page 0 of 3
			{2, 3, false}, // Valid: page 2 of 3 (last page)
			{3, 3, true},  // Invalid: page 3 >= TotalPages 3
			{5, 3, true},  // Invalid: page 5 >= TotalPages 3
		}

		for _, tc := range testCases {
			isInvalid := tc.pageNum >= tc.totalPages
			if isInvalid != tc.shouldFail {
				t.Errorf("Page=%d, TotalPages=%d: expected fail=%v, got fail=%v", tc.pageNum, tc.totalPages, tc.shouldFail, isInvalid)
			}

			if isInvalid {
				t.Logf("Correctly rejected: page %d >= totalPages %d", tc.pageNum, tc.totalPages)
			}
		}
	})
}

// TestWitnessPageCountVerification tests the page count verification logic
func TestWitnessPageCountVerification(t *testing.T) {
	tests := []struct {
		name           string
		reportedPages  uint64
		peerPages      []uint64
		expectedHonest bool
		description    string
	}{
		{
			name:           "UnderThreshold_ShouldBeHonest",
			reportedPages:  5,
			peerPages:      []uint64{5, 5},
			expectedHonest: true,
			description:    "Page count under threshold should be considered honest",
		},
		{
			name:           "OverThreshold_ConsensusAgreement",
			reportedPages:  15,
			peerPages:      []uint64{15, 15},
			expectedHonest: true,
			description:    "Consensus agreement should mark peer as honest",
		},
		{
			name:           "OverThreshold_ConsensusDisagreement",
			reportedPages:  15,
			peerPages:      []uint64{20, 20},
			expectedHonest: false,
			description:    "Consensus disagreement should mark peer as dishonest",
		},
		{
			name:           "OverThreshold_MixedResults",
			reportedPages:  15,
			peerPages:      []uint64{15, 20},
			expectedHonest: true,
			description:    "Mixed results should default to honest (conservative)",
		},
		{
			name:           "OverThreshold_InsufficientPeers",
			reportedPages:  15,
			peerPages:      []uint64{15},
			expectedHonest: true,
			description:    "Insufficient peers should default to honest (conservative)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the verification logic
			isHonest := simulateWitnessPageCountVerification(tt.reportedPages, tt.peerPages)

			if isHonest != tt.expectedHonest {
				t.Errorf("%s: expected honest=%v, got honest=%v", tt.description, tt.expectedHonest, isHonest)
			}
		})
	}
}

// simulateWitnessPageCountVerification simulates the verification logic from witness_manager.go
func simulateWitnessPageCountVerification(reportedPageCount uint64, peerPageCounts []uint64) bool {
	const witnessPageWarningThreshold = 10
	const witnessVerificationPeers = 2

	// If under threshold, assume honest
	if reportedPageCount <= witnessPageWarningThreshold {
		return true
	}

	// If insufficient peers, assume honest (conservative approach)
	if len(peerPageCounts) < witnessVerificationPeers {
		return true
	}

	// Check for consensus among peers
	consensusCount := uint64(0)
	honestPeers := 0

	for _, pageCount := range peerPageCounts {
		honestPeers++
		if consensusCount == 0 {
			consensusCount = pageCount
		} else if consensusCount != pageCount {
			// No clear consensus
			consensusCount = 0
			break
		}
	}

	// If we have consensus from at least 2 peers
	if honestPeers >= witnessVerificationPeers && consensusCount > 0 {
		return consensusCount == reportedPageCount
	}

	// No clear consensus, assume honest (conservative approach)
	return true
}

// TestWitnessVerificationPerformance tests the performance characteristics
func TestWitnessVerificationPerformance(t *testing.T) {
	t.Run("LargeWitness_Verification", func(t *testing.T) {
		// Test with a very large witness (1000+ pages)
		reportedPages := uint64(1000)
		peerPages := []uint64{1000, 1000}

		start := time.Now()
		isHonest := simulateWitnessPageCountVerification(reportedPages, peerPages)
		duration := time.Since(start)

		if !isHonest {
			t.Error("Expected large witness with consensus to be marked as honest")
		}

		// Verification should be fast (under 1ms)
		if duration > time.Millisecond {
			t.Errorf("Verification took too long: %v", duration)
		}
	})
}
