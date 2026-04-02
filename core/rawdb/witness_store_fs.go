package rawdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// writeRequest is a pending witness write for the background goroutine.
type writeRequest struct {
	hash    common.Hash
	witness []byte
	done    chan struct{} // closed after processing; nil for normal writes
}

// fsWitnessStore stores witness blobs on the filesystem (one file per block)
// while keeping size metadata in the key-value database for fast P2P queries.
//
// Writes are dispatched to a background goroutine so the caller does not block
// on filesystem I/O. The in-memory LRU cache in BlockChain is populated
// synchronously, so reads immediately after a write still succeed.
//
// Reads and HasWitness checks fall back to Pebble when the file is not found,
// enabling seamless migration from the DB backend.
type fsWitnessStore struct {
	dir       string
	db        ethdb.Database
	writeCh   chan writeRequest
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// writeChanSize is the buffer size for the async write channel.
// Polygon PoS produces one block every ~2 s, so 64 gives >2 min of headroom.
const writeChanSize = 64

// NewFSWitnessStore creates a filesystem-backed WitnessStore.
// dir is the root directory for witness files.
// db is used for size metadata and as a read fallback during migration.
func NewFSWitnessStore(dir string, db ethdb.Database) WitnessStore {
	s := &fsWitnessStore{
		dir:     dir,
		db:      db,
		writeCh: make(chan writeRequest, writeChanSize),
	}
	s.cleanupTempFiles()

	s.wg.Add(1)

	go s.writeLoop()

	return s
}

func (s *fsWitnessStore) ReadWitness(hash common.Hash) []byte {
	path := witnessFilePath(s.dir, hash)

	data, err := os.ReadFile(path)
	if err == nil {
		return data
	}

	// Fall back to Pebble for witnesses written before the FS backend was enabled.
	return ReadWitness(s.db, hash)
}

func (s *fsWitnessStore) WriteWitness(hash common.Hash, witness []byte) {
	s.writeCh <- writeRequest{hash: hash, witness: witness}
}

func (s *fsWitnessStore) HasWitness(hash common.Hash) bool {
	path := witnessFilePath(s.dir, hash)
	if _, err := os.Stat(path); err == nil {
		return true
	}

	// Fall back to Pebble for pre-migration witnesses.
	return HasWitness(s.db, hash)
}

func (s *fsWitnessStore) DeleteWitness(hash common.Hash) {
	path := witnessFilePath(s.dir, hash)

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Error("Failed to delete witness file", "path", path, "err", err)
	}

	// Best-effort removal of empty shard directories.
	dir := witnessDir(s.dir, hash)
	os.Remove(dir) // only succeeds if empty
	os.Remove(filepath.Dir(dir))

	// Also clean Pebble data (handles migration-era witnesses).
	DeleteWitness(s.db, hash)
}

// Close signals the write loop to drain pending writes and waits for it to finish.
// Must be called before closing the underlying database. Safe to call multiple times.
func (s *fsWitnessStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.writeCh)
		s.wg.Wait()
	})

	return nil
}

// writeLoop is the background goroutine that processes async witness writes.
func (s *fsWitnessStore) writeLoop() {
	defer s.wg.Done()

	for req := range s.writeCh {
		if req.witness != nil {
			s.writeWitnessSync(req.hash, req.witness)
		}

		if req.done != nil {
			close(req.done)
		}
	}
}

// writeWitnessSync performs the actual filesystem + metadata write.
func (s *fsWitnessStore) writeWitnessSync(hash common.Hash, witness []byte) {
	dir := witnessDir(s.dir, hash)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Crit("Failed to create witness shard directory", "dir", dir, "err", err)
	}

	finalPath := witnessFilePath(s.dir, hash)
	tmpPath := finalPath + ".tmp"

	if err := os.WriteFile(tmpPath, witness, 0644); err != nil {
		log.Crit("Failed to write witness temp file", "path", tmpPath, "err", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		log.Crit("Failed to rename witness temp file", "from", tmpPath, "to", finalPath, "err", err)
	}

	// Write size metadata to Pebble for P2P pagination queries.
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(len(witness)))

	if err := s.db.Put(witnessSizeKey(hash), buf[:]); err != nil {
		log.Crit("Failed to store witness size", "err", err)
	}
}

// cleanupTempFiles removes orphaned .tmp files left by interrupted writes.
func (s *fsWitnessStore) cleanupTempFiles() {
	count := 0

	filepath.Walk(s.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".tmp") {
			if removeErr := os.Remove(path); removeErr != nil {
				log.Warn("Failed to clean up orphaned witness temp file", "path", path, "err", removeErr)
			} else {
				count++
			}
		}
		return nil
	})

	if count > 0 {
		log.Info("Cleaned up orphaned witness temp files", "count", count)
	}
}
