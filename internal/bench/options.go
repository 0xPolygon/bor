package bench

// Options controls a benchmark run.
type Options struct {
	// ConfigPath is the path to a Bor TOML configuration file.
	ConfigPath string

	// GenesisPath is the path to the genesis JSON file.
	GenesisPath string

	// TxsPath is the path to the transaction dataset file (one hex tx per line).
	TxsPath string

	// OutPath is the path where the report JSON will be written.
	OutPath string

	// DataDir optionally overrides the data directory for this run.
	// If empty, a temporary directory is created and cleaned up after the run.
	DataDir string

	// RunID is an optional caller-provided identifier for this run.
	// If empty, a timestamp-based ID is generated.
	RunID string
}

// Validate checks that required options are set.
func (o Options) Validate() error {
	if o.ConfigPath == "" {
		return &ValidationError{Field: "ConfigPath", Message: "config path is required"}
	}
	if o.GenesisPath == "" {
		return &ValidationError{Field: "GenesisPath", Message: "genesis path is required"}
	}
	if o.TxsPath == "" {
		return &ValidationError{Field: "TxsPath", Message: "txs path is required"}
	}
	if o.OutPath == "" {
		return &ValidationError{Field: "OutPath", Message: "out path is required"}
	}
	return nil
}

// ValidationError indicates a missing or invalid option.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
