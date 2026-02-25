package bench

import (
	"context"
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
	"github.com/ethereum/go-ethereum/params"
)

// Integration tests validate the benchmark components work together.
// These tests use file fixtures and test the full preflight flow,
// but don't start an actual node (that requires the full node infrastructure).

// TestIntegration_PreflightWithValidDataset tests the preflight flow with a valid dataset.
func TestIntegration_PreflightWithValidDataset(t *testing.T) {
	// Create test fixtures
	tmpDir := t.TempDir()
	key := generateTestKey(t)
	chainID := big.NewInt(1337)

	// Create genesis file
	genesisPath := createTestGenesis(t, tmpDir, chainID, key)

	// Create transaction dataset with contiguous nonces
	txsPath := createTestTxDataset(t, tmpDir, key, chainID, 0, 10)

	// Run preflight
	expectedChainID, err := GetGenesisChainID(genesisPath)
	if err != nil {
		t.Fatalf("failed to get chain ID: %v", err)
	}

	result, err := PreflightDataset(txsPath, expectedChainID, 100)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}

	if result.TxCount != 10 {
		t.Errorf("expected 10 txs, got %d", result.TxCount)
	}

	if len(result.SenderNonces) != 1 {
		t.Errorf("expected 1 sender, got %d", len(result.SenderNonces))
	}
}

// TestIntegration_PreflightChainIDMismatch tests preflight catches chain ID mismatch.
func TestIntegration_PreflightChainIDMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	key := generateTestKey(t)

	// Genesis has chain ID 1337
	genesisChainID := big.NewInt(1337)
	genesisPath := createTestGenesis(t, tmpDir, genesisChainID, key)

	// Dataset has chain ID 9999
	datasetChainID := big.NewInt(9999)
	txsPath := createTestTxDataset(t, tmpDir, key, datasetChainID, 0, 5)

	// Preflight should fail
	expectedChainID, _ := GetGenesisChainID(genesisPath)
	_, err := PreflightDataset(txsPath, expectedChainID, 100)

	if err == nil {
		t.Fatal("expected chain ID mismatch error")
	}

	benchErr, ok := err.(*BenchError)
	if !ok || benchErr.Code != ErrCodeChainIDMismatch {
		t.Errorf("expected chain ID mismatch error, got: %v", err)
	}
}

// TestIntegration_PreflightNonceGapDetection tests preflight detects nonce gaps.
func TestIntegration_PreflightNonceGapDetection(t *testing.T) {
	tmpDir := t.TempDir()
	key := generateTestKey(t)
	chainID := big.NewInt(1337)

	// Create genesis
	_ = createTestGenesis(t, tmpDir, chainID, key)

	// Create dataset starting at nonce 10 (gap from chain nonce 0)
	txsPath := createTestTxDataset(t, tmpDir, key, chainID, 10, 5)

	// Preflight passes (it doesn't know chain state yet)
	result, err := PreflightDataset(txsPath, chainID, 100)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}

	// But nonce validation against chain state should fail
	sender := crypto.PubkeyToAddress(key.PublicKey)
	if startNonce, ok := result.SenderNonces[sender]; !ok || startNonce != 10 {
		t.Errorf("expected starting nonce 10, got %d", startNonce)
	}

	// Simulate chain state with nonce 0
	getNonce := func(addr common.Address) uint64 {
		return 0
	}

	err = ValidateNonceAgainstChain(result, getNonce)
	if err == nil {
		t.Fatal("expected nonce gap error")
	}

	benchErr, ok := err.(*BenchError)
	if !ok || benchErr.Code != ErrCodeNonceGap {
		t.Errorf("expected nonce gap error, got: %v", err)
	}
}

// TestIntegration_ReportRoundTrip tests report serialization and deserialization.
func TestIntegration_ReportRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	// Create minimal fixtures for hashing
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[p2p]\nmaxpeers = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	genesisPath := filepath.Join(tmpDir, "genesis.json")
	genesis := `{"config": {"chainId": 1337}, "alloc": {}}`
	if err := os.WriteFile(genesisPath, []byte(genesis), 0644); err != nil {
		t.Fatal(err)
	}

	txsPath := filepath.Join(tmpDir, "txs.txt")
	if err := os.WriteFile(txsPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(tmpDir, "report.json")

	opts := Options{
		ConfigPath:  configPath,
		GenesisPath: genesisPath,
		TxsPath:     txsPath,
		OutPath:     outPath,
		RunID:       "integration-test-123",
	}

	// Create and populate report
	report := NewReport(opts)
	if err := report.HashInputFiles(opts); err != nil {
		t.Fatalf("failed to hash files: %v", err)
	}

	report.TotalTxsInput = 1000
	report.TotalTxsMined = 1000
	report.BlocksMined = 50
	report.SetSuccess(21000000)

	// Write report
	if err := report.Write(outPath); err != nil {
		t.Fatalf("failed to write report: %v", err)
	}

	// Read and verify
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read report: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to decode report: %v", err)
	}

	// Verify key fields
	if decoded.RunID != "integration-test-123" {
		t.Errorf("RunID mismatch: %s", decoded.RunID)
	}
	if decoded.TotalTxsInput != 1000 {
		t.Errorf("TotalTxsInput mismatch: %d", decoded.TotalTxsInput)
	}
	if !decoded.Success {
		t.Error("expected success=true")
	}
	if decoded.ConfigSHA256 == "" {
		t.Error("missing config hash")
	}
	if decoded.GenesisSHA256 == "" {
		t.Error("missing genesis hash")
	}
}

// TestIntegration_OptionsValidation tests option validation catches all errors.
func TestIntegration_OptionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Options)
		wantErr string
	}{
		{
			name:    "missing all",
			modify:  func(o *Options) {},
			wantErr: "ConfigPath",
		},
		{
			name:    "config only",
			modify:  func(o *Options) { o.ConfigPath = "/tmp/c" },
			wantErr: "GenesisPath",
		},
		{
			name: "config and genesis",
			modify: func(o *Options) {
				o.ConfigPath = "/tmp/c"
				o.GenesisPath = "/tmp/g"
			},
			wantErr: "TxsPath",
		},
		{
			name: "missing out",
			modify: func(o *Options) {
				o.ConfigPath = "/tmp/c"
				o.GenesisPath = "/tmp/g"
				o.TxsPath = "/tmp/t"
			},
			wantErr: "OutPath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{}
			tt.modify(&opts)
			err := opts.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestIntegration_TxSourceWithLargeFile tests reading a larger transaction file.
func TestIntegration_TxSourceWithLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	tmpDir := t.TempDir()
	key := generateTestKey(t)
	chainID := big.NewInt(1337)

	// Create 1000 transactions
	txsPath := createTestTxDataset(t, tmpDir, key, chainID, 0, 1000)

	// Read all
	var count uint64
	result, err := ReadTransactions(txsPath, func(line int, tx *types.Transaction) error {
		count++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTxs != 1000 {
		t.Errorf("expected 1000 txs, got %d", result.TotalTxs)
	}
	if count != 1000 {
		t.Errorf("callback count %d != total %d", count, result.TotalTxs)
	}
}

// TestIntegration_MalformedTxFile tests handling of malformed transaction files.
func TestIntegration_MalformedTxFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		content string
	}{
		{"invalid hex", "not-hex-at-all"},
		{"valid hex but not tx", "deadbeef"},
		{"truncated tx", "f8"}, // Valid hex prefix but incomplete
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			txsPath := filepath.Join(tmpDir, tt.name+".txt")
			if err := os.WriteFile(txsPath, []byte(tt.content+"\n"), 0644); err != nil {
				t.Fatal(err)
			}

			_, err := ReadTransactions(txsPath, nil)
			if err == nil {
				t.Error("expected error for malformed input")
			}
		})
	}
}

// TestIntegration_ConfigDerivation tests etherbase derivation from genesis.
func TestIntegration_ConfigDerivation(t *testing.T) {
	tmpDir := t.TempDir()

	// Genesis with multiple accounts including system contracts
	// Use valid 40-char hex addresses
	genesis := `{
		"config": {"chainId": 1337},
		"alloc": {
			"0000000000000000000000000000000000001000": {"balance": "0"},
			"0000000000000000000000000000000000001001": {"balance": "0"},
			"0000000000000000000000000000000000001010": {"balance": "0"},
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {"balance": "1000000000000000000"},
			"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {"balance": "1000000000000000000"}
		}
	}`

	genesisPath := filepath.Join(tmpDir, "genesis.json")
	if err := os.WriteFile(genesisPath, []byte(genesis), 0644); err != nil {
		t.Fatal(err)
	}

	derived := deriveBenchmarkEtherbase(genesisPath)
	if derived == "" {
		t.Fatal("expected derived etherbase")
	}

	// Should be the non-prefixed address (validator-style) that's not a system contract
	// "aaaa..." should be selected as it's non-prefixed
	expected := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if derived != expected {
		t.Errorf("expected %s, got %s", expected, derived)
	}
}

// TestIntegration_RunWithMissingFiles tests Run fails gracefully with missing files.
func TestIntegration_RunWithMissingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	opts := Options{
		ConfigPath:  filepath.Join(tmpDir, "nonexistent.toml"),
		GenesisPath: filepath.Join(tmpDir, "nonexistent.json"),
		TxsPath:     filepath.Join(tmpDir, "nonexistent.txt"),
		OutPath:     filepath.Join(tmpDir, "out.json"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, opts)
	if err == nil {
		t.Error("expected error for missing files")
	}
}

// --- Helper functions ---

func generateTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	return key
}

func createTestGenesis(t *testing.T, dir string, chainID *big.Int, fundedKey *ecdsa.PrivateKey) string {
	t.Helper()

	address := crypto.PubkeyToAddress(fundedKey.PublicKey)
	genesis := map[string]interface{}{
		"config": map[string]interface{}{
			"chainId":             chainID.Int64(),
			"homesteadBlock":      0,
			"eip150Block":         0,
			"eip155Block":         0,
			"eip158Block":         0,
			"byzantiumBlock":      0,
			"constantinopleBlock": 0,
			"petersburgBlock":     0,
			"istanbulBlock":       0,
			"berlinBlock":         0,
			"londonBlock":         0,
		},
		"alloc": map[string]interface{}{
			address.Hex(): map[string]string{
				"balance": "1000000000000000000000000",
			},
		},
		"difficulty":    "1",
		"gasLimit":      "0x1c9c380", // 30M gas
		"baseFeePerGas": params.InitialBaseFee,
	}

	data, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal genesis: %v", err)
	}

	path := filepath.Join(dir, "genesis.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write genesis: %v", err)
	}

	return path
}

func createTestTxDataset(t *testing.T, dir string, key *ecdsa.PrivateKey, chainID *big.Int, startNonce, count uint64) string {
	t.Helper()

	var lines []string
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	signer := types.LatestSignerForChainID(chainID)

	for i := uint64(0); i < count; i++ {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     startNonce + i,
			GasTipCap: big.NewInt(1),
			GasFeeCap: big.NewInt(1000000000),
			Gas:       21000,
			To:        &to,
			Value:     big.NewInt(1),
		})

		signedTx, err := types.SignTx(tx, signer, key)
		if err != nil {
			t.Fatalf("failed to sign tx %d: %v", i, err)
		}

		data, err := signedTx.MarshalBinary()
		if err != nil {
			t.Fatalf("failed to marshal tx %d: %v", i, err)
		}

		lines = append(lines, hex.EncodeToString(data))
	}

	content := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, "txs.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write tx dataset: %v", err)
	}

	return path
}
