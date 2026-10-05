// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

// Package peerpolicy collects bounded, local peer reputation evidence.
// It does not disconnect peers, reserve resources or change protocol validation.
package peerpolicy

import "github.com/ethereum/go-ethereum/common"

type Reason uint8

const (
	None Reason = iota
	InvalidEncoding
	InvalidBlock
	InvalidTransaction
	InvalidWitnessAnnouncement
	InvalidWitnessBody
	AnnouncementVolume
	AnnouncementRepetition
	TransactionVolume
	BlockVolume
	RequestVolume
	DownloaderFailure
	FetcherDrop
	LegacyJail
	ServingVolume
	ServingRepetition
	reasonCount
)

var reasonNames = [...]string{
	"none", "invalid-encoding", "invalid-block", "invalid-transaction",
	"invalid-witness-announcement", "invalid-witness-body", "announcement-volume",
	"announcement-repetition", "transaction-volume", "block-volume", "request-volume",
	"downloader-failure", "fetcher-drop", "legacy-jail", "serving-volume", "serving-repetition",
}

func (r Reason) String() string {
	if r >= reasonCount {
		return "unknown"
	}
	return reasonNames[r]
}

type Family uint8

const (
	Other Family = iota
	BlockAnnouncements
	TransactionAnnouncements
	Transactions
	Blocks
	Requests
	BodyReplies
	familyCount
)

// Evidence belongs to one delivery. Reason takes precedence over traffic scoring
// so a malformed delivery is not also scored for exceeding a traffic allowance.
type Evidence struct {
	Reason      Reason
	Family      Family
	Items       uint64
	Bytes       uint64
	Hashes      []common.Hash
	ObjectBytes []uint64
}

type Snapshot struct {
	Mode    string            `json:"mode"`
	Risk    uint64            `json:"risk"`
	Action  string            `json:"wouldAction"`
	Windows map[string]uint64 `json:"reasonWindows"`
}

type allowance struct {
	items  uint64
	bytes  uint64
	reason Reason
}

// These are observation profiles, not production enforcement thresholds.
var allowances = [familyCount]allowance{
	BlockAnnouncements:       {640, 1 << 20, AnnouncementVolume},
	TransactionAnnouncements: {163840, 16 << 20, AnnouncementVolume},
	Transactions:             {32768, 64 << 20, TransactionVolume},
	Blocks:                   {160, 160 << 20, BlockVolume},
	Requests:                 {640, 16 << 20, RequestVolume},
	BodyReplies:              {10240, 160 << 20, ServingVolume},
}

func weight(reason Reason) uint64 {
	switch reason {
	case InvalidEncoding, InvalidBlock, InvalidTransaction, InvalidWitnessAnnouncement, InvalidWitnessBody:
		return 60
	case AnnouncementVolume, AnnouncementRepetition, TransactionVolume, BlockVolume, RequestVolume, ServingVolume, ServingRepetition:
		return 20
	default:
		return 0
	}
}
