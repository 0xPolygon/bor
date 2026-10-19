// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

// Only peer-specific admission refusals qualify. Successful delivery and global
// congestion do not establish repeated-download abuse.
func (p *peerRecord) refused(b *bucket, tick int64, event Evidence) Reason {
	p.refusalSequence++
	b.refusals = min(b.refusals+1, 3)
	repeated := false
	for _, hash := range event.Hashes {
		entry, ok := p.hashes.refusedBodies.Get(hash)
		if !ok || tick-entry.tick >= windowCount {
			entry = announcement{tick: tick}
		}
		if entry.lastRefusal == p.refusalSequence {
			continue
		}
		entry.lastRefusal = p.refusalSequence
		if entry.count < 3 {
			entry.count++
		}
		repeated = repeated || entry.count == 3
		p.hashes.refusedBodies.Add(hash, entry)
	}
	if repeated {
		return ServingRepetition
	}
	if b.refusals >= 3 {
		return ServingVolume
	}
	return ServingRefused
}
