package pevmbridge

import (
	"fmt"
	"sync"
)

// GasDebug tracks gas consumption per transaction for debugging
type GasDebug struct {
	mu          sync.Mutex
	txGas       map[int]uint64 // tx index -> gas used
	txGasDetail map[int]string // tx index -> detailed gas breakdown
}

var globalGasDebug = &GasDebug{
	txGas:       make(map[int]uint64),
	txGasDetail: make(map[int]string),
}

// LogGas logs gas consumption for a transaction
func LogGas(txIdx int, gasUsed uint64, details string) {
	globalGasDebug.mu.Lock()
	defer globalGasDebug.mu.Unlock()
	globalGasDebug.txGas[txIdx] = gasUsed
	globalGasDebug.txGasDetail[txIdx] = details
}

// GetGasReport returns a formatted gas report
func GetGasReport() string {
	globalGasDebug.mu.Lock()
	defer globalGasDebug.mu.Unlock()
	
	report := "=== Gas Debug Report ===\n"
	for i := 0; i < len(globalGasDebug.txGas); i++ {
		if gas, ok := globalGasDebug.txGas[i]; ok {
			report += fmt.Sprintf("Tx %d: gas_used=%d\n", i, gas)
			if details, ok := globalGasDebug.txGasDetail[i]; ok {
				report += fmt.Sprintf("  Details: %s\n", details)
			}
		}
	}
	return report
}

// ResetGasDebug clears the gas debug data
func ResetGasDebug() {
	globalGasDebug.mu.Lock()
	defer globalGasDebug.mu.Unlock()
	globalGasDebug.txGas = make(map[int]uint64)
	globalGasDebug.txGasDetail = make(map[int]string)
}

