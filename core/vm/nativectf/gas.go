package nativectf

// External-entrypoint gas for getCollectionId (parent==0) under the current bor
// fork. Derived from the real mainnet CTF bytecode: gasUsed is exactly determined
// by (QR-loop iterations, parityFlipped, bit254) — verified over thousands of
// inputs with zero collisions. TestGetCollectionId_GasEquivalence_Parent0 is the
// gate: if a future fork changes opcode gas, that test fails and these update.
const (
	extBaseGas    uint64 = 711  // gas(iters=1, no branches) = extBaseGas + extPerIterGas
	extPerIterGas uint64 = 7419 // marginal gas per QR-loop iteration
)

// branchOffset[parityFlipped][bit254] — the two conditional branches interact, so
// this is a 2x2 table rather than two additive deltas.
var branchOffset = [2][2]uint64{
	{0, 17}, // parityFlipped=false: {bit254=false, bit254=true}
	{47, 37}, // parityFlipped=true:  {bit254=false, bit254=true}
}

// ExternalCallGas returns the exact gas the EVM charges for an external
// getCollectionId(parent=0) call, given the facts GetCollectionId returns.
func ExternalCallGas(iters int, parityFlipped, bit254 bool) uint64 {
	pf, b := 0, 0
	if parityFlipped {
		pf = 1
	}
	if bit254 {
		b = 1
	}
	return extBaseGas + extPerIterGas*uint64(iters) + branchOffset[pf][b]
}
