package pathdb

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

func saveSnapshotConcurrent(cache *fastcache.Cache, path string) error {
	return cache.SaveToFileConcurrent(path, 4)
}

// snapshotPath returns the on-disk path used to persist/reload the given
// address's cache. journalDir is expected to already be an absolute,
// resolved directory (see triedb/pathdb.Config.JournalDirectory).
func snapshotPath(journalDir string, accountHash common.Hash) string {
	return filepath.Join(snapshotDir(journalDir), accountHash.Hex()+".cache")
}

// snapshotDir returns the directory holding all address cache snapshots.
func snapshotDir(journalDir string) string {
	return filepath.Join(journalDir, "addresscache")
}

// removeSnapshots deletes every address cache snapshot under journalDir.
func removeSnapshots(journalDir string) error {
	return os.RemoveAll(snapshotDir(journalDir))
}

// removeUnconfiguredSnapshots deletes the snapshots of addresses that are no
// longer configured, so dropping an address does not leave its (possibly
// multi-GB) snapshot behind forever. Only entries named exactly as
// snapshotPath names them are considered; failures are logged.
func removeUnconfiguredSnapshots(journalDir string, addressCacheSizes map[common.Address]int) {
	dir := snapshotDir(journalDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("Failed to list address cache snapshots", "dir", dir, "err", err)
		}
		return
	}
	configured := make(map[string]struct{}, len(addressCacheSizes))
	for addr := range addressCacheSizes {
		configured[filepath.Base(snapshotPath(journalDir, crypto.Keccak256Hash(addr.Bytes())))] = struct{}{}
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := configured[name]; ok || !isSnapshotName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.RemoveAll(path); err != nil {
			log.Warn("Failed to remove unconfigured address cache snapshot", "path", path, "err", err)
			continue
		}
		log.Info("Removed unconfigured address cache snapshot", "path", path)
	}
}

// isSnapshotName reports whether name is a snapshot entry as named by
// snapshotPath: a lowercase 0x-prefixed hash followed by ".cache".
func isSnapshotName(name string) bool {
	hexHash, ok := strings.CutSuffix(name, ".cache")
	return ok && len(hexHash) == 2+2*common.HashLength && common.HexToHash(hexHash).Hex() == hexHash
}

// warmFillThreshold returns the byte-fill level, out of cacheSize, that a
// reloaded snapshot must reach to be considered warm. It matches the 66.6%
// target preloadAddressAsync itself fills toward (see the totalBytesLoaded
// check there), so a reload and a from-scratch preload are held to the same
// bar.
func warmFillThreshold(cacheSize int) uint64 {
	return uint64(cacheSize * 2 / 3)
}

// isWarmReload reports whether a reloaded snapshot of bytesSize is warm
// enough to skip the top-up preload for cacheSize. Without this check, a
// snapshot persisted mid-preload (e.g. two restarts in quick succession)
// would be marked warm and permanently skip the top-up preload — leaving the
// cache stuck near-empty for addresses that are rarely touched by organic
// block-processing traffic, which is exactly the profile of the addresses
// this feature targets.
func isWarmReload(bytesSize uint64, cacheSize int) bool {
	return bytesSize >= warmFillThreshold(cacheSize)
}

// Close cancels all background preload operations and waits for them to
// finish. If persist is true and a journal directory is configured, it also
// persists each address's cache to disk so a future restart can reload it
// instead of preloading from scratch. commonCache is never persisted (see
// design spec).
//
// diskLayer.terminate() runs the same two steps itself, so that the save
// happens outside the disk layer lock; see its doc comment for when persist
// applies.
//
// A save failure (disk full, permission error, etc.) is logged and does not
// fail Close(): losing a snapshot only degrades the next startup to a cold
// preload, identical to today's behavior, and must not block shutdown.
func (c *AddressBiasedCache) Close(persist bool) {
	c.stopPreload()
	if persist {
		c.persist()
	}
}

// stopPreload cancels all background preload operations and waits for them
// to finish.
func (c *AddressBiasedCache) stopPreload() {
	if c.cancel != nil {
		c.cancel()  // Signal all goroutines to stop
		c.wg.Wait() // Wait for them to finish
	}
}

// persist saves each address's cache to the journal directory, if one is
// configured. fastcache allows Get/Set while a save is in progress, so the
// caches stay usable throughout.
func (c *AddressBiasedCache) persist() {
	if c.journalDir == "" {
		return
	}

	dir := snapshotDir(c.journalDir)
	if err := ensureDir(dir); err != nil {
		log.Warn("Failed to create address cache snapshot directory", "dir", dir, "err", err)
		return
	}

	c.addressCaches.Range(func(key, value any) bool {
		accountHash := key.(common.Hash)
		addrCache := value.(*fastcache.Cache)

		path := snapshotPath(c.journalDir, accountHash)
		if err := c.saveSnapshot(addrCache, path); err != nil {
			log.Warn("Failed to persist address cache", "account hash", accountHash.Hex(), "path", path, "err", err)
		}
		return true
	})
}

// ensureDir creates dir (and any missing parents) if it doesn't already exist.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
