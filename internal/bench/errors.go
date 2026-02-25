package bench

import "fmt"

// ErrorCode provides structured error classification for benchmark failures.
// These codes are included in the report JSON to enable programmatic failure analysis.
type ErrorCode string

const (
	// ErrCodeNone indicates no error.
	ErrCodeNone ErrorCode = ""

	// ErrCodeIngestionDecode indicates a transaction hex decoding failure.
	ErrCodeIngestionDecode ErrorCode = "ingestion_decode_error"

	// ErrCodeTxPoolReject indicates the txpool rejected a transaction.
	ErrCodeTxPoolReject ErrorCode = "txpool_reject_error"

	// ErrCodeChainIDMismatch indicates transaction chain ID doesn't match genesis.
	ErrCodeChainIDMismatch ErrorCode = "chain_id_mismatch"

	// ErrCodeNonceGap indicates a nonce gap causing all txs to be queued.
	ErrCodeNonceGap ErrorCode = "nonce_gap_all_queued"

	// ErrCodeSignerNotAuthorized indicates the miner address is not authorized.
	ErrCodeSignerNotAuthorized ErrorCode = "signer_not_authorized"

	// ErrCodeMiningStalled indicates no blocks were produced within the timeout.
	ErrCodeMiningStalled ErrorCode = "mining_stalled"

	// ErrCodeNoRunnableTxs indicates no transactions became runnable after ingestion.
	ErrCodeNoRunnableTxs ErrorCode = "no_runnable_txs"

	// ErrCodeConfigLoad indicates a configuration loading failure.
	ErrCodeConfigLoad ErrorCode = "config_load_error"

	// ErrCodeServerStart indicates the node failed to start.
	ErrCodeServerStart ErrorCode = "server_start_error"

	// ErrCodeTimeout indicates the benchmark timed out waiting for completion.
	ErrCodeTimeout ErrorCode = "timeout"
)

// BenchError wraps an error with a structured error code.
type BenchError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *BenchError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *BenchError) Unwrap() error {
	return e.Cause
}

// NewBenchError creates a new BenchError with the given code and message.
func NewBenchError(code ErrorCode, message string) *BenchError {
	return &BenchError{Code: code, Message: message}
}

// WrapError wraps an existing error with a BenchError.
func WrapError(code ErrorCode, message string, cause error) *BenchError {
	return &BenchError{Code: code, Message: message, Cause: cause}
}

// GetErrorCode extracts the ErrorCode from an error, returning ErrCodeNone if
// the error is not a BenchError.
func GetErrorCode(err error) ErrorCode {
	if err == nil {
		return ErrCodeNone
	}
	if be, ok := err.(*BenchError); ok {
		return be.Code
	}
	return ErrCodeNone
}
