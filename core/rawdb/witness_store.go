package rawdb

import (
	"fmt"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
)

// WitnessStore abstracts witness blob storage so callers can transparently
// use either the key-value database (Pebble/LevelDB) or the filesystem.
type WitnessStore interface {
	ReadWitness(hash common.Hash) []byte
	WriteWitness(hash common.Hash, witness []byte)
	HasWitness(hash common.Hash) bool
	DeleteWitness(hash common.Hash)
	Close() error
}

// witnessDir returns the 2-level shard directory for a given hash.
// Example: hash 0xabcd... → "ab/cd"
func witnessDir(base string, hash common.Hash) string {
	hex := fmt.Sprintf("%x", hash[:])
	return filepath.Join(base, hex[:2], hex[2:4])
}

// witnessFilePath returns the full path for a witness file.
// Example: hash 0xabcd... → "<base>/ab/cd/0xabcd....rlp"
func witnessFilePath(base string, hash common.Hash) string {
	return filepath.Join(witnessDir(base, hash), hash.Hex()+".rlp")
}
