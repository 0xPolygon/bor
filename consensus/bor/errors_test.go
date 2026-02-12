package bor

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
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
