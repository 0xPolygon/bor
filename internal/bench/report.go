package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
)

// Report is the benchmark output contract.
type Report struct {
	// Run identification
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`

	// Timing (populated at completion)
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMs int64     `json:"duration_ms"`

	// Build info
	GitCommit string `json:"git_commit"`

	// Input paths (absolute)
	ConfigPath  string `json:"config_path"`
	GenesisPath string `json:"genesis_path"`
	TxsPath     string `json:"txs_path"`

	// Input hashes for reproducibility
	ConfigSHA256  string `json:"config_sha256"`
	GenesisSHA256 string `json:"genesis_sha256"`
	TxsSHA256     string `json:"txs_sha256"`

	// Metrics
	TotalTxsInput uint64  `json:"total_txs_input"`
	TotalTxsMined uint64  `json:"total_txs_mined"`
	BlocksMined   uint64  `json:"blocks_mined"`
	TxPerSec      float64 `json:"tx_per_sec"`
	GasPerSec     float64 `json:"gas_per_sec"`

	// Status
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	ErrorCode ErrorCode `json:"error_code,omitempty"`

	// Environment
	Environment Environment `json:"environment"`
}

// Environment captures host and runtime metadata.
type Environment struct {
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	CPUCount int    `json:"cpu_count"`
}

// NewReport creates a new report initialized with start time and environment.
func NewReport(opts Options) *Report {
	now := time.Now().UTC()
	runID := opts.RunID
	if runID == "" {
		runID = now.Format("20060102T150405.000000000Z07:00")
	}

	return &Report{
		RunID:       runID,
		StartedAt:   now,
		GitCommit:   gitCommit(),
		ConfigPath:  absPath(opts.ConfigPath),
		GenesisPath: absPath(opts.GenesisPath),
		TxsPath:     absPath(opts.TxsPath),
		Environment: currentEnvironment(),
	}
}

// SetFailure marks the report as failed with the given error.
func (r *Report) SetFailure(err error) {
	r.finish()
	r.Success = false
	if err != nil {
		r.Error = err.Error()
		r.ErrorCode = GetErrorCode(err)
	}
}

// SetSuccess marks the report as successful and computes final metrics.
func (r *Report) SetSuccess(totalGas uint64) {
	r.finish()
	r.Success = true

	seconds := r.FinishedAt.Sub(r.StartedAt).Seconds()
	if seconds > 0 {
		r.TxPerSec = float64(r.TotalTxsMined) / seconds
		r.GasPerSec = float64(totalGas) / seconds
	}
}

func (r *Report) finish() {
	r.FinishedAt = time.Now().UTC()
	r.DurationMs = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
}

// HashInputFiles computes SHA256 hashes for the input files.
func (r *Report) HashInputFiles(opts Options) error {
	var err error

	r.ConfigSHA256, err = fileSHA256(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("hash config: %w", err)
	}

	r.GenesisSHA256, err = fileSHA256(opts.GenesisPath)
	if err != nil {
		return fmt.Errorf("hash genesis: %w", err)
	}

	r.TxsSHA256, err = fileSHA256(opts.TxsPath)
	if err != nil {
		return fmt.Errorf("hash tx file: %w", err)
	}

	return nil
}

// Write serializes the report to the given path as JSON.
func (r *Report) Write(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create report file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	return nil
}

// fileSHA256 computes the SHA256 hash of a file.
func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// absPath returns the absolute path, or the original if resolution fails.
func absPath(p string) string {
	out, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return out
}

// gitCommit extracts the VCS revision from build info.
func gitCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// currentEnvironment returns the current runtime environment.
func currentEnvironment() Environment {
	return Environment{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		CPUCount: runtime.NumCPU(),
	}
}
