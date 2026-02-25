package bench

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Test helper to create a signed transaction
func createTestTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, chainID *big.Int) *types.Transaction {
	t.Helper()

	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1000000000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(1000),
	})

	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, key)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}

	return signedTx
}

// Test helper to encode a transaction as hex
func encodeTxHex(t *testing.T, tx *types.Transaction) string {
	t.Helper()

	data, err := tx.MarshalBinary()
	if err != nil {
		t.Fatalf("failed to marshal tx: %v", err)
	}

	return hex.EncodeToString(data)
}

// Test helper to create a test key
func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	return key
}

// --- Options Tests ---

func TestOptions_Validate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
		errMsg  string
	}{
		{
			name: "all required fields",
			opts: Options{
				ConfigPath:  "/path/to/config.toml",
				GenesisPath: "/path/to/genesis.json",
				TxsPath:     "/path/to/txs.txt",
				OutPath:     "/path/to/out.json",
			},
			wantErr: false,
		},
		{
			name: "missing config",
			opts: Options{
				GenesisPath: "/path/to/genesis.json",
				TxsPath:     "/path/to/txs.txt",
				OutPath:     "/path/to/out.json",
			},
			wantErr: true,
			errMsg:  "ConfigPath",
		},
		{
			name: "missing genesis",
			opts: Options{
				ConfigPath: "/path/to/config.toml",
				TxsPath:    "/path/to/txs.txt",
				OutPath:    "/path/to/out.json",
			},
			wantErr: true,
			errMsg:  "GenesisPath",
		},
		{
			name: "missing txs",
			opts: Options{
				ConfigPath:  "/path/to/config.toml",
				GenesisPath: "/path/to/genesis.json",
				OutPath:     "/path/to/out.json",
			},
			wantErr: true,
			errMsg:  "TxsPath",
		},
		{
			name: "missing out",
			opts: Options{
				ConfigPath:  "/path/to/config.toml",
				GenesisPath: "/path/to/genesis.json",
				TxsPath:     "/path/to/txs.txt",
			},
			wantErr: true,
			errMsg:  "OutPath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- Errors Tests ---

func TestBenchError(t *testing.T) {
	t.Run("without cause", func(t *testing.T) {
		err := NewBenchError(ErrCodeChainIDMismatch, "chain ID mismatch")
		if err.Code != ErrCodeChainIDMismatch {
			t.Errorf("expected code %s, got %s", ErrCodeChainIDMismatch, err.Code)
		}
		if !strings.Contains(err.Error(), "chain_id_mismatch") {
			t.Errorf("error string should contain code: %s", err.Error())
		}
	})

	t.Run("with cause", func(t *testing.T) {
		cause := NewBenchError(ErrCodeIngestionDecode, "inner error")
		err := WrapError(ErrCodeTxPoolReject, "outer error", cause)
		if err.Unwrap() != cause {
			t.Errorf("Unwrap should return cause")
		}
		if !strings.Contains(err.Error(), "inner error") {
			t.Errorf("error string should contain cause: %s", err.Error())
		}
	})

	t.Run("GetErrorCode", func(t *testing.T) {
		benchErr := NewBenchError(ErrCodeNonceGap, "test")
		if GetErrorCode(benchErr) != ErrCodeNonceGap {
			t.Errorf("expected ErrCodeNonceGap")
		}

		if GetErrorCode(nil) != ErrCodeNone {
			t.Errorf("expected ErrCodeNone for nil")
		}
	})
}

// --- TxSource Tests ---

func TestReadTransactions_Valid(t *testing.T) {
	key := testKey(t)
	chainID := big.NewInt(1337)

	tx1 := createTestTx(t, key, 0, chainID)
	tx2 := createTestTx(t, key, 1, chainID)

	content := encodeTxHex(t, tx1) + "\n" + encodeTxHex(t, tx2) + "\n"
	reader := strings.NewReader(content)

	var count int
	result, err := readTransactionsFromReader(reader, func(line int, tx *types.Transaction) error {
		count++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTxs != 2 {
		t.Errorf("expected 2 txs, got %d", result.TotalTxs)
	}
	if count != 2 {
		t.Errorf("callback called %d times, expected 2", count)
	}
	if result.TotalGas != 42000 { // 21000 * 2
		t.Errorf("expected total gas 42000, got %d", result.TotalGas)
	}
}

func TestReadTransactions_WithPrefix(t *testing.T) {
	key := testKey(t)
	chainID := big.NewInt(1337)

	tx := createTestTx(t, key, 0, chainID)
	content := "0x" + encodeTxHex(t, tx) + "\n"
	reader := strings.NewReader(content)

	result, err := readTransactionsFromReader(reader, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTxs != 1 {
		t.Errorf("expected 1 tx, got %d", result.TotalTxs)
	}
}

func TestReadTransactions_EmptyLines(t *testing.T) {
	key := testKey(t)
	chainID := big.NewInt(1337)

	tx := createTestTx(t, key, 0, chainID)
	content := "\n\n" + encodeTxHex(t, tx) + "\n\n"
	reader := strings.NewReader(content)

	result, err := readTransactionsFromReader(reader, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTxs != 1 {
		t.Errorf("expected 1 tx (empty lines skipped), got %d", result.TotalTxs)
	}
}

func TestReadTransactions_InvalidHex(t *testing.T) {
	content := "not-valid-hex\n"
	reader := strings.NewReader(content)

	_, err := readTransactionsFromReader(reader, nil)
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}

	benchErr, ok := err.(*BenchError)
	if !ok {
		t.Fatalf("expected BenchError, got %T", err)
	}
	if benchErr.Code != ErrCodeIngestionDecode {
		t.Errorf("expected code %s, got %s", ErrCodeIngestionDecode, benchErr.Code)
	}
}

func TestReadTransactions_InvalidRLP(t *testing.T) {
	// Valid hex but not valid RLP transaction
	content := "deadbeef\n"
	reader := strings.NewReader(content)

	_, err := readTransactionsFromReader(reader, nil)
	if err == nil {
		t.Fatal("expected error for invalid RLP")
	}
}

func TestPeekTransactions(t *testing.T) {
	key := testKey(t)
	chainID := big.NewInt(1337)

	// Create 5 transactions
	var lines []string
	for i := uint64(0); i < 5; i++ {
		tx := createTestTx(t, key, i, chainID)
		lines = append(lines, encodeTxHex(t, tx))
	}

	// Write to temp file
	tmpDir := t.TempDir()
	txFile := filepath.Join(tmpDir, "txs.txt")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(txFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write tx file: %v", err)
	}

	// Peek only first 3
	txs, err := PeekTransactions(txFile, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(txs) != 3 {
		t.Errorf("expected 3 txs, got %d", len(txs))
	}
}

// --- Report Tests ---

func TestReport_Serialization(t *testing.T) {
	opts := Options{
		ConfigPath:  "/tmp/config.toml",
		GenesisPath: "/tmp/genesis.json",
		TxsPath:     "/tmp/txs.txt",
		OutPath:     "/tmp/out.json",
		RunID:       "test-run-123",
	}

	report := NewReport(opts)
	report.TotalTxsInput = 1000
	report.TotalTxsMined = 1000
	report.BlocksMined = 50
	report.ConfigSHA256 = "abc123"
	report.GenesisSHA256 = "def456"
	report.TxsSHA256 = "789xyz"
	report.SetSuccess(21000000)

	// Serialize to buffer
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		t.Fatalf("failed to encode: %v", err)
	}

	// Deserialize
	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// Verify key fields
	if decoded.RunID != "test-run-123" {
		t.Errorf("RunID mismatch: %s", decoded.RunID)
	}
	if decoded.TotalTxsInput != 1000 {
		t.Errorf("TotalTxsInput mismatch: %d", decoded.TotalTxsInput)
	}
	if decoded.TotalTxsMined != 1000 {
		t.Errorf("TotalTxsMined mismatch: %d", decoded.TotalTxsMined)
	}
	if !decoded.Success {
		t.Error("expected Success=true")
	}
	if decoded.TxPerSec <= 0 {
		t.Error("TxPerSec should be positive")
	}
	if decoded.Environment.CPUCount <= 0 {
		t.Error("CPUCount should be positive")
	}
}

func TestReport_SetFailure(t *testing.T) {
	opts := Options{
		ConfigPath:  "/tmp/config.toml",
		GenesisPath: "/tmp/genesis.json",
		TxsPath:     "/tmp/txs.txt",
		OutPath:     "/tmp/out.json",
	}

	report := NewReport(opts)
	// Add small delay so DurationMs is measurable
	time.Sleep(time.Millisecond)
	benchErr := NewBenchError(ErrCodeNonceGap, "nonce gap detected")
	report.SetFailure(benchErr)

	if report.Success {
		t.Error("expected Success=false")
	}
	if report.ErrorCode != ErrCodeNonceGap {
		t.Errorf("expected error code %s, got %s", ErrCodeNonceGap, report.ErrorCode)
	}
	if report.Error == "" {
		t.Error("expected Error message")
	}
	if report.DurationMs < 0 {
		t.Error("DurationMs should be non-negative")
	}
}

func TestFileSHA256(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "hello world"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	hash, err := fileSHA256(testFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SHA256 of "hello world" (no newline)
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("expected hash %s, got %s", expected, hash)
	}
}

// --- Preflight Tests ---

func TestPreflightDataset_ChainIDMismatch(t *testing.T) {
	key := testKey(t)
	txChainID := big.NewInt(1337)
	expectedChainID := big.NewInt(9999)

	tx := createTestTx(t, key, 0, txChainID)

	tmpDir := t.TempDir()
	txFile := filepath.Join(tmpDir, "txs.txt")
	content := encodeTxHex(t, tx) + "\n"
	if err := os.WriteFile(txFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write tx file: %v", err)
	}

	_, err := PreflightDataset(txFile, expectedChainID, 10)
	if err == nil {
		t.Fatal("expected chain ID mismatch error")
	}

	benchErr, ok := err.(*BenchError)
	if !ok {
		t.Fatalf("expected BenchError, got %T", err)
	}
	if benchErr.Code != ErrCodeChainIDMismatch {
		t.Errorf("expected code %s, got %s", ErrCodeChainIDMismatch, benchErr.Code)
	}
}

func TestPreflightDataset_Valid(t *testing.T) {
	key := testKey(t)
	chainID := big.NewInt(1337)

	// Create contiguous nonces
	var lines []string
	for i := uint64(0); i < 5; i++ {
		tx := createTestTx(t, key, i, chainID)
		lines = append(lines, encodeTxHex(t, tx))
	}

	tmpDir := t.TempDir()
	txFile := filepath.Join(tmpDir, "txs.txt")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(txFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write tx file: %v", err)
	}

	result, err := PreflightDataset(txFile, chainID, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TxCount != 5 {
		t.Errorf("expected 5 txs, got %d", result.TxCount)
	}
	if len(result.SenderNonces) != 1 {
		t.Errorf("expected 1 sender, got %d", len(result.SenderNonces))
	}

	// Check that we detected the starting nonce
	sender := crypto.PubkeyToAddress(key.PublicKey)
	if nonce, ok := result.SenderNonces[sender]; !ok || nonce != 0 {
		t.Errorf("expected sender nonce 0, got %d", nonce)
	}
}

func TestValidateNonceAgainstChain_Gap(t *testing.T) {
	key := testKey(t)
	sender := crypto.PubkeyToAddress(key.PublicKey)

	preflight := &PreflightResult{
		SenderNonces: map[common.Address]uint64{
			sender: 10, // Dataset starts at nonce 10
		},
	}

	// Chain is at nonce 5 - there's a gap
	getNonce := func(addr common.Address) uint64 {
		return 5
	}

	err := ValidateNonceAgainstChain(preflight, getNonce)
	if err == nil {
		t.Fatal("expected nonce gap error")
	}

	benchErr, ok := err.(*BenchError)
	if !ok {
		t.Fatalf("expected BenchError, got %T", err)
	}
	if benchErr.Code != ErrCodeNonceGap {
		t.Errorf("expected code %s, got %s", ErrCodeNonceGap, benchErr.Code)
	}
}

func TestValidateNonceAgainstChain_Valid(t *testing.T) {
	key := testKey(t)
	sender := crypto.PubkeyToAddress(key.PublicKey)

	preflight := &PreflightResult{
		SenderNonces: map[common.Address]uint64{
			sender: 5, // Dataset starts at nonce 5
		},
	}

	// Chain is at nonce 5 - exact match
	getNonce := func(addr common.Address) uint64 {
		return 5
	}

	err := ValidateNonceAgainstChain(preflight, getNonce)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateTxPoolHealth(t *testing.T) {
	tests := []struct {
		name     string
		stats    TxPoolStats
		ingested uint64
		wantErr  bool
		errCode  ErrorCode
	}{
		{
			name:     "healthy pool",
			stats:    TxPoolStats{Pending: 100, Queued: 50},
			ingested: 150,
			wantErr:  false,
		},
		{
			name:     "all queued",
			stats:    TxPoolStats{Pending: 0, Queued: 100},
			ingested: 100,
			wantErr:  true,
			errCode:  ErrCodeNoRunnableTxs,
		},
		{
			name:     "empty pool",
			stats:    TxPoolStats{Pending: 0, Queued: 0},
			ingested: 100,
			wantErr:  true,
			errCode:  ErrCodeNoRunnableTxs,
		},
		{
			name:     "no ingestion",
			stats:    TxPoolStats{Pending: 0, Queued: 0},
			ingested: 0,
			wantErr:  true,
			errCode:  ErrCodeIngestionDecode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTxPoolHealth(tt.stats, tt.ingested)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if benchErr, ok := err.(*BenchError); ok {
					if benchErr.Code != tt.errCode {
						t.Errorf("expected code %s, got %s", tt.errCode, benchErr.Code)
					}
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetGenesisChainID(t *testing.T) {
	tmpDir := t.TempDir()
	genesisFile := filepath.Join(tmpDir, "genesis.json")

	genesis := `{
		"config": {
			"chainId": 1337,
			"homesteadBlock": 0
		},
		"alloc": {}
	}`

	if err := os.WriteFile(genesisFile, []byte(genesis), 0644); err != nil {
		t.Fatalf("failed to write genesis: %v", err)
	}

	chainID, err := GetGenesisChainID(genesisFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := big.NewInt(1337)
	if chainID.Cmp(expected) != 0 {
		t.Errorf("expected chain ID %s, got %s", expected, chainID)
	}
}

// --- Config Tests ---

func TestDeriveBenchmarkEtherbase(t *testing.T) {
	tmpDir := t.TempDir()
	genesisFile := filepath.Join(tmpDir, "genesis.json")

	genesis := `{
		"alloc": {
			"0000000000000000000000000000000000001000": {"balance": "0"},
			"0000000000000000000000000000000000001001": {"balance": "0"},
			"0000000000000000000000000000000000001010": {"balance": "0"},
			"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {"balance": "1000"},
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {"balance": "1000"}
		}
	}`

	if err := os.WriteFile(genesisFile, []byte(genesis), 0644); err != nil {
		t.Fatalf("failed to write genesis: %v", err)
	}

	derived := deriveBenchmarkEtherbase(genesisFile)

	// Should prefer non-prefixed addresses, excluding system contracts
	// "bbbb..." is non-prefixed, should be selected
	expected := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if derived != expected {
		t.Errorf("expected %s, got %s", expected, derived)
	}
}

// --- Tracker Tests ---

func TestMinedTracker_Accounting(t *testing.T) {
	// Create a mock tracker (without actual eth backend)
	tracker := &MinedTracker{
		seen:  make(map[common.Hash]struct{}),
		mined: make(map[common.Hash]struct{}),
	}

	// Mark some hashes as seen
	hash1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	hash2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	hash3 := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	tracker.MarkSeen(hash1)
	tracker.MarkSeen(hash2)

	if tracker.SeenCount() != 2 {
		t.Errorf("expected 2 seen, got %d", tracker.SeenCount())
	}

	// Simulate mining hash1
	tracker.mu.Lock()
	if _, ok := tracker.seen[hash1]; ok {
		tracker.mined[hash1] = struct{}{}
		tracker.minedCount.Add(1)
	}
	tracker.mu.Unlock()

	if tracker.MinedCount() != 1 {
		t.Errorf("expected 1 mined, got %d", tracker.MinedCount())
	}

	// Try to mine hash3 (not seen)
	tracker.mu.Lock()
	if _, ok := tracker.seen[hash3]; ok {
		tracker.mined[hash3] = struct{}{}
		tracker.minedCount.Add(1)
	}
	tracker.mu.Unlock()

	// Should still be 1 since hash3 wasn't seen
	if tracker.MinedCount() != 1 {
		t.Errorf("expected 1 mined (hash3 not seen), got %d", tracker.MinedCount())
	}

	// Try to mine hash1 again (duplicate)
	tracker.mu.Lock()
	if _, ok := tracker.seen[hash1]; ok {
		if _, alreadyMined := tracker.mined[hash1]; !alreadyMined {
			tracker.mined[hash1] = struct{}{}
			tracker.minedCount.Add(1)
		}
	}
	tracker.mu.Unlock()

	// Should still be 1 since hash1 was already mined
	if tracker.MinedCount() != 1 {
		t.Errorf("expected 1 mined (no duplicate), got %d", tracker.MinedCount())
	}
}

// --- Runner Tests ---

func TestWaitForBlock(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctx := t.Context()
		blockNum := uint64(5)
		getBlockNum := func() uint64 {
			blockNum++
			return blockNum
		}

		err := waitForBlock(ctx, getBlockNum, 5, time.Second)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		ctx := t.Context()
		getBlockNum := func() uint64 {
			return 5 // Never advances
		}

		err := waitForBlock(ctx, getBlockNum, 5, 100*time.Millisecond)
		if err == nil {
			t.Error("expected timeout error")
		}
	})
}
