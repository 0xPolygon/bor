package miner

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/contract/registrytest"
)

// TestMiner_ReservedRegistry_NilByDefault verifies the accessor on a Miner
// (and a typed-nil receiver) returns nil so consumers don't panic.
func TestMiner_ReservedRegistry_NilByDefault(t *testing.T) {
	var miner *Miner
	require.Nil(t, miner.ReservedRegistry())

	miner = &Miner{worker: &worker{}}
	require.Nil(t, miner.ReservedRegistry())
}

// TestMiner_ReservedRegistry_PropagatesToWorker confirms SetReservedRegistry
// installs the handle on both the Miner and its worker, and that the handle
// answers EVM-backed queries identically to the harness reader.
func TestMiner_ReservedRegistry_PropagatesToWorker(t *testing.T) {
	h := registrytest.NewHarness(t)

	w := &worker{}
	miner := &Miner{worker: w}
	miner.SetReservedRegistry(h.Reader)

	require.NotNil(t, miner.ReservedRegistry())
	require.Same(t, h.Reader, miner.ReservedRegistry())
	require.Same(t, h.Reader, w.reservedRegistry, "worker must mirror the miner's registry handle")

	reserved, err := miner.ReservedRegistry().IsReservedAddress(nil, 0, common.Hash{}, h.ReservedAddr)
	require.NoError(t, err)
	require.True(t, reserved)

	reserved, err = miner.ReservedRegistry().IsReservedAddress(nil, 0, common.Hash{}, h.UnreservedAddr)
	require.NoError(t, err)
	require.False(t, reserved)
}
