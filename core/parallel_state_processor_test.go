package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetadata(t *testing.T) {
	t.Parallel()

	correctTxDependency := [][]uint64{{}, {0}, {}, {1}, {3}, {}, {0, 2}, {5}, {}, {8}}
	wrongTxDependency := [][]uint64{{0}}
	wrongTxDependencyCircular := [][]uint64{{}, {2}, {1}}
	wrongTxDependencyOutOfRange := [][]uint64{{}, {}, {3}}

	var temp map[int][]int

	temp = GetDeps(correctTxDependency)
	assert.Equal(t, true, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependency)
	assert.Equal(t, false, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependencyCircular)
	assert.Equal(t, false, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependencyOutOfRange)
	assert.Equal(t, false, VerifyDeps(temp))
}

func TestNegativeDependencyFromUint64Overflow(t *testing.T) {
	t.Parallel()

	// 0xFFFFFFFFFFFFFFFF casts to -1 when converted to int
	// This should be rejected as an invalid dependency
	txDependencyWithMaxUint64 := [][]uint64{{}, {0xFFFFFFFFFFFFFFFF}}

	deps := GetDeps(txDependencyWithMaxUint64)

	// VerifyDeps should return false because dependency value becomes -1 after casting
	// Currently fails: VerifyDeps incorrectly returns true (missing depTx < 0 check)
	// After fix: VerifyDeps will correctly return false
	assert.Equal(t, false, VerifyDeps(deps), "Dependencies with negative values (from uint64 overflow) should be invalid")
}
