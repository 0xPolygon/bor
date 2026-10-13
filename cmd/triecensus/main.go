// triecensus reads the path-based trie node tables of a bor chaindata
// directory and classifies every stored MPT node as branch, extension, or
// leaf, reporting counts and bytes for each class.
//
// The motivating question: if the flat-state snapshot were dropped and ALL
// intermediate (non-leaf) trie nodes were kept in memory — with leaf reads
// served from disk — how much memory would the intermediate layer need?
// The answer is the branch+extension rows of the report.
//
// Classification is structural and exact: shortNode keys carry the compact
// terminator flag exactly when they are leaves, which distinguishes a storage
// leaf whose value happens to be 32 bytes from an extension (whose child
// reference is also 32 bytes) with no probing. Note that small children
// (RLP under ~32 bytes, e.g. storage slots holding small values) are inlined
// into their parent node's blob rather than stored as separate rows, so the
// leaf rows undercount total state entries; those payloads ride along with
// the internal nodes and would be kept in RAM with them in the hypothetical
// design.
//
// The tool opens the key-value store read-only, the same pattern as
// cmd/bodyscan. Run it on a stopped node or a copy of chaindata when in
// doubt.
//
// Usage:
//
//	triecensus --chaindata /var/lib/bor/data/bor/chaindata
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	classBranch = iota
	classExtension
	classLeaf
	classUnknown
)

var classNames = []string{"branch", "extension", "leaf", "unknown"}

type nodeClass struct {
	count      uint64
	bytes      uint64 // raw RLP blob bytes stored in the table
	valueBytes uint64 // leaves only: decoded length of the value item
}

type ownerStats struct {
	classes    [4]nodeClass
	totalNodes uint64
	totalBytes uint64
}

func (s *ownerStats) add(class int, blobLen, valueLen int) {
	s.classes[class].count++
	s.classes[class].bytes += uint64(blobLen)
	s.classes[class].valueBytes += uint64(valueLen)
	s.totalNodes++
	s.totalBytes += uint64(blobLen)
}

func (s *ownerStats) internal() nodeClass {
	var t nodeClass
	t.count = s.classes[classBranch].count + s.classes[classExtension].count
	t.bytes = s.classes[classBranch].bytes + s.classes[classExtension].bytes
	return t
}

type census struct {
	accounts   ownerStats
	storage    ownerStats
	decodeErrs uint64
	// Per-owner storage-trie stats for the top-N report.
	storageOwners map[common.Hash]*ownerStats
}

func newCensus() *census {
	return &census{storageOwners: make(map[common.Hash]*ownerStats)}
}

// classify decodes one path-scheme trie node blob and returns its class
// plus the decoded length of a leaf's value item. The classification is
// structural: a fullNode is a 17-item RLP list; a shortNode is a 2-item
// list whose compact key carries the terminator flag (0x20 in its first
// byte) exactly when it is a leaf. This distinguishes a storage leaf whose
// value happens to be 32 bytes from an extension (whose child reference is
// also 32 bytes) with no database probing.
func classify(blob []byte) (int, int, error) {
	content, _, err := rlp.SplitList(blob)
	if err != nil {
		return classUnknown, 0, err
	}
	var items [][]byte
	for len(content) > 0 {
		_, item, rest, err := rlp.Split(content)
		if err != nil {
			return classUnknown, 0, err
		}
		items = append(items, item)
		content = rest
	}
	switch len(items) {
	case 17: // fullNode: 16 child slots + value flag
		return classBranch, 0, nil
	case 2: // shortNode: terminator flag separates leaves from extensions
		key, val := items[0], items[1]
		if len(key) == 0 {
			return classUnknown, 0, fmt.Errorf("empty shortNode key")
		}
		if key[0]&0x20 != 0 {
			// Leaf. The second item is the payload: account RLP for
			// account-trie leaves, the slot value for storage ones.
			return classLeaf, len(val), nil
		}
		// Extension: the second item is the child node hash (or,
		// rarely, an inlined small child RLP).
		return classExtension, 0, nil
	default:
		return classUnknown, 0, fmt.Errorf("unexpected item count %d", len(items))
	}
}

func run(db ethdb.KeyValueStore, top int, logw io.Writer) (*census, error) {
	c := newCensus()

	it := db.NewIterator(nil, nil)
	defer it.Release()
	scanned := 0
	for it.Next() {
		key, blob := it.Key(), it.Value()
		// Parse node keys directly: the rawdb.Resolve* validators cap
		// the node path at 64 bytes of key, but real leaf paths reach
		// 64 bytes (128 nibbles) and would be silently rejected.
		if len(key) >= 1 && key[0] == rawdb.TrieNodeAccountPrefix[0] {
			class, valueLen, err := classify(blob)
			if err != nil {
				c.decodeErrs++
				continue
			}
			c.accounts.add(class, len(blob), valueLen)
		} else if len(key) >= 1+common.HashLength && key[0] == rawdb.TrieNodeStoragePrefix[0] {
			class, valueLen, err := classify(blob)
			if err != nil {
				c.decodeErrs++
				continue
			}
			owner := common.BytesToHash(key[1 : 1+common.HashLength])
			stats := c.storageOwners[owner]
			if stats == nil {
				stats = &ownerStats{}
				c.storageOwners[owner] = stats
			}
			stats.add(class, len(blob), valueLen)
		}
		scanned++
		if scanned%5_000_000 == 0 {
			fmt.Fprintf(logw, "scanned %dM entries...\n", scanned/1_000_000)
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	// Fold per-owner storage stats into the storage total.
	for _, os := range c.storageOwners {
		for class := range classNames {
			c.storage.classes[class].count += os.classes[class].count
			c.storage.classes[class].bytes += os.classes[class].bytes
			c.storage.classes[class].valueBytes += os.classes[class].valueBytes
		}
		c.storage.totalNodes += os.totalNodes
		c.storage.totalBytes += os.totalBytes
	}
	printReport(c, top, logw)
	return c, nil
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TiB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func printOwner(logw io.Writer, name string, s ownerStats) {
	for class := range classNames {
		n := s.classes[class]
		line := fmt.Sprintf("  %-9s %-10s %14d %16s", name, classNames[class], n.count, humanBytes(n.bytes))
		if class == classLeaf {
			line += fmt.Sprintf(" %16s (payload)", humanBytes(n.valueBytes))
		}
		fmt.Fprintln(logw, line)
	}
}

func printReport(c *census, top int, logw io.Writer) {
	fmt.Fprintln(logw, "\n--- node census (path scheme) ---")
	fmt.Fprintf(logw, "%-22s %14s %16s\n", "class", "count", "stored bytes")
	fmt.Fprintln(logw, "account trie:")
	printOwner(logw, "account", c.accounts)
	fmt.Fprintln(logw, "storage tries:")
	printOwner(logw, "storage", c.storage)
	if c.decodeErrs > 0 {
		fmt.Fprintf(logw, "decode errors : %d\n", c.decodeErrs)
	}

	totalNodes := c.accounts.totalNodes + c.storage.totalNodes
	totalBytes := c.accounts.totalBytes + c.storage.totalBytes
	accInt, stoInt := c.accounts.internal(), c.storage.internal()
	totalInternalBytes := accInt.bytes + stoInt.bytes
	leafCount := c.accounts.classes[classLeaf].count + c.storage.classes[classLeaf].count
	leafBytes := c.accounts.classes[classLeaf].bytes + c.storage.classes[classLeaf].bytes
	leafPayload := c.accounts.classes[classLeaf].valueBytes + c.storage.classes[classLeaf].valueBytes

	fmt.Fprintln(logw, "\n--- memory experiment: all intermediate nodes in RAM, leaves on disk ---")
	fmt.Fprintf(logw, "intermediate nodes : %d (%s)\n", accInt.count+stoInt.count, humanBytes(totalInternalBytes))
	fmt.Fprintf(logw, "  account          : %d (%s)\n", accInt.count, humanBytes(accInt.bytes))
	fmt.Fprintf(logw, "  storage          : %d (%s)\n", stoInt.count, humanBytes(stoInt.bytes))
	fmt.Fprintf(logw, "leaves (stay disk) : %d (%s stored, %s payload)\n", leafCount, humanBytes(leafBytes), humanBytes(leafPayload))
	fmt.Fprintf(logw, "total nodes        : %d (%s)\n", totalNodes, humanBytes(totalBytes))
	fmt.Fprintf(logw, "intermediate share : %.1f%% of node bytes\n", 100*float64(totalInternalBytes)/float64(max(totalBytes, 1)))
	// Rough in-RAM overhead for a map keyed by (owner, path) holding the
	// blob: 24B slice header + ~48B map/bucket overhead + key material.
	const perNodeOverhead = 24 + 48 + 4
	estRAM := totalInternalBytes + (accInt.count+stoInt.count)*perNodeOverhead
	fmt.Fprintf(logw, "est. RAM incl. map overhead (~76B/node): %s\n", humanBytes(estRAM))
	fmt.Fprintf(logw, "leaf rows note: small-valued leaves are inlined in parents;\n")
	fmt.Fprintf(logw, "account leaf rows ~= #accounts, storage leaf rows <= #live slots\n")

	if top > 0 {
		type ownerRow struct {
			owner common.Hash
			count uint64
			bytes uint64
		}
		rows := make([]ownerRow, 0, len(c.storageOwners))
		for owner, os := range c.storageOwners {
			if n := os.internal(); n.count > 0 {
				rows = append(rows, ownerRow{owner, n.count, n.bytes})
			}
		}
		if len(rows) > 0 {
			sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
			fmt.Fprintf(logw, "\ntop %d storage tries by intermediate bytes:\n", min(top, len(rows)))
			fmt.Fprintf(logw, "  %-66s %14s %16s\n", "owner (account hash)", "int. nodes", "int. bytes")
			for _, r := range rows[:min(top, len(rows))] {
				fmt.Fprintf(logw, "  %-66s %14d %16s\n", r.owner.Hex(), r.count, humanBytes(r.bytes))
			}
		}
	}
}

func main() {
	var (
		chaindata = flag.String("chaindata", "", "path to bor/chaindata (the dir containing CURRENT, required)")
		top       = flag.Int("top", 20, "top N storage tries to report by intermediate bytes (0 = none)")
	)
	flag.Parse()
	if *chaindata == "" {
		flag.Usage()
		os.Exit(2)
	}
	var kv ethdb.KeyValueStore
	var err error
	switch rawdb.PreexistingDatabase(*chaindata) {
	case rawdb.DBPebble:
		kv, err = pebble.New(*chaindata, 512, 512, "", true /*readonly*/)
	case rawdb.DBLeveldb:
		kv, err = leveldb.New(*chaindata, 512, 512, "", true /*readonly*/)
	default:
		fmt.Fprintf(os.Stderr, "no pebble/leveldb database found at %s\n", *chaindata)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer kv.Close()
	if _, err := run(kv, *top, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
