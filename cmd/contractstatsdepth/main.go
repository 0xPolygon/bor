package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

const (
	chaindataDirName = "chaindata"
	ancientDirName   = "ancient"
)

type depthStat struct {
	Nodes uint64 `json:"nodes"`
	Bytes uint64 `json:"bytes"`
}

type contractStats struct {
	Identity string               `json:"identity"`
	Nodes    uint64               `json:"nodes"`
	Bytes    uint64               `json:"bytes"`
	MaxDepth uint64               `json:"maxDepth"`
	Depths   map[uint64]depthStat `json:"depths,omitempty"`
}

type outputDepth struct {
	Nodes uint64 `json:"n"`
	Bytes uint64 `json:"b"`
}

type outputLine struct {
	Address  string                `json:"a"`
	Nodes    uint64                `json:"n"`
	Bytes    uint64                `json:"b"`
	MaxDepth uint64                `json:"d"`
	Depths   map[uint64]outputDepth `json:"p,omitempty"`
}

func main() {
	datadir := flag.String("datadir", "", "Path to the Bor data directory (the parent of the instance directory)")
	instance := flag.String("instance", "bor", "Name of the instance directory to use under --datadir")
	chainOverride := flag.String("chaindata", "", "Explicit path to the chaindata directory (skips --datadir/--instance resolution)")
	ancient := flag.String("datadir.ancient", "", "Path to the ancient directory (optional)")
	limit := flag.Uint64("limit", 0, "Maximum number of contract entries to print (0 = all)")
	minNodes := flag.Uint64("min-nodes", 0, "Only print contracts whose storage trie has at least this many nodes")
	workers := flag.Int("workers", 0, "Number of worker goroutines for storage sizing (0 = auto)")
	progressFile := flag.String("progress-file", "output.txt", "Path to periodically written progress summary")
	progressEvery := flag.Duration("progress-interval", 30*time.Second, "How often to write progress summary (0 to disable)")
	perDepth := flag.Bool("per-depth", false, "Include per-depth node/byte stats in output")
	depthCap := flag.Uint64("depth-cap", 0, "If >0, aggregate all depths >= cap into bucket 'cap' in per-depth stats")
	var contracts addressList
	flag.Var(&contracts, "contract", "Smart contract address to inspect directly (may be repeated)")
	flag.Parse()

	if *datadir == "" && *chainOverride == "" {
		log.Fatal("either --datadir or --chaindata must be provided")
	}
	log.SetFlags(0)

	dbHandles, err := server.MakeDatabaseHandles(0)
	if err != nil {
		log.Fatalf("failed to determine database handles: %v", err)
	}

	chaindataPath, ancientPath := resolveDataPaths(*datadir, *instance, *chainOverride, *ancient)
	chaindb, err := openChainDatabase(chaindataPath, ancientPath, 1024, dbHandles)
	if err != nil {
		log.Fatalf("failed to open chain database: %v", err)
	}
	defer chaindb.Close()

	head := rawdb.ReadHeadBlock(chaindb)
	if head == nil {
		log.Fatal("no head block found in database")
	}

	scheme := rawdb.ReadStateScheme(chaindb)
	var trieConfig *triedb.Config
	switch scheme {
	case rawdb.PathScheme:
		trieConfig = &triedb.Config{PathDB: pathdb.Defaults}
	case rawdb.HashScheme, "":
		trieConfig = triedb.HashDefaults
	default:
		log.Fatalf("unsupported state scheme %q", scheme)
	}

	tdb := triedb.NewDatabase(chaindb, trieConfig)
	statedb := state.NewDatabase(tdb, nil)

	accountTrie, err := statedb.OpenTrie(head.Root())
	if err != nil {
		log.Fatalf("failed to open account trie: %v", err)
	}

	if len(contracts) > 0 {
		for _, addr := range contracts {
			stats, err := getContractStats(statedb, accountTrie, head, addr, *perDepth, *depthCap)
			if err != nil {
				log.Printf("%v", err)
				continue
			}
			printStats(stats)
		}
		return
	}

	nw := *workers
	if nw <= 0 {
		nw = runtime.GOMAXPROCS(0)
		if nw <= 0 {
			nw = runtime.NumCPU()
		}
		if nw <= 0 {
			nw = 1
		}
	}

	nodeIt, err := accountTrie.NodeIterator(nil)
	if err != nil {
		log.Fatalf("failed to create account iterator: %v", err)
	}
	iter := trie.NewIterator(nodeIt)

	type job struct {
		addrHash    common.Hash
		storageRoot common.Hash
	}
	type result struct {
		stats contractStats
		err   error
	}
	jobs := make(chan job, nw*2)
	results := make(chan result, nw*2)

	var wg sync.WaitGroup
	for i := 0; i < nw; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				identity := formatIdentity(nil, j.addrHash)
				nodes, size, maxDepth, depths, err := countStorageStats(statedb, head.Root(), nil, j.addrHash, j.storageRoot, accountTrie, *perDepth, *depthCap)
				if err != nil {
					results <- result{err: fmt.Errorf("%s: %w", identity, err)}
					continue
				}
				results <- result{stats: contractStats{Identity: identity, Nodes: nodes, Bytes: size, MaxDepth: maxDepth, Depths: depths}}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		enqueued  uint64
		completed uint64
		skipped   uint64
		filtered  uint64
		printed   uint64
		mu        sync.Mutex
	)

	var outputLines chan []byte
	outputDone := make(chan struct{})
	if *progressFile != "" {
		file, err := os.OpenFile(*progressFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatalf("failed to open output file %q: %v", *progressFile, err)
		}
		outputLines = make(chan []byte, nw*4)
		go func() {
			defer close(outputDone)
			defer func() {
				if err := file.Close(); err != nil {
					log.Printf("failed to close output file %q: %v", *progressFile, err)
				}
			}()
			writer := bufio.NewWriter(file)
			var ticker *time.Ticker
			var tick <-chan time.Time
			if *progressEvery > 0 {
				ticker = time.NewTicker(*progressEvery)
				defer ticker.Stop()
				tick = ticker.C
			}
			for {
				select {
				case line, ok := <-outputLines:
					if !ok {
						if err := writer.Flush(); err != nil {
							log.Printf("failed to flush output file %q: %v", *progressFile, err)
						}
						return
					}
					if _, err := writer.Write(line); err != nil {
						log.Printf("failed to write output file %q: %v", *progressFile, err)
					}
				case <-tick:
					if err := writer.Flush(); err != nil {
						log.Printf("failed to flush output file %q: %v", *progressFile, err)
					}
				}
			}
		}()
	} else {
		close(outputDone)
	}

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for r := range results {
			mu.Lock()
			completed++
			mu.Unlock()
			if r.err != nil {
				mu.Lock()
				skipped++
				mu.Unlock()
				log.Printf("%v", r.err)
				continue
			}
			if r.stats.Nodes < *minNodes {
				mu.Lock()
				filtered++
				mu.Unlock()
				continue
			}
			if r.stats.Bytes == 0 {
				mu.Lock()
				filtered++
				mu.Unlock()
				continue
			}
			if outputLines != nil {
				line, err := encodeOutputLine(r.stats)
				if err != nil {
					log.Printf("failed to encode output for %s: %v", r.stats.Identity, err)
				} else {
					outputLines <- line
				}
			}
			printStats(r.stats)
			mu.Lock()
			printed++
			stop := *limit > 0 && printed >= *limit
			mu.Unlock()
			if stop {
				return
			}
		}
	}()

	for iter.Next() {
		var account types.StateAccount
		if err := rlp.DecodeBytes(iter.Value, &account); err != nil {
			log.Printf("skipping account: %v", err)
			continue
		}
		if bytes.Equal(account.CodeHash, types.EmptyCodeHash.Bytes()) {
			continue
		}
		addrHash := common.BytesToHash(iter.Key)
		jobs <- job{addrHash: addrHash, storageRoot: account.Root}
		mu.Lock()
		enqueued++
		mu.Unlock()
	}
	close(jobs)
	<-consumerDone
	if outputLines != nil {
		close(outputLines)
	}
	<-outputDone
}

func printStats(stats contractStats) {
	if stats.Depths == nil {
		fmt.Printf("%s,%d,%d,%d\n", stats.Identity, stats.Nodes, stats.Bytes, stats.MaxDepth)
		return
	}
	payload, err := json.Marshal(stats)
	if err != nil {
		log.Printf("failed to marshal stats for %s: %v", stats.Identity, err)
		return
	}
	fmt.Println(string(payload))
}

func encodeOutputLine(stats contractStats) ([]byte, error) {
	line := outputLine{
		Address:  stats.Identity,
		Nodes:    stats.Nodes,
		Bytes:    stats.Bytes,
		MaxDepth: stats.MaxDepth,
	}
	if stats.Depths != nil {
		depths := make(map[uint64]outputDepth, len(stats.Depths))
		for depth, stat := range stats.Depths {
			depths[depth] = outputDepth{Nodes: stat.Nodes, Bytes: stat.Bytes}
		}
		line.Depths = depths
	}
	payload, err := json.Marshal(line)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	return payload, nil
}

func countStorageStats(db state.Database, stateRoot common.Hash, addr *common.Address, addrHash common.Hash, storageRoot common.Hash, parent state.Trie, perDepth bool, depthCap uint64) (uint64, uint64, uint64, map[uint64]depthStat, error) {
	if storageRoot == types.EmptyRootHash {
		return 0, 0, 0, nil, nil
	}
	storageTrie, err := openStorageTrie(db, stateRoot, addr, addrHash, storageRoot, parent)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	it, err := storageTrie.NodeIterator(nil)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	var (
		nodes    uint64
		size     uint64
		maxDepth uint64
		depths   map[uint64]depthStat
	)
	if perDepth {
		depths = make(map[uint64]depthStat)
	}
	for it.Next(true) {
		blob := it.NodeBlob()
		if blob == nil {
			continue
		}
		depth := pathDepth(it.Path())
		if depthCap > 0 && depth >= depthCap {
			depth = depthCap
		}
		nodes++
		size += uint64(len(blob))
		if depth > maxDepth {
			maxDepth = depth
		}
		if perDepth {
			d := depths[depth]
			d.Nodes++
			d.Bytes += uint64(len(blob))
			depths[depth] = d
		}
	}
	if err := it.Error(); err != nil {
		return 0, 0, 0, nil, err
	}
	return nodes, size, maxDepth, depths, nil
}

func pathDepth(path []byte) uint64 {
	if len(path) == 0 {
		return 0
	}
	depth := uint64(len(path))
	if path[len(path)-1] == 0x10 {
		depth--
	}
	return depth
}

func openStorageTrie(db state.Database, stateRoot common.Hash, addr *common.Address, addrHash common.Hash, storageRoot common.Hash, parent state.Trie) (state.Trie, error) {
	if db.TrieDB().IsVerkle() {
		return parent, nil
	}
	if addr != nil {
		return db.OpenStorageTrie(stateRoot, *addr, storageRoot, parent)
	}
	return trie.NewStateTrie(trie.StorageTrieID(stateRoot, addrHash, storageRoot), db.TrieDB())
}

func getContractStats(statedb state.Database, accountTrie state.Trie, head *types.Block, addr common.Address, perDepth bool, depthCap uint64) (contractStats, error) {
	account, err := accountTrie.GetAccount(addr)
	if err != nil {
		return contractStats{}, fmt.Errorf("%s: %w", addr.Hex(), err)
	}
	if account == nil {
		return contractStats{}, fmt.Errorf("%s: account not found", addr.Hex())
	}
	if bytes.Equal(account.CodeHash, types.EmptyCodeHash.Bytes()) {
		return contractStats{}, fmt.Errorf("%s: no contract code", addr.Hex())
	}
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	nodes, size, maxDepth, depths, err := countStorageStats(statedb, head.Root(), &addr, addrHash, account.Root, accountTrie, perDepth, depthCap)
	if err != nil {
		return contractStats{}, fmt.Errorf("%s: %w", addr.Hex(), err)
	}
	return contractStats{Identity: addr.Hex(), Nodes: nodes, Bytes: size, MaxDepth: maxDepth, Depths: depths}, nil
}

type addressList []common.Address

func (l *addressList) String() string {
	return fmt.Sprint([]common.Address(*l))
}

func (l *addressList) Set(value string) error {
	if value == "" {
		return fmt.Errorf("empty address")
	}
	if !common.IsHexAddress(value) {
		return fmt.Errorf("invalid address %q", value)
	}
	addr := common.HexToAddress(value)
	*l = append(*l, addr)
	return nil
}

func resolveDataPaths(datadir, instance, chaindataOverride, ancientOverride string) (string, string) {
	chaindataPath := chaindataOverride
	if chaindataPath == "" {
		chaindataPath = filepath.Join(datadir, instance, chaindataDirName)
	}
	info, err := os.Stat(chaindataPath)
	if err != nil {
		log.Fatalf("chaindata directory %q is not accessible: %v", chaindataPath, err)
	}
	if !info.IsDir() {
		log.Fatalf("chaindata path %q is not a directory", chaindataPath)
	}
	ancientPath := ancientOverride
	if ancientPath == "" {
		ancientPath = filepath.Join(chaindataPath, ancientDirName)
	}
	return chaindataPath, ancientPath
}

func openChainDatabase(chaindataPath, ancientPath string, cache int, handles int) (ethdb.Database, error) {
	engine := rawdb.PreexistingDatabase(chaindataPath)
	readonly := true
	var (
		kv  ethdb.KeyValueStore
		err error
	)
	switch engine {
	case rawdb.DBLeveldb:
		kv, err = leveldb.New(chaindataPath, cache, handles, "", readonly)
	case rawdb.DBPebble, "":
		kv, err = pebble.New(chaindataPath, cache, handles, "", readonly)
	default:
		return nil, fmt.Errorf("unsupported database engine %q at %s", engine, chaindataPath)
	}
	if err != nil {
		return nil, err
	}
	opts := rawdb.OpenOptions{
		Ancient:             ancientPath,
		ReadOnly:            readonly,
		DisableFreeze:       true,
		IsLastOffset:        false,
		WitnessPruneEnabled: false,
		BlockPruneEnabled:   false,
		Stateless:           false,
	}
	db, err := rawdb.Open(kv, opts)
	if err != nil {
		_ = kv.Close()
		return nil, err
	}
	return db, nil
}

func formatIdentity(addr *common.Address, addrHash common.Hash) string {
	if addr != nil {
		return addr.Hex()
	}
	return fmt.Sprintf("hash:%s", addrHash.Hex())
}
