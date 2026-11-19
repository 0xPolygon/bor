// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package downloader

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// SetFailureObserver installs a non-blocking observer before peer registration.
// A sync failure alone does not prove that this peer supplied invalid data.
func (d *Downloader) SetFailureObserver(observer func(string)) {
	d.failureObserver = observer
}

// SetInvalidResponseObserver reports provable request/response mismatches.
// Install before peer registration; the callback must not block.
func (d *Downloader) SetInvalidResponseObserver(observer func(string)) {
	d.invalidResponseObserver = observer
}

func (d *Downloader) observeInvalidResponse(id, check string) {
	metrics.GetOrRegisterCounter("eth/peerpolicy/downloader/invalid_response/"+check, nil).Inc(1)
	if d.invalidResponseObserver != nil {
		d.invalidResponseObserver(id)
	}
}

// Short or empty replies can reflect unavailable data.
func (d *Downloader) observeExcessHeaders(id string, got, want int) {
	if got > want {
		d.observeInvalidResponse(id, "excess_headers")
	}
}

// Preserve native validation against the current pivot, but suppress evidence
// when the pivot changed while the request was in flight.
func (d *Downloader) verifyNextPivot(p *peerConnection, headers []*types.Header, requestPivot, currentPivot uint64) error {
	if have, want := headers[0].Number.Uint64(), currentPivot+uint64(fsMinFullBlocks); have != want {
		log.Warn("Peer sent invalid next pivot", "have", have, "want", want)
		d.observePivotResponse(p.id, "pivot_number", requestPivot, currentPivot)

		return fmt.Errorf("%w: next pivot number %d != requested %d", errInvalidChain, have, want)
	}

	if have, want := headers[1].Number.Uint64(), currentPivot+2*uint64(fsMinFullBlocks)-8; have != want {
		log.Warn("Peer sent invalid pivot confirmer", "have", have, "want", want)
		d.observePivotResponse(p.id, "pivot_confirmer", requestPivot, currentPivot)

		return fmt.Errorf("%w: next pivot confirmer number %d != requested %d", errInvalidChain, have, want)
	}

	return nil
}

func (d *Downloader) observePivotResponse(id, check string, requestPivot, currentPivot uint64) {
	if requestPivot != currentPivot {
		metrics.GetOrRegisterCounter("eth/peerpolicy/downloader/pivot_moved", nil).Inc(1)
		return
	}

	d.observeInvalidResponse(id, check)
}

func (d *Downloader) observeFailure(id string, reason peerFailureReason) {
	if d.failureObserver == nil {
		return
	}
	switch reason {
	case peerFailureInvalidChain, peerFailurePrunedSidechain, peerFailureBadPeer,
		peerFailureTimeout, peerFailureStalling, peerFailureUnsynced, peerFailureEmptyHeaderSet,
		peerFailurePeersUnavailable, peerFailureTooOld, peerFailureInvalidAncestor,
		peerFailureWhitelistMismatch, peerFailureDisconnected, peerFailureNoRemote:
		name := strings.ReplaceAll(string(reason), "-", "_")
		metrics.GetOrRegisterCounter("eth/peerpolicy/downloader/"+name, nil).Inc(1)
		d.failureObserver(id)
	}
}
