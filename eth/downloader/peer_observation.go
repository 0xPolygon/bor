// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package downloader

import (
	"strings"

	"github.com/ethereum/go-ethereum/metrics"
)

// SetFailureObserver installs a non-blocking observer before peer registration.
// A sync failure alone does not prove that this peer supplied invalid data.
func (d *Downloader) SetFailureObserver(observer func(string)) {
	d.failureObserver = observer
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
