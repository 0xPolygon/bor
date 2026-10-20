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
	RateLimited
	ServingRefused
	UnknownTransactionDelivery
	WitnessAnnouncementStrike
	WitnessBodyStrike
	reasonCount
)

var reasonNames = [...]string{
	"none", "invalid-encoding", "invalid-block", "invalid-transaction",
	"invalid-witness-announcement", "invalid-witness-body", "announcement-volume",
	"announcement-repetition", "transaction-volume", "block-volume", "request-volume",
	"downloader-failure", "fetcher-drop", "legacy-jail", "serving-volume", "serving-repetition",
	"rate-limited", "serving-refused", "unknown-transaction-delivery", "witness-announcement-strike", "witness-body-strike",
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
	// Pooled requests do not share the native block-request limiter.
	TransactionRequests
	familyCount
)

// Evidence belongs to one delivery. Reason takes precedence over traffic scoring
// so a malformed delivery is not also scored for exceeding a traffic allowance.
type Evidence struct {
	Activity    ActivityEvent
	Refused     bool
	Reason      Reason
	Family      Family
	Items       uint64
	Bytes       uint64
	Hashes      []common.Hash
	ObjectBytes []uint64
}

type Snapshot struct {
	Mode         string                `json:"mode"`
	Risk         uint64                `json:"risk"`
	Action       string                `json:"wouldAction"`
	Windows      map[string]uint64     `json:"reasonWindows"`
	Contribution *ContributionSnapshot `json:"contribution"`
}

type allowance struct {
	items  uint64
	bytes  uint64 // Zero leaves bytes diagnostic-only.
	reason Reason
}

// Observation profiles include native burst + rate*window where available.
// Profiles without a native limiter remain provisional.
var allowances = [familyCount]allowance{
	// 64 hashes/s plus the 256-hash burst.
	BlockAnnouncements: {896, 1 << 20, AnnouncementVolume},
	// No native limiter covers pooled transaction announcements or deliveries.
	TransactionAnnouncements: {163840, 16 << 20, AnnouncementVolume},
	Transactions:             {32768, 64 << 20, TransactionVolume},
	// Native 16 message/s and 16 MiB/s rates plus the 64-message and 64-MiB bursts.
	Blocks: {224, 224 << 20, BlockVolume},
	// Native block-data requests have a count bucket and per-message size checks, but no
	// aggregate byte budget that could establish request-byte abuse.
	Requests: {768, 0, RequestVolume}, // 64 requests/s plus the native 128-request burst.
	// Separate provisional budget: reply reservations do not bound lookup rate.
	TransactionRequests: {768, 0, RequestVolume},
}

func weight(reason Reason) uint64 {
	switch reason {
	case InvalidEncoding, InvalidBlock, InvalidTransaction, InvalidWitnessAnnouncement, InvalidWitnessBody:
		return 60
	case RateLimited, AnnouncementVolume, AnnouncementRepetition, TransactionVolume, BlockVolume, RequestVolume, ServingVolume, ServingRepetition:
		return 20
	default:
		return 0
	}
}
