package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Known event topics
var (
	// keccak256("Transfer(address,address,uint256)")
	transferEventTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	// keccak256("Approval(address,address,uint256)")
	approvalEventTopic = common.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925")
)

// Known 4-byte selectors (function signatures)
var selectorNames = map[string]string{
	"a9059cbb": "ERC20.transfer(address,uint256)",
	"095ea7b3": "ERC20.approve(address,uint256)",
	"23b872dd": "ERC20/ERC721.transferFrom(address,address,uint256)",
	"42842e0e": "ERC721.safeTransferFrom(address,address,uint256)",
	"b88d4fde": "ERC721.safeTransferFrom(address,address,uint256,bytes)",
	"40c10f19": "ERC20.mint(address,uint256)",
	"c9807539": "transmit(bytes,bytes32[],bytes32[],bytes32)",
	"3593564c": "execute(bytes,bytes[],uint256)",
	"1249c58b": "mint()",
	"252f7b01": "validateTransactionProofV1(uint16,address,uint256,bytes32,bytes32,bytes)",
	"a1305b17": "relayCall(address,address,bytes,uint256,bytes,uint256,address,bytes)",
	"e74094ed": "resellTransferToken(uint256,address)",
	"ef6c5996": "claim(uint256,address,uint256,uint256,address,bytes)",
}

func short(addr common.Address) string { return addr.Hex() }

func shortHash(h common.Hash) string { return h.Hex()[:10] + "…" }

type TxAnalysis struct {
	Hash      common.Hash
	From      common.Address
	To        *common.Address // nil => contract creation
	ValueWei  *big.Int
	Method    string
	InputHex  string
	Nonce     uint64
	GasUsed   uint64
	Status    uint64          // 1 success, 0 revert
	Creates   *common.Address // for contract creation
	Transfers []string        // parsed from logs (token transfers summary)
}

type BlockSummary struct {
	Number     *big.Int
	Hash       common.Hash
	Time       time.Time
	TxCount    int
	Txs        []TxAnalysis
	TopTo      []PairCount
	TopFrom    []PairCount
	TopMethods []PairCount
	TopTokens  []PairCount // by token address seen in Transfer logs
}

type PairCount struct {
	Key   string
	Count int
}

func topN(m map[string]int, n int) []PairCount {
	arr := make([]PairCount, 0, len(m))
	for k, v := range m {
		arr = append(arr, PairCount{Key: k, Count: v})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Count > arr[j].Count })
	if len(arr) > n {
		arr = arr[:n]
	}
	return arr
}

func analyzeBlock(ctx context.Context, client *ethclient.Client, number *big.Int) (*BlockSummary, error) {
	blk, err := client.BlockByNumber(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("fetch block %v: %w", number, err)
	}

	chainID, err := client.NetworkID(ctx)
	if err != nil {
		return nil, fmt.Errorf("network id: %w", err)
	}
	signer := types.LatestSignerForChainID(chainID)

	var (
		toFreq   = map[string]int{}
		fromFreq = map[string]int{}
		methFreq = map[string]int{}
		tokFreq  = map[string]int{}
	)

	analyses := make([]TxAnalysis, 0, len(blk.Transactions()))
	for _, tx := range blk.Transactions() {
		msg, err := types.Sender(signer, tx)
		if err != nil {
			// Fallback: unknown sender
			log.Printf("warn: cannot recover sender for %s: %v", tx.Hash(), err)
		}

		var toPtr *common.Address
		if tx.To() != nil {
			addr := *tx.To()
			toPtr = &addr
		}

		// Method selector
		input := tx.Data()
		method := "<none>"
		if len(input) >= 4 {
			method = selectorNames[hex.EncodeToString(input[:4])]
			if method == "" {
				method = "unknown:" + hex.EncodeToString(input[:4])
			}
		}

		rec, err := client.TransactionReceipt(ctx, tx.Hash())
		if err != nil {
			log.Printf("warn: receipt not available for %s: %v", tx.Hash(), err)
		}

		var status uint64
		var gasUsed uint64
		var created *common.Address
		var transfers []string
		if rec != nil {
			status = rec.Status
			gasUsed = rec.GasUsed
			if tx.To() == nil {
				addr := rec.ContractAddress
				created = &addr
			}
			// Scan logs for Transfer/Approval
			for _, lg := range rec.Logs {
				if len(lg.Topics) == 0 {
					continue
				}
				if lg.Topics[0] == transferEventTopic {
					// Token address is lg.Address
					from := common.BytesToAddress(lg.Topics[1].Bytes())
					to := common.BytesToAddress(lg.Topics[2].Bytes())
					// value is in lg.Data (uint256)
					value := new(big.Int).SetBytes(lg.Data)
					tr := fmt.Sprintf("Transfer token=%s from=%s to=%s value=%s", lg.Address.Hex(), from.Hex(), to.Hex(), value.String())
					transfers = append(transfers, tr)
					tokFreq[strings.ToLower(lg.Address.Hex())]++
				}
				if lg.Topics[0] == approvalEventTopic {
					owner := common.BytesToAddress(lg.Topics[1].Bytes())
					spender := common.BytesToAddress(lg.Topics[2].Bytes())
					value := new(big.Int).SetBytes(lg.Data)
					tr := fmt.Sprintf("Approval token=%s owner=%s spender=%s value=%s", lg.Address.Hex(), owner.Hex(), spender.Hex(), value.String())
					transfers = append(transfers, tr)
				}
			}
		}

		a := TxAnalysis{
			Hash:      tx.Hash(),
			From:      msg,
			To:        toPtr,
			ValueWei:  tx.Value(),
			Method:    method,
			InputHex:  hex.EncodeToString(input),
			Nonce:     tx.Nonce(),
			GasUsed:   gasUsed,
			Status:    status,
			Creates:   created,
			Transfers: transfers,
		}
		analyses = append(analyses, a)

		// Update freqs
		fromKey := strings.ToLower(a.From.Hex())
		fromFreq[fromKey]++
		if a.To != nil {
			toFreq[strings.ToLower(a.To.Hex())]++
		} else if a.Creates != nil {
			toFreq[strings.ToLower(a.Creates.Hex())]++
		}
		methFreq[a.Method]++
	}

	sum := &BlockSummary{
		Number:     blk.Number(),
		Hash:       blk.Hash(),
		Time:       time.Unix(int64(blk.Time()), 0),
		TxCount:    len(blk.Transactions()),
		Txs:        analyses,
		TopTo:      topN(toFreq, 10),
		TopFrom:    topN(fromFreq, 10),
		TopMethods: topN(methFreq, 10),
		TopTokens:  topN(tokFreq, 10),
	}
	return sum, nil
}

func printSummary(sum *BlockSummary) {
	fmt.Printf("\n===== Block %v (%s) at %s =====\n", sum.Number, sum.Hash.Hex(), sum.Time.Format(time.RFC3339))
	fmt.Printf("Transactions: %d\n", sum.TxCount)
	if len(sum.TopMethods) > 0 {
		fmt.Printf("Top methods: \n")
		for _, p := range sum.TopMethods {
			fmt.Printf("  %-45s %4d\n", p.Key, p.Count)
		}
	}
	if len(sum.TopTo) > 0 {
		fmt.Printf("Top recipients (To/Created):\n")
		for _, p := range sum.TopTo {
			fmt.Printf("  %-42s %4d\n", p.Key, p.Count)
		}
	}
	if len(sum.TopFrom) > 0 {
		fmt.Printf("Top senders:\n")
		for _, p := range sum.TopFrom {
			fmt.Printf("  %-42s %4d\n", p.Key, p.Count)
		}
	}
	if len(sum.TopTokens) > 0 {
		fmt.Printf("Top tokens by Transfer logs:\n")
		for _, p := range sum.TopTokens {
			fmt.Printf("  %-42s %4d\n", p.Key, p.Count)
		}
	}

	fmt.Println("\n-- Transactions --")
	for _, tx := range sum.Txs {
		toStr := "<contract creation>"
		if tx.To != nil {
			toStr = tx.To.Hex()
		}
		if tx.Creates != nil {
			toStr = fmt.Sprintf("created %s", tx.Creates.Hex())
		}
		fmt.Printf("%s | from=%s to=%s val=%s wei nonce=%d status=%d gasUsed=%d method=%s\n", shortHash(tx.Hash), tx.From.Hex(), toStr, tx.ValueWei.String(), tx.Nonce, tx.Status, tx.GasUsed, tx.Method)
		if len(tx.Transfers) > 0 {
			for _, tr := range tx.Transfers {
				fmt.Printf("    log: %s\n", tr)
			}
		}
	}

	// Simple similarity hints within the block
	fmt.Println("\n-- Similarity hints --")
	fmt.Println("Same sender -> receiver pairs with 2+ tx:")
	pairFreq := map[string]int{}
	for _, tx := range sum.Txs {
		to := "<create>"
		if tx.To != nil {
			to = strings.ToLower(tx.To.Hex())
		}
		key := strings.ToLower(tx.From.Hex()) + "->" + to
		pairFreq[key]++
	}
	pairs := topN(pairFreq, len(pairFreq))
	printed := 0
	for _, p := range pairs {
		if p.Count >= 2 {
			fmt.Printf("  %s: %d tx\n", p.Key, p.Count)
			printed++
		}
	}
	if printed == 0 {
		fmt.Println("  (none)")
	}

	fmt.Println("Repeated unknown selectors (possible custom contracts):")
	unk := map[string]int{}
	for _, tx := range sum.Txs {
		if strings.HasPrefix(tx.Method, "unknown:") {
			unk[tx.Method]++
		}
	}
	for _, p := range topN(unk, 10) {
		if p.Count >= 2 {
			fmt.Printf("  %s -> %d tx\n", p.Key, p.Count)
		}
	}
}

func parseBlocksArg(arg string) ([]*big.Int, error) {
	parts := strings.Split(arg, ",")
	res := make([]*big.Int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			r := strings.SplitN(p, "-", 2)
			start, ok1 := new(big.Int).SetString(strings.TrimSpace(r[0]), 10)
			end, ok2 := new(big.Int).SetString(strings.TrimSpace(r[1]), 10)
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("bad range: %s", p)
			}
			for i := new(big.Int).Set(start); i.Cmp(end) <= 0; i.Add(i, big.NewInt(1)) {
				res = append(res, new(big.Int).Set(i))
			}
		} else {
			n, ok := new(big.Int).SetString(p, 10)
			if !ok {
				return nil, fmt.Errorf("bad number: %s", p)
			}
			res = append(res, n)
		}
	}
	return res, nil
}

func main() {
	var (
		rpcURL   string
		blocksIn string
	)
	flag.StringVar(&rpcURL, "rpc", os.Getenv("POLYGON_RPC"), "Polygon RPC URL (https or wss). Defaults to POLYGON_RPC env var.")
	flag.StringVar(&blocksIn, "blocks", "", "Comma-separated list and/or ranges of block numbers, e.g. '51840000,51840010-51840015'.")
	flag.Parse()

	if rpcURL == "" {
		log.Fatal("missing -rpc (or POLYGON_RPC env var)")
	}
	if blocksIn == "" && flag.NArg() == 0 {
		log.Fatal("provide -blocks or positional block numbers")
	}

	// Allow positional numbers if -blocks not set
	if blocksIn == "" {
		blocksIn = strings.Join(flag.Args(), ",")
	}

	blockNums, err := parseBlocksArg(blocksIn)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		log.Fatalf("connect RPC: %v", err)
	}
	defer client.Close()

	for i, n := range blockNums {
		sum, err := analyzeBlock(ctx, client, n)
		if err != nil {
			log.Printf("error: %v", err)
			continue
		}
		printSummary(sum)
		if i < len(blockNums)-1 {
			time.Sleep(250 * time.Millisecond)
		} // be polite
	}
}
