// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

const repeatedBodyBytes = 32 << 20

func (p *peerRecord) repeatedBodies(b *bucket, tick int64, event Evidence) bool {
	if event.Family != BodyReplies {
		return false
	}
	count := min(len(event.Hashes), len(event.ObjectBytes), maxHashes)
	for i, hash := range event.Hashes[:count] {
		entry, ok := p.hashes[BodyReplies].Get(hash)
		if !ok || tick-entry.tick >= windowCount {
			entry = announcement{tick: tick}
		}
		if entry.count < 3 {
			entry.count++
		}
		if entry.count == 3 {
			b.repeatedBytes = saturatingAdd(b.repeatedBytes, event.ObjectBytes[i], repeatedBodyBytes+1)
		}
		p.hashes[BodyReplies].Add(hash, entry)
	}
	return b.repeatedBytes > repeatedBodyBytes
}
