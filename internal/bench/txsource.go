package bench

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
)

// TxCallback is called for each successfully decoded transaction.
// The line parameter is the 1-indexed line number in the source file.
type TxCallback func(line int, tx *types.Transaction) error

// TxSourceResult holds the results of reading a transaction file.
type TxSourceResult struct {
	TotalTxs uint64
	TotalGas uint64
}

// ReadTransactions reads transactions from a file and calls the callback for each.
// The file format is one hex-encoded RLP transaction per line.
// Empty lines are skipped. Lines may optionally have a 0x prefix.
//
// Returns the total transaction count and total gas on success.
// If decoding fails or the callback returns an error, reading stops immediately.
func ReadTransactions(path string, onTx TxCallback) (TxSourceResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return TxSourceResult{}, WrapError(ErrCodeIngestionDecode, "open tx file", err)
	}
	defer f.Close()

	return readTransactionsFromReader(f, onTx)
}

// readTransactionsFromReader is the internal implementation that reads from any io.Reader.
// This is separated for testing.
func readTransactionsFromReader(r io.Reader, onTx TxCallback) (TxSourceResult, error) {
	var result TxSourceResult

	scanner := bufio.NewScanner(r)
	// Allow large transaction lines (up to 32MB)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024)

	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}

		tx, err := decodeTxHex(raw)
		if err != nil {
			return result, WrapError(ErrCodeIngestionDecode,
				fmt.Sprintf("decode tx on line %d", line), err)
		}

		if onTx != nil {
			if err := onTx(line, tx); err != nil {
				return result, err
			}
		}

		result.TotalTxs++
		result.TotalGas += tx.Gas()
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return result, WrapError(ErrCodeIngestionDecode, "read tx file", err)
	}

	return result, nil
}

// decodeTxHex decodes a hex-encoded RLP transaction.
func decodeTxHex(hexStr string) (*types.Transaction, error) {
	// Strip optional 0x prefix
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")

	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}

	var tx types.Transaction
	if err := tx.UnmarshalBinary(blob); err != nil {
		return nil, fmt.Errorf("invalid RLP: %w", err)
	}

	return &tx, nil
}

// PeekTransactions reads up to n transactions from the file without consuming them.
// This is useful for preflight validation (chain ID checks, nonce analysis).
func PeekTransactions(path string, n int) ([]*types.Transaction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, WrapError(ErrCodeIngestionDecode, "open tx file for peek", err)
	}
	defer f.Close()

	var txs []*types.Transaction

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024)

	line := 0
	for scanner.Scan() && len(txs) < n {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}

		tx, err := decodeTxHex(raw)
		if err != nil {
			return txs, WrapError(ErrCodeIngestionDecode,
				fmt.Sprintf("decode tx on line %d", line), err)
		}

		txs = append(txs, tx)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return txs, WrapError(ErrCodeIngestionDecode, "read tx file", err)
	}

	return txs, nil
}

// CountTransactions counts the total number of transactions in the file
// without fully decoding them. This is faster than ReadTransactions when
// you only need the count.
func CountTransactions(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, WrapError(ErrCodeIngestionDecode, "open tx file for count", err)
	}
	defer f.Close()

	var count uint64
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024)

	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw != "" {
			count++
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return count, WrapError(ErrCodeIngestionDecode, "read tx file", err)
	}

	return count, nil
}
