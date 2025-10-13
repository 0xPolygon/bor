package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rlp"
)

type BackFillStateSyncTxsEntriesCommand struct {
	*Meta
	backfillFile string
}

func (c *BackFillStateSyncTxsEntriesCommand) MarkDown() string {
	items := []string{
		"# Backfill StateSync Txs",
		"The ```backfill-statesync-txs``` command receives a trusted file containing statesync txs and events from a time period and backfill it into the database. It walks over the block period checking any missing data and backfilling them. It writes just over KV database, which means the data which were supposed to already be on ancient will be now always at ancient.",
		c.Flags().MarkDown(),
	}

	return strings.Join(items, "\n\n")
}

// Help implements the cli.Command interface
func (c *BackFillStateSyncTxsEntriesCommand) Help() string {
	return `Usage: bor backfill-statesync-txs --datadir <path-to-datadir> --backfill-file <path-to-backfill-file>

  This command receives a trusted file containing statesync txs and events from a time period and backfill it into the database. It walks over the block period checking any missing data and backfilling them. It writes just over KV database, which means the data which were supposed to already be on ancient will be now always at ancient.`
}

func (c *BackFillStateSyncTxsEntriesCommand) Flags() *flagset.Flagset {
	flags := c.NewFlagSet("backfill-statesync-txs")

	flags.StringFlag(&flagset.StringFlag{
		Name:    "backfill-file",
		Value:   &c.backfillFile,
		Usage:   "Path of the file containing the backfill data",
		Default: "",
	})
	return flags
}

// Synopsis implements the cli.Command interface
func (c *BackFillStateSyncTxsEntriesCommand) Synopsis() string {
	return "Backfill missing statesync txs based on a trusted file"
}

type WriteInstruction struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

var (
	// Receipt key byte prefix: "matic-bor-receipt-"
	borReceiptPrefix = []byte("matic-bor-receipt-")

	// State sync topic[0]
	stateSyncTopic0 = common.HexToHash("0x5a22725590b0a51c923940223f7458512164b1113359a735e86e7f27f44791ee")
)

// isHex0x checks and strips "0x" if present.
func strip0x(s string) string {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return s[2:]
	}
	return s
}

// decodeHexString decodes a 0x-prefixed or plain hex string into bytes.
func decodeHexString(s string) ([]byte, error) {
	raw := strip0x(s)
	if len(raw)%2 == 1 {
		// tolerate odd-length hex by left-padding a zero nibble
		raw = "0" + raw
	}
	return hex.DecodeString(raw)
}

// parseReceiptKey decodes the key and, if it is a receipt key, returns (true, blockNumber, blockHash, nil).
// Receipt key layout (bytes): prefix ("matic-bor-receipt-") + [8 bytes big-endian block number] + [32 bytes block hash]
func parseReceiptKey(keyHex string) (ok bool, blockNumber uint64, blockHash common.Hash, err error) {
	b, err := decodeHexString(keyHex)
	if err != nil {
		return false, 0, common.Hash{}, fmt.Errorf("invalid hex key: %w", err)
	}
	if !bytes.HasPrefix(b, borReceiptPrefix) {
		return false, 0, common.Hash{}, nil
	}
	suffix := b[len(borReceiptPrefix):]
	if len(suffix) != 8+32 {
		return false, 0, common.Hash{}, fmt.Errorf("receipt key: unexpected length (got %d, want %d)", len(suffix), 40)
	}
	blockNumber = binary.BigEndian.Uint64(suffix[:8])
	copy(blockHash[:], suffix[8:])
	return true, blockNumber, blockHash, nil
}

// parseTxLookup extracts the tx hash (best-effort) and assumes the layout: [prefix (unknown len)] + [32 bytes tx hash].
func parseTxLookup(keyHex string) (txHash common.Hash, err error) {
	b, err := decodeHexString(keyHex)
	if err != nil {
		return common.Hash{}, fmt.Errorf("invalid txlookup key hex: %w", err)
	}
	if len(b) < 32 {
		return common.Hash{}, fmt.Errorf("txlookup key too short: %d", len(b))
	}
	copy(txHash[:], b[len(b)-32:])
	return txHash, nil
}

// decodeReceiptValue decodes the RLP-encoded value into ReceiptForStorage.
func decodeReceiptValue(valHex string) (*types.ReceiptForStorage, error) {
	b, err := decodeHexString(valHex)
	if err != nil {
		return nil, fmt.Errorf("invalid receipt value hex: %w", err)
	}
	var out types.ReceiptForStorage
	if err := rlp.DecodeBytes(b, &out); err != nil {
		// Helpful hint: you can sanity-check what you’re decoding by hashing the bytes.
		h := sha256.Sum256(b)
		return nil, fmt.Errorf("failed to RLP-decode receipt (sha256=%x): %w", h[:8], err)
	}
	return &out, nil
}

// toBigIntFromTopicHash interprets a topic hash (32 bytes) as a big-endian integer.
func toBigIntFromTopicHash(h common.Hash) *big.Int {
	// Topics are 32-byte values; interpret as uint256.
	return new(big.Int).SetBytes(h[:])
}

// parseBlockNumberFromTxLookupValue: txlookup value is "0x" + hex(blockNumberBytes) (big-endian).
func parseBlockNumberFromTxLookupValue(valHex string) (uint64, error) {
	b, err := decodeHexString(valHex)
	if err != nil {
		return 0, fmt.Errorf("invalid txlookup value hex: %w", err)
	}
	if len(b) == 0 {
		return 0, fmt.Errorf("empty txlookup value")
	}
	if len(b) > 8 {
		// Values may be shorter (no leading zeros); longer than 8 bytes would overflow uint64
		return 0, fmt.Errorf("txlookup value too large for uint64: %d bytes", len(b))
	}
	var n uint64
	for _, by := range b {
		n = (n << 8) | uint64(by)
	}
	return n, nil
}

type stateSyncPoint struct {
	ID        uint64
	BlockNum  uint64
	ReceiptAt string
}

type validationResult struct {
	Valid                                   bool
	Errors                                  []string
	Warnings                                []string
	FirstStateSyncID, LastStateSyncID       uint64
	FirstStateSyncBlock, LastStateSyncBlock uint64
}

func (vr *validationResult) addErr(format string, args ...any) {
	vr.Errors = append(vr.Errors, fmt.Sprintf(format, args...))
}
func (vr *validationResult) addWarn(format string, args ...any) {
	vr.Warnings = append(vr.Warnings, fmt.Sprintf(format, args...))
}

// ValidateBackfillFile runs the checks described in your request and prints a summary.
// It returns a non-nil error if *invalid*.
func (c *BackFillStateSyncTxsEntriesCommand) validateBackfillFile(backfillFilePath string) ([]WriteInstruction, error) {
	raw, err := os.ReadFile(backfillFilePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	var writes []WriteInstruction
	if err := json.Unmarshal(raw, &writes); err != nil {
		return nil, fmt.Errorf("parse JSON writes: %w", err)
	}

	res := &validationResult{Valid: true}

	// Collect txlookup map: txHash -> blockNumber
	txLookupBlockByHash := make(map[common.Hash]uint64)
	// Also collect a set of blockNumbers that appear in any txlookup values, to satisfy the best-effort check.
	txLookupBlocks := make(map[uint64]struct{})

	// First pass: index txlookup entries
	for _, w := range writes {
		if ok, _, _, _ := parseReceiptKey(w.Key); ok {
			continue // not a txlookup
		}
		// Treat as txlookup
		txh, err := parseTxLookup(w.Key)
		if err != nil {
			res.addWarn("txlookup key parse failed (%s): %v", w.Key, err)
			continue
		}
		bn, err := parseBlockNumberFromTxLookupValue(w.Value)
		if err != nil {
			res.addWarn("txlookup value parse failed for %s: %v", txh.Hex(), err)
			continue
		}
		txLookupBlockByHash[txh] = bn
		txLookupBlocks[bn] = struct{}{}
	}

	// For global consecutiveness: collect all state sync IDs across all receipts (and remember first/last blocks)
	var allPoints []stateSyncPoint

	// Second pass: validate receipt entries
	for _, w := range writes {
		ok, blockNum, _, err := parseReceiptKey(w.Key)
		if err != nil {
			res.addErr("receipt key parse error: %v", err)
			continue
		}
		if !ok {
			continue // not a receipt
		}
		// decode RLP value
		rr, err := decodeReceiptValue(w.Value)
		if err != nil {
			res.addErr("receipt value decode failed (block %d): %v", blockNum, err)
			continue
		}

		// 1) At least one state sync log
		var stateSyncIDs []uint64
		for _, lg := range rr.Logs {
			if len(lg.Topics) == 0 {
				continue
			}
			if lg.Topics[0] == stateSyncTopic0 {
				// topics[1] must exist and encodes the state-sync ID
				if len(lg.Topics) < 2 {
					res.addErr("state-sync log missing topics[1] (block %d)", blockNum)
					continue
				}
				idBI := toBigIntFromTopicHash(lg.Topics[1])
				if !idBI.IsUint64() {
					res.addErr("state-sync ID does not fit into uint64 (block %d)", blockNum)
					continue
				}
				id := idBI.Uint64()
				stateSyncIDs = append(stateSyncIDs, id)
				allPoints = append(allPoints, stateSyncPoint{
					ID:        id,
					BlockNum:  blockNum,
					ReceiptAt: w.Key,
				})
			}
		}
		if len(stateSyncIDs) == 0 {
			res.addErr("receipt at block %d has no state-sync logs", blockNum)
			continue
		}

		// 3) Best-effort txlookup presence: ensure that there exists *some* txlookup with this block number
		if _, ok := txLookupBlocks[blockNum]; !ok {
			// We can't derive the exact tx hash from the encoded logs (rlp:"-"), so we check block presence instead.
			res.addErr("no txlookup entry found for block %d (best-effort check)", blockNum)
		}
	}

	// 2) Global consecutiveness check for all state-sync IDs
	if len(allPoints) == 0 {
		res.addErr("no state-sync logs found in any receipt")
	} else {
		// Sort by ID, dedupe, and check gaps
		sort.Slice(allPoints, func(i, j int) bool { return allPoints[i].ID < allPoints[j].ID })

		// Track first/last ID and blocks
		res.FirstStateSyncID = allPoints[0].ID
		res.FirstStateSyncBlock = allPoints[0].BlockNum
		res.LastStateSyncID = allPoints[len(allPoints)-1].ID
		res.LastStateSyncBlock = allPoints[len(allPoints)-1].BlockNum

		// Build unique ordered IDs and remember a canonical block for each
		type idBlock struct {
			id    uint64
			block uint64
		}
		uniq := make([]idBlock, 0, len(allPoints))
		for i := 0; i < len(allPoints); i++ {
			if i == 0 || allPoints[i].ID != allPoints[i-1].ID {
				uniq = append(uniq, idBlock{allPoints[i].ID, allPoints[i].BlockNum})
			}
		}
		for i := 1; i < len(uniq); i++ {
			prev, cur := uniq[i-1].id, uniq[i].id
			if cur != prev+1 {
				prevBlock, curBlock := uniq[i-1].block, uniq[i].block
				res.addErr("state-sync IDs are not consecutive: %d (block: %d) -> %d (block %d) (gap detected)", prev, prevBlock, cur, curBlock)
			}
		}
	}

	if len(res.Errors) > 0 {
		res.Valid = false
	}

	c.UI.Output("=== Backfill Validation Summary ===")
	c.UI.Output(fmt.Sprintf("File: %s\n", backfillFilePath))
	c.UI.Output(fmt.Sprintf("Valid: %v\n", res.Valid))
	if len(res.Errors) > 0 {
		c.UI.Output("\nErrors:")
		for _, e := range res.Errors {
			c.UI.Output(fmt.Sprintf("  - %s\n", e))
		}
	}
	if len(res.Warnings) > 0 {
		c.UI.Output("\nWarnings:")
		for _, w := range res.Warnings {
			c.UI.Output(fmt.Sprintf("  - %s\n", w))
		}
	}
	if len(allPoints) > 0 {
		c.UI.Output(fmt.Sprintf("\nFirst state-sync:  ID=%d  Block=%d\n", res.FirstStateSyncID, res.FirstStateSyncBlock))
		c.UI.Output(fmt.Sprintf("Last state-sync:   ID=%d  Block=%d\n", res.LastStateSyncID, res.LastStateSyncBlock))
	} else {
		c.UI.Output("\nNo state-sync logs discovered, so first/last not available.")
	}

	if !res.Valid {
		return nil, fmt.Errorf("backfill validation: INVALID")
	}
	return writes, nil
}

type putReq struct {
	key []byte
	val []byte
}

// propagateOnce non-blockingly sends the first error into errCh.
// Subsequent calls become no-ops if the channel already contains an error.
func propagateOnce(errCh chan error, err error) {
	if err == nil {
		return
	}
	select {
	case errCh <- err:
	default:
	}
}

// hasPendingErr returns true if errCh already holds an error.
func hasPendingErr(errCh chan error) bool {
	select {
	case e := <-errCh:
		// put it back for other consumers and signal presence
		propagateOnce(errCh, e)
		return true
	default:
		return false
	}
}

// tryDecodeKV decodes the hex key/value of a write instruction and wraps errors with kind.
func tryDecodeKV(kind string, w WriteInstruction) (keyBytes, valBytes []byte, err error) {
	keyBytes, err = decodeHexString(w.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("decode %s key: %w", kind, err)
	}
	valBytes, err = decodeHexString(w.Value)
	if err != nil {
		return nil, nil, fmt.Errorf("decode %s value: %w", kind, err)
	}
	return keyBytes, valBytes, nil
}

// enqueuePut attempts to send a put request; returns false if a hard error is pending.
func enqueuePut(puts chan putReq, req putReq, errCh chan error) bool {
	select {
	case puts <- req:
		return true
	case e := <-errCh:
		// propagate and signal caller to stop
		propagateOnce(errCh, e)
		return false
	}
}

func incrementAndMaybeReportProgress(c *BackFillStateSyncTxsEntriesCommand, processed *int64, total int64) {
	n := atomic.AddInt64(processed, 1)
	if n%1000 == 0 || n == total {
		c.UI.Output(fmt.Sprintf("Progress: %d/%d (%.2f%%)", n, total, 100*float64(n)/float64(total)))
	}
}

// putMissingBackfill iterates over the writes in parallel, checks db presence, and batches missing puts.
func (c *BackFillStateSyncTxsEntriesCommand) putMissingBackfill(chaindb ethdb.Database, writes []WriteInstruction) error {
	batch := chaindb.NewBatch()

	total := len(writes)

	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}

	jobs := make(chan WriteInstruction, workers*2)
	puts := make(chan putReq, workers*4)
	errCh := make(chan error, 1)

	var processed int64
	var checkedRcpt int64
	var checkedLookup int64
	var inserted int64
	var skippedKnown int64
	var skippedOther int64

	var batchWG sync.WaitGroup
	batchWG.Add(1)
	go func() {
		defer batchWG.Done()
		for p := range puts {
			if err := batch.Put(p.key, p.val); err != nil {
				// First error wins
				propagateOnce(errCh, fmt.Errorf("batch put failed: %w", err))
				return
			}
			atomic.AddInt64(&inserted, 1)
		}
	}()

	// Worker pool
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for w := range jobs {
				// Early exit if a hard error occurred (avoid wasted work)
				if hasPendingErr(errCh) {
					return
				}

				isReceipt, number, blockHash, err := parseReceiptKey(w.Key)
				if err != nil {
					c.UI.Output(fmt.Sprintf("WARN: %v (key=%s)", err, w.Key))
					atomic.AddInt64(&skippedOther, 1)
					incrementAndMaybeReportProgress(c, &processed, int64(total))
					continue
				}

				if isReceipt {
					atomic.AddInt64(&checkedRcpt, 1)

					exists := rawdb.ReadBorReceiptRLP(chaindb, blockHash, number)
					if len(exists) > 0 {
						atomic.AddInt64(&skippedKnown, 1)
						incrementAndMaybeReportProgress(c, &processed, int64(total))
						continue
					}

					keyBytes, valBytes, err := tryDecodeKV("receipt", w)
					if err != nil {
						propagateOnce(errCh, err)
						return
					}
					if !enqueuePut(puts, putReq{key: keyBytes, val: valBytes}, errCh) {
						return
					}

					incrementAndMaybeReportProgress(c, &processed, int64(total))
					continue
				}

				// txlookup path
				txh, err := parseTxLookup(w.Key)
				if err != nil {
					c.UI.Output(fmt.Sprintf("WARN: cannot parse txlookup key (key=%s): %v", w.Key, err))
					atomic.AddInt64(&skippedOther, 1)
					incrementAndMaybeReportProgress(c, &processed, int64(total))
					continue
				}
				atomic.AddInt64(&checkedLookup, 1)

				if rawdb.ReadBorTxLookupEntry(chaindb, txh) != nil {
					atomic.AddInt64(&skippedKnown, 1)
					incrementAndMaybeReportProgress(c, &processed, int64(total))
					continue
				}

				keyBytes, valBytes, err := tryDecodeKV("txlookup", w)
				if err != nil {
					propagateOnce(errCh, err)
					return
				}
				if !enqueuePut(puts, putReq{key: keyBytes, val: valBytes}, errCh) {
					return
				}

				incrementAndMaybeReportProgress(c, &processed, int64(total))
			}
		}()
	}

	go func() {
		for _, w := range writes {
			// If a hard error already happened, stop feeding.
			if hasPendingErr(errCh) {
				close(jobs)
				return
			}
			jobs <- w
		}
		close(jobs)
	}()

	wg.Wait()
	close(puts)
	batchWG.Wait()

	select {
	case e := <-errCh:
		return e
	default:
	}

	// Final write
	if err := batch.Write(); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}

	c.UI.Output(fmt.Sprintf(
		"Backfill summary: total=%d receipts_checked=%d txlookups_checked=%d inserted=%d already_present=%d skipped_other=%d",
		total,
		checkedRcpt, checkedLookup, inserted, skippedKnown, skippedOther,
	))
	return nil
}

// Run implements the cli.Command interface
func (c *BackFillStateSyncTxsEntriesCommand) Run(args []string) int {
	flags := c.Flags()

	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	datadir := c.dataDir
	if datadir == "" {
		c.UI.Error("--datadir flag is required")
		return 1
	}

	backfillFilePath := c.backfillFile
	if backfillFilePath == "" {
		c.UI.Error("--backfill-file flag is required")
		return 1
	}

	c.UI.Output("Starting to check the backfill file. Opening at path: " + backfillFilePath)

	writes, err := c.validateBackfillFile(backfillFilePath)
	if err != nil {
		c.UI.Error("error while validating backfill file: " + err.Error())
		return 1
	}

	c.UI.Output("Starting to check missing txs over database")
	node, err := node.New(&node.Config{
		DataDir: datadir,
	})
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to create node: %v", err))
		return 1
	}
	chaindb, err := node.OpenDatabase(datadir+"/bor/chaindata", 1024, 2000, "", false)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to open underlying key value database: %v", err))
		return 1
	}
	c.UI.Output("Opened database at path: " + datadir)

	if err := c.putMissingBackfill(chaindb, writes); err != nil {
		c.UI.Error("error while putting missing backfill: " + err.Error())
		return 1
	}

	return 0
}
