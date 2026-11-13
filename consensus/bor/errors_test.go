package bor

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
)

func TestMaxCheckpointLengthExceededError(t *testing.T) {
	t.Parallel()
	e := &MaxCheckpointLengthExceededError{Start: 100, End: 50000}
	msg := e.Error()
	require.Contains(t, msg, "100")
	require.Contains(t, msg, "50000")
	require.Contains(t, msg, "max allowed checkpoint length")
}

func TestMismatchingValidatorsError(t *testing.T) {
	t.Parallel()
	e := &MismatchingValidatorsError{
		Number:             42,
		ValidatorSetSnap:   []byte{0xaa, 0xbb},
		ValidatorSetHeader: []byte{0xcc, 0xdd},
	}
	msg := e.Error()
	require.Contains(t, msg, "42")
	require.Contains(t, msg, "aabb")
	require.Contains(t, msg, "ccdd")
	require.Contains(t, msg, "Mismatching validators")
}

func TestBlockTooSoonError(t *testing.T) {
	t.Parallel()
	e := &BlockTooSoonError{Number: 99, Succession: 3}
	msg := e.Error()
	require.Contains(t, msg, "99")
	require.Contains(t, msg, "3")
	require.Contains(t, msg, "too soon")
}

func TestUnauthorizedProposerError(t *testing.T) {
	t.Parallel()
	proposer := common.HexToAddress("0xdeadbeef").Bytes()
	e := &UnauthorizedProposerError{Number: 7, Proposer: proposer}
	msg := e.Error()
	require.Contains(t, msg, "7")
	require.True(t, strings.Contains(msg, "not a part of the producer set"))
}

func TestUnauthorizedSignerError(t *testing.T) {
	t.Parallel()
	signer := common.HexToAddress("0xbadcafe").Bytes()
	vals := []*valset.Validator{
		{Address: common.HexToAddress("0x1"), VotingPower: 10},
		{Address: common.HexToAddress("0x2"), VotingPower: 20},
	}
	e := &UnauthorizedSignerError{Number: 55, Signer: signer, AllowedSigners: vals}
	msg := e.Error()
	require.Contains(t, msg, "55")
	require.Contains(t, msg, "not a part of the producer set")
	require.Contains(t, msg, "Allowed signers")
}

func TestWrongDifficultyError(t *testing.T) {
	t.Parallel()
	signer := common.HexToAddress("0xabcdef").Bytes()
	e := &WrongDifficultyError{Number: 10, Expected: 5, Actual: 3, Signer: signer}
	msg := e.Error()
	require.Contains(t, msg, "10")
	require.Contains(t, msg, "expected: 5")
	require.Contains(t, msg, "actual 3")
}

func TestInvalidStateReceivedError(t *testing.T) {
	t.Parallel()
	now := time.Now()
	event := &clerk.EventRecordWithTime{
		EventRecord: clerk.EventRecord{
			ID:      42,
			ChainID: "137",
		},
		Time: now,
	}
	e := &InvalidStateReceivedError{
		Number:      100,
		LastStateID: 41,
		To:          &now,
		Event:       event,
	}
	msg := e.Error()
	require.Contains(t, msg, "100")
	require.Contains(t, msg, "41")
	require.Contains(t, msg, "invalid event")
}

// Callers outside this package tell a provably unauthorized header apart
// from an inconclusive verification failure through the consensus sentinel.
func TestUnauthorizedErrorsMatchSentinel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "signer", err: &UnauthorizedSignerError{Number: 5, Signer: common.Address{0x1}.Bytes()}, want: true},
		{name: "proposer", err: &UnauthorizedProposerError{Number: 5, Proposer: common.Address{0x1}.Bytes()}, want: true},
		{name: "wrapped signer", err: fmt.Errorf("verify: %w", &UnauthorizedSignerError{Number: 5}), want: true},
		{name: "signer on an unsettled span", err: &UnauthorizedSignerError{Number: 5, spanUnsettled: true}, want: false},
		{name: "proposer on an unsettled span", err: &UnauthorizedProposerError{Number: 5, spanUnsettled: true}, want: false},
		{name: "wrong difficulty", err: &WrongDifficultyError{Number: 5}, want: false},
		{name: "unknown ancestor", err: consensus.ErrUnknownAncestor, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, errors.Is(tc.err, consensus.ErrUnauthorizedSigner))
		})
	}

	var signerErr *UnauthorizedSignerError
	require.True(t, errors.As(fmt.Errorf("x: %w", &UnauthorizedSignerError{Number: 7}), &signerErr), "errors.As must keep working")
	require.Equal(t, uint64(7), signerErr.Number)
}
