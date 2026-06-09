package wit2test

import (
	"encoding/binary"
	"strconv"
)

// WitnessPadNodes returns deterministic synthetic trie-node blobs totalling
// ~totalBytes, derived from seed. Determinism matters: the block producer and
// any witness-producing relay (a full node with produce_witness) must generate
// byte-identical witnesses for the same block, or the WIT2 commit-hash and the
// producer signature won't agree across the network. Seeding from the block
// hash guarantees that.
//
// The blobs are orphan nodes — no real trie path references them, and stateless
// execution looks nodes up by keccak(node), so these are never read. They only
// inflate witness size to exercise multi-page transport / pre-import caching.
// Throwaway dev-only harness; nothing here ships.
func WitnessPadNodes(seed []byte, totalBytes int) map[string][]byte {
	if totalBytes <= 0 {
		return nil
	}
	const nodeSize = 4096
	n := (totalBytes + nodeSize - 1) / nodeSize

	var s0 uint64
	for i := 0; i < len(seed) && i < 8; i++ {
		s0 = s0<<8 | uint64(seed[i])
	}

	out := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		b := make([]byte, nodeSize)
		// Counter prefix guarantees uniqueness; seed-derived fill makes the
		// content a deterministic function of (block, index).
		binary.BigEndian.PutUint64(b[0:8], uint64(i))
		binary.BigEndian.PutUint64(b[8:16], s0)
		fill := byte(s0) ^ byte(i)
		for j := 16; j < nodeSize; j++ {
			b[j] = fill
		}
		out[strconv.Itoa(i)] = b // AddState keys by value; map key is irrelevant
	}
	return out
}

// padEveryN selects which blocks get padded. Padding every block makes each
// of the 9 devnet nodes hold ~3.4GiB (every witness ~19MB through produce/
// encode/serve buffers) — ~30GiB total, which OOMs any single-host devnet.
// 1-in-8 matches the production shape (occasional large witness between
// normal ones) while still exercising the oversize-push suppression and the
// multi-page pull path on every padded block.
const padEveryN = 8

// PadBytesForBlock returns the pad size to apply to blockNumber's witness:
// WitnessPadBytes on every padEveryN-th block, 0 otherwise. Keyed on block
// number so the producer and every witness-producing relay make the same
// decision and the WIT2 commit hashes agree.
func PadBytesForBlock(blockNumber uint64) int {
	pad := Get().WitnessPadBytes
	if pad <= 0 || blockNumber%padEveryN != 0 {
		return 0
	}
	return pad
}
