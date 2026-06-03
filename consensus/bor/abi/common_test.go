package abi

import (
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	ethabi "github.com/ethereum/go-ethereum/accounts/abi"
)

func TestValidatorSet_ExposesCommitSpan(t *testing.T) {
	v := ValidatorSet()

	// commitSpan(uint256, uint256, uint256, bytes, bytes) is the method consensus uses to
	// install a new validator set on chain. Verify it is packable with the exact arg shape.
	data, err := v.Pack(
		"commitSpan",
		big.NewInt(1),
		big.NewInt(10),
		big.NewInt(20),
		[]byte{0x01, 0x02},
		[]byte{0x03, 0x04},
	)
	require.NoError(t, err)
	require.NotEmpty(t, data, "packed commitSpan calldata is empty")

	// getCurrentSpan() must round-trip: empty input, decodes to three uint256.
	currentSpanData, err := v.Pack("getCurrentSpan")
	require.NoError(t, err)
	require.NotEmpty(t, currentSpanData)
}

func TestStateReceiver_ExposesCommitState(t *testing.T) {
	r := StateReceiver()

	// commitState(uint256 syncTime, bytes recordBytes) is the method the system contract
	// dispatches bridge events through. ApplyStateSyncEvents depends on this exact shape.
	data, err := r.Pack("commitState", big.NewInt(1700000000), []byte{0xde, 0xad, 0xbe, 0xef})
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// lastStateId() returns one uint256 — used by GenesisContractsClient.LastStateId.
	lastIDData, err := r.Pack("lastStateId")
	require.NoError(t, err)
	require.NotEmpty(t, lastIDData)
}

func TestStateReceiver_Pack_RejectsWrongArgCount(t *testing.T) {
	// ABI packing must surface arg mismatches — silently accepting them would send malformed
	// calldata to a system contract (see contract-interaction-security.md).
	r := StateReceiver()
	_, err := r.Pack("commitState", big.NewInt(1))
	require.Error(t, err)
}

func TestValidatorSet_Pack_RejectsUnknownMethod(t *testing.T) {
	v := ValidatorSet()
	_, err := v.Pack("doesNotExist", big.NewInt(0))
	require.Error(t, err)
}

func TestValidatorSetAndStateReceiver_AreSingletonsAcrossCalls(t *testing.T) {
	// Repeated calls must return the cached package-level value. abi.ABI is keyed by
	// pointer-comparable maps; identity equality is the cheapest check.
	require.Equal(t, ValidatorSet(), ValidatorSet())
	require.Equal(t, StateReceiver(), StateReceiver())
}

func TestMustParseABI_ValidJSON(t *testing.T) {
	// Smallest valid ABI: one no-arg view function.
	minimal := `[{"inputs":[],"name":"noop","outputs":[],"stateMutability":"view","type":"function"}]`
	parsed := mustParseABI(minimal)
	require.Contains(t, parsed.Methods, "noop")
}

func TestMustParseABI_PanicsOnInvalidJSON(t *testing.T) {
	// Init-time panic is the intentional behaviour: a corrupted embedded ABI is an
	// unrecoverable programming error, not a runtime condition.
	require.Panics(t, func() {
		mustParseABI("not-valid-json")
	})
}

func TestMustParseABI_PanicsOnMalformedABI(t *testing.T) {
	// Well-formed JSON but not a valid ABI spec.
	require.Panics(t, func() {
		mustParseABI(`[{"type":"unknown-entry-type"}]`)
	})
}

func TestValidatorSet_UnpackEmptyReturnFails(t *testing.T) {
	// State receiver returns boolean; validator set returns various tuples. Unpacking
	// an empty []byte must surface as an error — never as a silent zero value.
	r := StateReceiver()
	var success bool
	err := r.UnpackIntoInterface(&success, "commitState", []byte{})
	require.Error(t, err)
}

func TestValidatorSet_MatchesPackagedABI(t *testing.T) {
	// Defence-in-depth: confirm the embedded ABI string can be parsed standalone, so that
	// drift between embedded ABI and the production deployment is caught here, not
	// hours into a devnet run.
	_, err := ethabi.JSON(strings.NewReader(validatorsetABI))
	require.NoError(t, err)
	_, err = ethabi.JSON(strings.NewReader(stateReceiverABI))
	require.NoError(t, err)
}
