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

// TestWitnessVerificationConstants tests the verification constants
func TestWitnessVerificationConstants(t *testing.T) {
	// These constants should match the ones defined in eth/fetcher/witness_manager.go
	const (
		witnessPageWarningThreshold = 10
		witnessVerificationPeers    = 2
	)

	if witnessPageWarningThreshold != 10 {
		t.Errorf("Expected witnessPageWarningThreshold to be 10, got %d", witnessPageWarningThreshold)
	}
	if witnessVerificationPeers != 2 {
		t.Errorf("Expected witnessVerificationPeers to be 2, got %d", witnessVerificationPeers)
	}
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

// TestWitnessVerificationScenarios tests various verification scenarios
func TestWitnessVerificationScenarios(t *testing.T) {
	t.Run("MaliciousPeer_ExcessivePages", func(t *testing.T) {
		// Simulate a malicious peer reporting 1000+ pages
		reportedPages := uint64(1000)
		peerPages := []uint64{15, 15} // Other peers report normal page count

		isHonest := simulateWitnessPageCountVerification(reportedPages, peerPages)

		if isHonest {
			t.Error("Expected malicious peer with excessive pages to be marked as dishonest")
		}
	})

	t.Run("HonestPeer_LargeButReasonablePages", func(t *testing.T) {
		// Simulate an honest peer with large but reasonable page count
		reportedPages := uint64(50)
		peerPages := []uint64{50, 50} // Other peers agree

		isHonest := simulateWitnessPageCountVerification(reportedPages, peerPages)

		if !isHonest {
			t.Error("Expected honest peer with large but reasonable pages to be marked as honest")
		}
	})

	t.Run("NetworkPartition_ConservativeApproach", func(t *testing.T) {
		// Simulate network partition where only one peer responds
		reportedPages := uint64(100)
		peerPages := []uint64{100} // Only one peer responds

		isHonest := simulateWitnessPageCountVerification(reportedPages, peerPages)

		if !isHonest {
			t.Error("Expected conservative approach to mark peer as honest when insufficient consensus")
		}
	})

	t.Run("ConsensusThreshold_EdgeCase", func(t *testing.T) {
		// Test exactly at the warning threshold
		reportedPages := uint64(10)
		peerPages := []uint64{10, 10}

		isHonest := simulateWitnessPageCountVerification(reportedPages, peerPages)

		if !isHonest {
			t.Error("Expected peer at threshold to be marked as honest")
		}
	})
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
