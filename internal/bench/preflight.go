package bench

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// PreflightResult contains the results of preflight validation.
type PreflightResult struct {
	// DatasetChainID is the chain ID found in the dataset transactions.
	// May be nil if transactions don't specify a chain ID.
	DatasetChainID *big.Int

	// SenderNonces maps sender addresses to their expected starting nonces.
	SenderNonces map[common.Address]uint64

	// TxCount is the number of transactions examined.
	TxCount int
}

// PreflightDataset validates the transaction dataset before starting the benchmark.
// It checks:
//   - All transactions can be decoded
//   - Transaction chain IDs match the expected chain ID
//   - Sender nonces are contiguous (no gaps)
//
// The function examines the first peekCount transactions.
func PreflightDataset(txsPath string, expectedChainID *big.Int, peekCount int) (*PreflightResult, error) {
	txs, err := PeekTransactions(txsPath, peekCount)
	if err != nil {
		return nil, err
	}

	if len(txs) == 0 {
		return nil, NewBenchError(ErrCodeIngestionDecode, "transaction file is empty")
	}

	result := &PreflightResult{
		SenderNonces: make(map[common.Address]uint64),
		TxCount:      len(txs),
	}

	// Track nonces per sender to detect gaps
	senderNonces := make(map[common.Address][]uint64)
	signer := types.LatestSignerForChainID(expectedChainID)

	for i, tx := range txs {
		// Check chain ID
		txChainID := tx.ChainId()
		if txChainID != nil && txChainID.Sign() > 0 {
			if result.DatasetChainID == nil {
				result.DatasetChainID = txChainID
			}

			if expectedChainID != nil && txChainID.Cmp(expectedChainID) != 0 {
				return nil, &BenchError{
					Code: ErrCodeChainIDMismatch,
					Message: fmt.Sprintf("transaction %d has chain ID %s, expected %s",
						i+1, txChainID.String(), expectedChainID.String()),
				}
			}
		}

		// Extract sender and nonce
		sender, err := types.Sender(signer, tx)
		if err != nil {
			log.Warn("Could not extract sender from tx", "index", i, "err", err)
			continue
		}

		senderNonces[sender] = append(senderNonces[sender], tx.Nonce())
	}

	// Check for nonce gaps in each sender's transactions
	for sender, nonces := range senderNonces {
		if len(nonces) == 0 {
			continue
		}

		// Sort nonces
		sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })

		// Record the minimum nonce (expected starting nonce)
		result.SenderNonces[sender] = nonces[0]

		// Check for gaps within the peeked transactions
		for i := 1; i < len(nonces); i++ {
			if nonces[i] != nonces[i-1]+1 && nonces[i] != nonces[i-1] {
				// There's a gap - this might be intentional if not all txs are from this sender
				// Just log a warning, don't fail
				log.Warn("Nonce gap detected in preflight",
					"sender", sender,
					"prevNonce", nonces[i-1],
					"nextNonce", nonces[i],
					"gap", nonces[i]-nonces[i-1]-1)
			}
		}
	}

	log.Info("Preflight dataset check passed",
		"txsExamined", result.TxCount,
		"uniqueSenders", len(result.SenderNonces),
		"datasetChainID", result.DatasetChainID)

	return result, nil
}

// ValidateNonceAgainstChain checks that sender nonces in the dataset align with
// the current chain state. This should be called after the server is started.
func ValidateNonceAgainstChain(
	preflight *PreflightResult,
	getNonce func(addr common.Address) uint64,
) error {
	var nonceErrors []string

	for sender, expectedNonce := range preflight.SenderNonces {
		chainNonce := getNonce(sender)

		if expectedNonce < chainNonce {
			// Transactions have nonces that are already used
			nonceErrors = append(nonceErrors,
				fmt.Sprintf("sender %s: dataset starts at nonce %d, chain is at %d (txs would be rejected)",
					sender.Hex(), expectedNonce, chainNonce))
		} else if expectedNonce > chainNonce {
			// Gap between chain and first tx - all txs will be queued
			nonceErrors = append(nonceErrors,
				fmt.Sprintf("sender %s: dataset starts at nonce %d, chain is at %d (gap of %d, txs will be queued)",
					sender.Hex(), expectedNonce, chainNonce, expectedNonce-chainNonce))
		}
	}

	if len(nonceErrors) > 0 {
		// Log all errors for diagnostics
		for _, e := range nonceErrors {
			log.Error("Nonce validation failed", "issue", e)
		}

		return &BenchError{
			Code:    ErrCodeNonceGap,
			Message: fmt.Sprintf("nonce validation failed for %d sender(s); first issue: %s", len(nonceErrors), nonceErrors[0]),
		}
	}

	log.Info("Nonce validation passed", "senders", len(preflight.SenderNonces))
	return nil
}

// genesisChainConfig is a minimal struct for extracting chain ID from genesis.
type genesisChainConfig struct {
	Config struct {
		ChainID *big.Int `json:"chainId"`
	} `json:"config"`
}

// GetGenesisChainID extracts the chain ID from a genesis file.
func GetGenesisChainID(genesisPath string) (*big.Int, error) {
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("read genesis: %w", err)
	}

	var g genesisChainConfig
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("parse genesis: %w", err)
	}

	if g.Config.ChainID == nil {
		return nil, fmt.Errorf("genesis does not specify chainId")
	}

	return g.Config.ChainID, nil
}

// TxPoolStats holds txpool status information.
type TxPoolStats struct {
	Pending int
	Queued  int
}

// ValidateTxPoolHealth checks that the txpool has runnable transactions.
// This should be called after ingestion to detect nonce gap issues.
func ValidateTxPoolHealth(stats TxPoolStats, totalIngested uint64) error {
	if totalIngested == 0 {
		return NewBenchError(ErrCodeIngestionDecode, "no transactions were ingested")
	}

	if stats.Pending == 0 && stats.Queued > 0 {
		return &BenchError{
			Code: ErrCodeNoRunnableTxs,
			Message: fmt.Sprintf(
				"all %d transactions are queued (non-executable), none pending; "+
					"likely nonce gap between dataset and chain state",
				stats.Queued),
		}
	}

	if stats.Pending == 0 && stats.Queued == 0 {
		return &BenchError{
			Code: ErrCodeNoRunnableTxs,
			Message: fmt.Sprintf(
				"txpool is empty after ingesting %d transactions; "+
					"transactions may have been rejected or already mined",
				totalIngested),
		}
	}

	log.Info("TxPool health check passed",
		"pending", stats.Pending,
		"queued", stats.Queued,
		"ingested", totalIngested)

	return nil
}
