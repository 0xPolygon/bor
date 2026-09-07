//go:build devnet_repro

package eth

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/stretchr/testify/require"
)

// newTestEthereumForDebugAPI creates a minimal *Ethereum instance for testing the DebugAPI.
// It sets up only the fields required by the debug RPC methods being tested.
func newTestEthereumForDebugAPI(t *testing.T) *Ethereum {
	return &Ethereum{
		miner: &miner.Miner{},
	}
}

// TestDebugAPIStageFakeTxForwardsToMiner verifies the RPC method is a thin,
// correctly-typed pass-through to (*miner.Miner).StageFakeTx — the actual
// injection behavior is covered by miner package tests (Task 1).
func TestDebugAPIStageFakeTxForwardsToMiner(t *testing.T) {
	// fakeTxPending is a package-level (process-global) var in package miner:
	// this test stages a spec but never runs a real block build to consume
	// it via devnetInjectFakeTx, so it must be drained here or it leaks into
	// whichever other test in this binary's process next builds a real block
	// (any worker created by any eth-package test shares the same miner
	// package global). See miner.ClearPendingFakeTxForTest's doc comment.
	t.Cleanup(miner.ClearPendingFakeTxForTest)

	eth := newTestEthereumForDebugAPI(t)

	api := NewDebugAPI(eth)

	value := hexutil.Big(*common.Big0)
	spec := miner.FakeTxSpec{
		From:     common.HexToAddress("0x0000000000000000000000000000000000009999"),
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    &value,
		GasLimit: hexutil.Uint64(21000),
	}

	// api.eth.p2pServer is nil in this minimal fixture: devnetPeerCount
	// treats that as zero peers (see its doc comment), so this call is not
	// itself exercising the I3 peer-count gate — that's covered by
	// TestDebugAPIStageFakeTxRefusesWhenPeersConnected below.
	err := api.StageFakeTx(spec)
	require.NoError(t, err)
}

// TestDebugAPIStageFakeTxRefusesWhenPeersConnected verifies StageFakeTx fails
// closed when the node has connected p2p peers (final review finding I3):
// the design's safety argument for "the fake block can't reach real mainnet
// peers" rests on p2p being fully disconnected, and this is a code-level
// backstop for that runbook step. devnetPeerCount is swapped for the
// duration of this test since constructing a real, running *p2p.Server with
// connected peers is impractical in a unit test.
func TestDebugAPIStageFakeTxRefusesWhenPeersConnected(t *testing.T) {
	original := devnetPeerCount
	devnetPeerCount = func(api *DebugAPI) int { return 3 }
	t.Cleanup(func() { devnetPeerCount = original })

	eth := newTestEthereumForDebugAPI(t)
	api := NewDebugAPI(eth)

	value := hexutil.Big(*common.Big0)
	spec := miner.FakeTxSpec{
		From:     common.HexToAddress("0x0000000000000000000000000000000000009999"),
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    &value,
		GasLimit: hexutil.Uint64(21000),
	}

	err := api.StageFakeTx(spec)
	require.Error(t, err, "StageFakeTx must refuse to stage while peers are connected")
}

// TestDebugAPIDrainSlowOpcodeGaps verifies the RPC method returns a non-nil
// (possibly empty) slice, even when nothing has been recorded yet.
func TestDebugAPIDrainSlowOpcodeGaps(t *testing.T) {
	eth := newTestEthereumForDebugAPI(t)
	api := NewDebugAPI(eth)

	gaps := api.DrainSlowOpcodeGaps()
	require.NotNil(t, gaps) // empty slice, not nil, when nothing has been recorded yet
}

// TestFakeTxSpecDecodesRunbookPayload verifies FakeTxSpec decodes via
// encoding/json from the exact hex/JSON shape the debug_stageFakeTx runbook
// invocation sends (final review finding C1): a plain Go []byte/*big.Int/
// uint64-typed struct silently fails to decode this shape at all (base64 for
// []byte, no "0x..." support for *big.Int and uint64), so this test pins the
// wire-format contract directly rather than relying on unit tests of
// StageFakeTx alone, which construct FakeTxSpec in Go and would never
// exercise the JSON boundary.
func TestFakeTxSpecDecodesRunbookPayload(t *testing.T) {
	raw := []byte(`{"from":"0x0000000000000000000000000000000000009999","to":"0x00000000000000000000000000000000000000aa","data":"0x1249c58b","value":"0x0","gasLimit":"0x7a120"}`)

	var spec miner.FakeTxSpec
	err := json.Unmarshal(raw, &spec)
	require.NoError(t, err)

	require.Equal(t, common.HexToAddress("0x0000000000000000000000000000000000009999"), spec.From)
	require.Equal(t, common.HexToAddress("0x00000000000000000000000000000000000000aa"), spec.To)
	require.Equal(t, hexutil.Bytes(common.FromHex("0x1249c58b")), spec.Data)
	require.Equal(t, uint64(0), spec.Value.ToInt().Uint64())
	require.Equal(t, uint64(500000), uint64(spec.GasLimit))
}
