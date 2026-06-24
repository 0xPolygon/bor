package nativectf

import "github.com/ethereum/go-ethereum/common"

// GetCollectionIdSelector is the 4-byte selector of
// getCollectionId(bytes32,bytes32,uint256).
var GetCollectionIdSelector = [4]byte{0x85, 0x62, 0x96, 0xf7}

// canonicalCTFCodeHash is keccak256(runtime code) of the canonical Gnosis
// Conditional Tokens Framework at 0x4d97dcd97ec945f40cf65f87097ace5ea0476045
// (Polygon mainnet) — the contract that exposes getCollectionId externally.
var canonicalCTFCodeHash = common.HexToHash("0xbe524e094025c2a1122ccfbe3264e29fe662d7e0ae518b6926135c814405eceb")

// ctfCodeHashes is the allowlist of contract codehashes whose external
// getCollectionId calls are eligible for the native fast-path (Case A). Inlined
// uses in other contracts (Case B) are handled separately by bytecode fingerprint.
var ctfCodeHashes = map[common.Hash]struct{}{
	canonicalCTFCodeHash: {},
}

// IsCanonicalCTF reports whether codeHash is an allowlisted CTF codehash.
func IsCanonicalCTF(codeHash common.Hash) bool {
	_, ok := ctfCodeHashes[codeHash]
	return ok
}
