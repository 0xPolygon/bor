// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/p2p/tracker"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	// newBlockPushIntervalTimer tracks time deltas between consecutive inbound NewBlock pushes.
	newBlockPushIntervalTimer = metrics.NewRegisteredTimer("eth/protocols/eth/newblock/push_interval", nil)

	// lastNewBlockPushUnix holds the timestamp (unix nanos) when the last push arrived.
	lastNewBlockPushUnix atomic.Int64
)

func handleGetBlockHeaders(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the complex header query
	var query GetBlockHeadersPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	response := ServiceGetBlockHeadersQuery(backend.Chain(), query.GetBlockHeadersRequest, peer)
	return peer.ReplyBlockHeadersRLP(query.RequestId, response)
}

// ServiceGetBlockHeadersQuery assembles the response to a header query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetBlockHeadersQuery(chain *core.BlockChain, query *GetBlockHeadersRequest, peer *Peer) []rlp.RawValue {
	if query.Amount == 0 {
		return nil
	}
	if query.Skip == 0 {
		// The fast path: when the request is for a contiguous segment of headers.
		return serviceContiguousBlockHeaderQuery(chain, query)
	} else {
		return serviceNonContiguousBlockHeaderQuery(chain, query, peer)
	}
}

func serviceNonContiguousBlockHeaderQuery(chain *core.BlockChain, query *GetBlockHeadersRequest, peer *Peer) []rlp.RawValue {
	hashMode := query.Origin.Hash != (common.Hash{})
	first := true
	maxNonCanonical := uint64(100)

	// Gather headers until the fetch or network limits is reached
	var (
		bytes   common.StorageSize
		headers []rlp.RawValue
		unknown bool
		lookups int
	)

	for !unknown && len(headers) < int(query.Amount) && bytes < softResponseLimit &&
		len(headers) < maxHeadersServe && lookups < 2*maxHeadersServe {
		lookups++
		// Retrieve the next header satisfying the query
		var origin *types.Header

		if hashMode {
			if first {
				first = false

				origin = chain.GetHeaderByHash(query.Origin.Hash)
				if origin != nil {
					query.Origin.Number = origin.Number.Uint64()
				}
			} else {
				origin = chain.GetHeader(query.Origin.Hash, query.Origin.Number)
			}
		} else {
			origin = chain.GetHeaderByNumber(query.Origin.Number)
		}

		if origin == nil {
			break
		}

		if rlpData, err := rlp.EncodeToBytes(origin); err != nil {
			log.Crit("Unable to encode our own headers", "err", err)
		} else {
			headers = append(headers, rlp.RawValue(rlpData))
			bytes += common.StorageSize(len(rlpData))
		}
		// Advance to the next header of the query
		switch {
		case hashMode && query.Reverse:
			// Hash based traversal towards the genesis block
			ancestor := query.Skip + 1
			if ancestor == 0 {
				unknown = true
			} else {
				query.Origin.Hash, query.Origin.Number = chain.GetAncestor(query.Origin.Hash, query.Origin.Number, ancestor, &maxNonCanonical)
				unknown = (query.Origin.Hash == common.Hash{})
			}
		case hashMode && !query.Reverse:
			// Hash based traversal towards the leaf block
			var (
				current = origin.Number.Uint64()
				next    = current + query.Skip + 1
			)

			if next <= current {
				infos, _ := json.MarshalIndent(peer.Peer.Info(), "", "  ")
				peer.Log().Warn("GetBlockHeaders skip overflow attack", "current", current, "skip", query.Skip, "next", next, "attacker", infos)

				unknown = true
			} else {
				if header := chain.GetHeaderByNumber(next); header != nil {
					nextHash := header.Hash()
					expOldHash, _ := chain.GetAncestor(nextHash, next, query.Skip+1, &maxNonCanonical)

					if expOldHash == query.Origin.Hash {
						query.Origin.Hash, query.Origin.Number = nextHash, next
					} else {
						unknown = true
					}
				} else {
					unknown = true
				}
			}
		case query.Reverse:
			// Number based traversal towards the genesis block
			current := query.Origin.Number
			ancestor := current - (query.Skip + 1)
			if ancestor >= current { // check for underflow
				unknown = true
			} else {
				query.Origin.Number = ancestor
			}

		case !query.Reverse:
			current := query.Origin.Number
			next := current + query.Skip + 1
			if next <= current { // check for overflow
				unknown = true
			} else {
				query.Origin.Number = next
			}
		}
	}

	return headers
}

func serviceContiguousBlockHeaderQuery(chain *core.BlockChain, query *GetBlockHeadersRequest) []rlp.RawValue {
	count := query.Amount
	if count > maxHeadersServe {
		count = maxHeadersServe
	}

	if query.Origin.Hash == (common.Hash{}) {
		// Number mode, just return the canon chain segment. The backend
		// delivers in [N, N-1, N-2..] descending order, so we need to
		// accommodate for that.
		from := query.Origin.Number
		if !query.Reverse {
			from = from + count - 1
		}

		headers := chain.GetHeadersFrom(from, count)

		if !query.Reverse {
			for i, j := 0, len(headers)-1; i < j; i, j = i+1, j-1 {
				headers[i], headers[j] = headers[j], headers[i]
			}
		}

		return headers
	}
	// Hash mode.
	var (
		headers []rlp.RawValue
		hash    = query.Origin.Hash
		header  = chain.GetHeaderByHash(hash)
	)

	if header != nil {
		rlpData, _ := rlp.EncodeToBytes(header)
		headers = append(headers, rlpData)
	} else {
		// We don't even have the origin header
		return headers
	}

	num := header.Number.Uint64()
	if !query.Reverse {
		// Theoretically, we are tasked to deliver header by hash H, and onwards.
		// However, if H is not canon, we will be unable to deliver any descendants of
		// H.
		if canonHash := chain.GetCanonicalHash(num); canonHash != hash {
			// Not canon, we can't deliver descendants
			return headers
		}

		descendants := chain.GetHeadersFrom(num+count-1, count-1)
		for i, j := 0, len(descendants)-1; i < j; i, j = i+1, j-1 {
			descendants[i], descendants[j] = descendants[j], descendants[i]
		}

		headers = append(headers, descendants...)

		return headers
	}
	{ // Last mode: deliver ancestors of H
		for i := uint64(1); header != nil && i < count; i++ {
			header = chain.GetHeaderByHash(header.ParentHash)
			if header == nil {
				break
			}

			rlpData, _ := rlp.EncodeToBytes(header)
			headers = append(headers, rlpData)
		}

		return headers
	}
}

func handleGetBlockBodies(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block body retrieval message
	var query GetBlockBodiesPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	return observedBodyReply(backend, peer, query)
}

// ServiceGetBlockBodiesQuery assembles the response to a body query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetBlockBodiesQuery(chain *core.BlockChain, query GetBlockBodiesRequest) []rlp.RawValue {
	return serviceGetBlockBodiesQuery(chain, query, nil)
}

func serviceGetBlockBodiesQuery(chain *core.BlockChain, query GetBlockBodiesRequest, served func(common.Hash, int)) []rlp.RawValue {
	// Gather blocks until the fetch or network limits is reached
	var (
		bytes  int
		bodies []rlp.RawValue
	)

	for lookups, hash := range query {
		if bytes >= softResponseLimit || len(bodies) >= maxBodiesServe ||
			lookups >= 2*maxBodiesServe {
			break
		}

		data := chain.GetBodyRLP(hash)
		if len(data) == 0 {
			break // If we don't have this block's body, stop serving.
		}
		bodies = append(bodies, data)
		if served != nil {
			served(hash, len(data))
		}
		bytes += len(data)
	}

	return bodies
}

func handleGetReceipts68(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block receipts retrieval message
	var query GetReceiptsPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	response := ServiceGetReceiptsQuery68(backend.Chain(), query.GetReceiptsRequest)
	return peer.ReplyReceiptsRLP(query.RequestId, response)
}

func handleGetReceipts69(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block receipts retrieval message
	var query GetReceiptsPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	response := ServiceGetReceiptsQuery69(backend.Chain(), query.GetReceiptsRequest)
	return peer.ReplyReceiptsRLP(query.RequestId, response)
}

func handleGetReceipts70(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block receipts retrieval message
	var query GetReceiptsPacket70
	if err := msg.Decode(&query); err != nil {
		return err
	}
	response, lastBlockIncomplete := ServiceGetReceiptsQuery70(backend.Chain(), query.GetReceiptsRequest, query.FirstBlockReceiptIndex)

	return peer.ReplyReceiptsRLP70(query.RequestId, response, lastBlockIncomplete)
}

// ServiceGetReceiptsQuery68 assembles the response to a receipt query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetReceiptsQuery68(chain *core.BlockChain, query GetReceiptsRequest) []rlp.RawValue {
	// Gather state data until the fetch or network limits is reached
	var (
		bytes    int
		receipts []rlp.RawValue
	)

	for lookups, hash := range query {
		if bytes >= softResponseLimit || len(receipts) >= maxReceiptsServe ||
			lookups >= 2*maxReceiptsServe {
			break
		}
		// Retrieve the requested block's receipts. If receipts are empty
		// replace the result with an rlp.EmptyList to encode in the final
		// response.
		results := chain.GetReceiptsRLP(hash)
		if len(results) == 0 {
			if header := chain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
				break // Don't have this block's receipts, stop serving.
			}
			results = rlp.EmptyList
		} else {
			body := chain.GetBodyRLP(hash)
			if body == nil {
				break // The block body is missing, stop serving.
			}
			var err error
			results, err = blockReceiptsToNetwork68(results, body)
			if err != nil {
				log.Error("Error in block receipts conversion", "hash", hash, "err", err)
				break
			}
		}
		receipts = append(receipts, results)
		bytes += len(results)
	}
	return receipts
}

// gatherBlockReceipts assembles the storage-encoded receipt list of a block, together
// with a predicate reporting the position of the state-sync receipt within it.
//
// Before the Madhugiri hardfork the state-sync receipt is stored apart from the block
// receipts and has to be merged in at the tail; from Madhugiri onwards it is already
// among them and needs no special handling. The final return reports whether the block
// can be served at all.
func gatherBlockReceipts(chain *core.BlockChain, hash common.Hash, borCfg *params.BorConfig) (rlp.RawValue, func(index int) bool, bool) {
	noStateSyncReceipt := func(index int) bool { return false }

	number, ok := rawdb.ReadHeaderNumber(chain.DB(), hash)
	if !ok {
		return nil, nil, false
	}

	if borCfg != nil && borCfg.IsMadhugiri(big.NewInt(int64(number))) {
		allReceipts := chain.GetReceiptsRLP(hash)
		if len(allReceipts) == 0 {
			if header := chain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
				return nil, nil, false
			}
			// If receipts are empty, replace the result with an rlp.EmptyList
			// to encode in the final response.
			allReceipts = rlp.EmptyList
		}
		return allReceipts, noStateSyncReceipt, true
	}

	// Before the Madhugiri hardfork, the state-sync receipt is fetched separately from the
	// block receipts, decoded, merged into a single unit and re-encoded for the wire.
	normalReceipts := chain.GetReceiptsRLP(hash)
	var normalReceiptsDecoded []*types.ReceiptForStorage
	if len(normalReceipts) != 0 {
		if err := rlp.DecodeBytes(normalReceipts, &normalReceiptsDecoded); err != nil {
			log.Error("Failed to decode normal receipts", "err", err)
			return nil, nil, false
		}
	}

	borReceipt := chain.GetBorReceiptRLPByHash(hash)
	var borReceiptDecoded types.ReceiptForStorage
	if len(borReceipt) != 0 {
		if err := rlp.DecodeBytes(borReceipt, &borReceiptDecoded); err != nil {
			log.Error("Failed to decode state-sync receipt", "err", err)
			return nil, nil, false
		}
	}

	// Check if receipts are absent due to non existence or something else
	if len(normalReceipts) == 0 && len(borReceipt) == 0 {
		// Don't append empty receipt data for this block if either the local header is nil
		// or the receipt root of header denotes existence of receipt (i.e. is not empty hash)
		if header := chain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
			return nil, nil, false
		}
	}

	// Track existence of bor receipts for encoding
	var isBorReceiptPresent bool

	// We at least have some non-empty data for this block. Combine the receipts for encoding.
	var blockReceipts []*types.ReceiptForStorage = make([]*types.ReceiptForStorage, 0)
	if len(normalReceipts) != 0 {
		blockReceipts = append(blockReceipts, normalReceiptsDecoded...)
	}
	if len(borReceipt) != 0 {
		isBorReceiptPresent = true
		blockReceipts = append(blockReceipts, &borReceiptDecoded)
	}

	// isStateSyncReceipt denotes whether a receipt belongs to state-sync transaction or not
	isStateSyncReceipt := func(index int) bool {
		// If bor receipt is present, it will always be at the end of list
		if isBorReceiptPresent && index == len(blockReceipts)-1 {
			return true
		}
		return false
	}

	encodedBlockReceipts, err := rlp.EncodeToBytes(blockReceipts)
	if err != nil {
		return nil, nil, false
	}
	return encodedBlockReceipts, isStateSyncReceipt, true
}

// ServiceGetReceiptsQuery69 assembles the response to a receipt query.
// It does not send the bloom filters for the receipts
func ServiceGetReceiptsQuery69(chain *core.BlockChain, query GetReceiptsRequest) []rlp.RawValue {
	// Gather state data until the fetch or network limits is reached
	var (
		bytes    int
		receipts []rlp.RawValue
	)
	borCfg := chain.Config().Bor
	for lookups, hash := range query {
		if bytes >= softResponseLimit || len(receipts) >= maxReceiptsServe ||
			lookups >= 2*maxReceiptsServe {
			break
		}

		// If we don't have this block's receipts or body, stop serving.
		blockReceipts, isStateSyncReceipt, ok := gatherBlockReceipts(chain, hash, borCfg)
		if !ok {
			break
		}
		body := chain.GetBodyRLP(hash)
		if body == nil {
			break
		}

		results, _, err := blockReceiptsToNetwork69(blockReceipts, body, isStateSyncReceipt, receiptQueryParams{})
		if err != nil {
			log.Error("Error in block receipts conversion", "hash", hash, "err", err)
			break
		}

		receipts = append(receipts, results)
		bytes += len(results)
	}

	return receipts
}

// ServiceGetReceiptsQuery70 assembles the response to an eth/70 receipt query. Receipts
// of the first block positioned before firstBlockReceiptIndex are omitted, resuming a
// block that a previous response had to truncate. The second return value reports that
// the last block in the response is itself truncated.
func ServiceGetReceiptsQuery70(chain *core.BlockChain, query GetReceiptsRequest, firstBlockReceiptIndex uint64) ([]rlp.RawValue, bool) {
	var (
		bytes    int
		receipts []rlp.RawValue
	)
	borCfg := chain.Config().Bor
	for i, hash := range query {
		// The lookup bound matters more here than in the size and count bounds
		// above: a hash that misses is skipped without adding to either, so
		// without it a peer can make the node pay one database lookup per hash
		// for as many hashes as fit in a message. eth/68 and eth/69 bound it the
		// same way.
		if bytes >= softResponseLimit || len(receipts) >= maxReceiptsServe ||
			i >= 2*maxReceiptsServe {
			break
		}

		blockReceipts, isStateSyncReceipt, ok := gatherBlockReceipts(chain, hash, borCfg)
		if !ok {
			continue
		}
		body := chain.GetBodyRLP(hash)
		if body == nil {
			continue
		}

		q := receiptQueryParams{sizeLimit: uint64(maxMessageSize - receiptsPacketOverhead - bytes)}
		if i == 0 {
			q.firstIndex = firstBlockReceiptIndex
		}
		results, incomplete, err := blockReceiptsToNetwork69(blockReceipts, body, isStateSyncReceipt, q)
		if err != nil {
			log.Error("Error in block receipts conversion", "hash", hash, "err", err)
			break
		}
		if results == nil {
			// Not even the first receipt of this block fits in the remaining space. There
			// is no progress to report, so the response ends here.
			break
		}

		receipts = append(receipts, results)
		bytes += len(results)

		if incomplete {
			return receipts, true
		}
	}

	return receipts, false
}

func handleNewBlockhashes(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of new block announcements just arrived
	ann := new(NewBlockHashesPacket)
	if err := msg.Decode(ann); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	observeBlockAnnouncements(msg, *ann)
	if err := peer.checkAnnouncementRate(len(*ann), time.Now()); err != nil {
		observeInvalid(msg, peerpolicy.RateLimited)
		return err
	}
	// Mark the hashes as present at the remote node
	for _, block := range *ann {
		peer.markBlock(block.Hash)
	}
	// Deliver them all to the backend for queuing
	return backend.Handle(peer, ann)
}

func handleNewBlock(backend Backend, msg Decoder, peer *Peer) error {
	// Retrieve and decode the propagated block
	ann := new(NewBlockPacket)
	if err := msg.Decode(ann); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	if err := ann.sanityCheck(); err != nil {
		observeInvalid(msg, peerpolicy.InvalidBlock)
		return err
	}

	if hash := types.CalcUncleHash(ann.Block.Uncles()); hash != ann.Block.UncleHash() {
		observeInvalid(msg, peerpolicy.InvalidBlock)
		log.Warn("Propagated block has invalid uncles", "have", hash, "exp", ann.Block.UncleHash())
		return nil // TODO(karalabe): return error eventually, but wait a few releases
	}

	if hash := types.DeriveSha(ann.Block.Transactions(), trie.NewStackTrie(nil)); hash != ann.Block.TxHash() {
		observeInvalid(msg, peerpolicy.InvalidBlock)
		log.Warn("Propagated block has invalid body", "have", hash, "exp", ann.Block.TxHash())
		return nil // TODO(karalabe): return error eventually, but wait a few releases
	}

	msgTime := msg.Time()
	if prev := lastNewBlockPushUnix.Load(); prev > 0 {
		if delta := msgTime.Sub(time.Unix(0, prev)); delta > 0 {
			newBlockPushIntervalTimer.Update(delta)
		}
	}
	lastNewBlockPushUnix.Store(msgTime.UnixNano())

	ann.Block.ReceivedAt = msgTime
	ann.Block.ReceivedFrom = peer
	ann.Block.AnnouncedAt = &msgTime

	// Mark the peer as owning the block
	peer.markBlock(ann.Block.Hash())

	return backend.Handle(peer, ann)
}

func handleBlockHeaders(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of headers arrived to one of our previous requests
	res := new(BlockHeadersPacket)
	if err := msg.Decode(res); err != nil {
		return err
	}
	tresp := tracker.Response{ID: res.RequestId, MsgCode: BlockHeadersMsg, Size: res.List.Len()}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		observeUnmatchedResponse(msg, err)
		return fmt.Errorf("BlockHeaders: %w", err)
	}
	headers, err := res.List.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("BlockHeaders: %w", err)
	}

	metadata := func() interface{} {
		hashes := make([]common.Hash, len(headers))
		for i, header := range headers {
			hashes[i] = header.Hash()
		}

		return hashes
	}

	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: BlockHeadersMsg,
		Res:  (*BlockHeadersRequest)(&headers),
	}, metadata)
}

func handleBlockBodies(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of block bodies arrived to one of our previous requests
	res := new(BlockBodiesPacket)
	if err := msg.Decode(res); err != nil {
		return err
	}

	// Check against the request.
	length := res.List.Len()
	tresp := tracker.Response{ID: res.RequestId, MsgCode: BlockBodiesMsg, Size: length}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		observeUnmatchedResponse(msg, err)
		return fmt.Errorf("BlockBodies: %w", err)
	}

	// Collect items and dispatch.
	items, err := res.List.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("BlockBodies: %w", err)
	}
	for i := range items {
		if err := checkBodyItemCounts(&items[i]); err != nil {
			observeInvalid(msg, peerpolicy.InvalidEncoding)
			return fmt.Errorf("BlockBodies: body %d: %w", i, err)
		}
	}
	metadata := func() any { return hashBodyParts(items) }
	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: BlockBodiesMsg,
		Res:  (*BlockBodiesResponse)(&items),
	}, metadata)
}

const (
	minTxEncodedSize         = 1 + 9      // Min size for RLP encoded transaction
	minWithdrawalEncodedSize = 1 + 3 + 21 // Min size for RLP encoded withdrawal
	minReceiptEncodedSize    = 1 + 4      // Min size for RLP encoded receipt
	minHeaderEncodedSize     = 496        // Min size for RLP encoded header (pre-London fields only)
)

// checkBodyItemCounts rejects bodies containing more transactions, uncles or
// withdrawals than can fit in their encoded size. The block fetcher decodes
// uncles before matching their hash, so they are bounded here too.
func checkBodyItemCounts(body *BlockBody) error {
	if n := body.Transactions.Len(); n*minTxEncodedSize > len(body.Transactions.Content()) {
		return fmt.Errorf("%d transactions in %d bytes", n, len(body.Transactions.Content()))
	}
	if n := body.Uncles.Len(); n*minHeaderEncodedSize > len(body.Uncles.Content()) {
		return fmt.Errorf("%d uncles in %d bytes", n, len(body.Uncles.Content()))
	}
	if body.Withdrawals != nil {
		if n := body.Withdrawals.Len(); n*minWithdrawalEncodedSize > len(body.Withdrawals.Content()) {
			return fmt.Errorf("%d withdrawals in %d bytes", n, len(body.Withdrawals.Content()))
		}
	}
	return nil
}

// checkReceiptItemCounts rejects receipt lists containing more receipts than
// can fit in their encoded size.
func checkReceiptItemCounts[L ReceiptsList](lists []L) error {
	for i, list := range lists {
		items := list.rawItems()
		if n := items.Len(); n*minReceiptEncodedSize > len(items.Content()) {
			return fmt.Errorf("list %d: %d receipts in %d bytes", i, n, len(items.Content()))
		}
	}
	return nil
}

// BlockBodyHashes contains the lists of block body part roots for a list of block bodies.
type BlockBodyHashes struct {
	TransactionRoots []common.Hash
	WithdrawalRoots  []common.Hash
	UncleHashes      []common.Hash
}

func hashBodyParts(items []BlockBody) BlockBodyHashes {
	h := BlockBodyHashes{
		TransactionRoots: make([]common.Hash, len(items)),
		WithdrawalRoots:  make([]common.Hash, len(items)),
		UncleHashes:      make([]common.Hash, len(items)),
	}
	hasher := trie.NewStackTrie(nil)
	for i, body := range items {
		// txs
		txsList := newDerivableRawList(&body.Transactions, writeTxForHash)
		h.TransactionRoots[i] = types.DeriveSha(txsList, hasher)
		// uncles
		if body.Uncles.Len() == 0 {
			h.UncleHashes[i] = types.EmptyUncleHash
		} else {
			h.UncleHashes[i] = crypto.Keccak256Hash(body.Uncles.Bytes())
		}
		// withdrawals
		if body.Withdrawals != nil {
			wdlist := newDerivableRawList(body.Withdrawals, nil)
			h.WithdrawalRoots[i] = types.DeriveSha(wdlist, hasher)
		}
	}
	return h
}

// derivableRawList implements types.DerivableList for a serialized RLP list.
type derivableRawList struct {
	data    []byte
	offsets []uint32
	write   func([]byte, *bytes.Buffer)
}

func newDerivableRawList[T any](list *rlp.RawList[T], write func([]byte, *bytes.Buffer)) *derivableRawList {
	dl := derivableRawList{data: list.Content(), write: write}
	if dl.write == nil {
		// default transform is identity
		dl.write = func(b []byte, buf *bytes.Buffer) { buf.Write(b) }
	}
	// Assert to ensure 32-bit offsets are valid. This can never trigger
	// unless a block body component or p2p receipt list is larger than 4GB.
	if uint(len(dl.data)) > math.MaxUint32 {
		panic("list data too big for derivableRawList")
	}
	it := list.ContentIterator()
	dl.offsets = make([]uint32, list.Len())
	for i := 0; it.Next(); i++ {
		dl.offsets[i] = uint32(it.Offset())
	}
	return &dl
}

// Len returns the number of items in the list.
func (dl *derivableRawList) Len() int {
	return len(dl.offsets)
}

// EncodeIndex writes the i'th item to the buffer.
func (dl *derivableRawList) EncodeIndex(i int, buf *bytes.Buffer) {
	start := dl.offsets[i]
	end := uint32(len(dl.data))
	if i != len(dl.offsets)-1 {
		end = dl.offsets[i+1]
	}
	dl.write(dl.data[start:end], buf)
}

// writeTxForHash changes a transaction in 'network encoding' into the format used for
// the transactions MPT.
func writeTxForHash(tx []byte, buf *bytes.Buffer) {
	k, content, _, _ := rlp.Split(tx)
	if k == rlp.List {
		buf.Write(tx) // legacy tx
	} else {
		buf.Write(content) // typed tx
	}
}

func handleReceipts[L ReceiptsList](backend Backend, msg Decoder, peer *Peer) error {
	// A batch of receipts arrived to one of our previous requests
	res := new(ReceiptsPacket[L])
	if err := msg.Decode(res); err != nil {
		return err
	}

	tresp := tracker.Response{ID: res.RequestId, MsgCode: ReceiptsMsg, Size: res.List.Len()}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		observeUnmatchedResponse(msg, err)
		return fmt.Errorf("Receipts: %w", err)
	}

	// Assign temporary hashing buffer to each list item, the same buffer is shared
	// between all receipt list instances.
	receiptLists, err := res.List.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Receipts: %w", err)
	}
	if err := checkReceiptItemCounts(receiptLists); err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Receipts: %w", err)
	}
	buffers := new(receiptListBuffers)
	for i := range receiptLists {
		receiptLists[i].setBuffers(buffers)
	}

	// The `metadata` function below was used earlier to calculate `ReceiptHash` which is further
	// used to validate against `header.ReceiptHash`. By default, state-sync receipts (which are
	// appended at the end of list for a block) are excluded from the `ReceiptHash` calculation.
	// After the Madhugiri hardfork, they should be included in the calculation. We don't have
	// access to block number here so we can't determine whether to exclude or not. Instead, just
	// ignore the `metadata` function and pass on the whole receipt list as is. The receipt queue
	// handler which has access to block number will take care of the exclusion if needed.
	metadata := func() interface{} {
		return nil
	}

	// Assign the decoded receipt list to the result of `Response` packet.
	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: ReceiptsMsg,
		Res:  &receiptLists,
	}, metadata)
}

func handleReceipts70(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of receipts arrived to one of our previous requests
	res := new(ReceiptsPacket70)
	if err := msg.Decode(res); err != nil {
		return err
	}

	tresp := tracker.Response{ID: res.RequestId, MsgCode: ReceiptsMsg, Size: res.List.Len()}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		observeUnmatchedResponse(msg, err)
		return fmt.Errorf("Receipts: %w", err)
	}
	receiptLists, err := res.List.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Receipts: %w", err)
	}
	if err := checkReceiptItemCounts(receiptLists); err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Receipts: %w", err)
	}

	// Assign temporary hashing buffer to each list item, the same buffer is shared
	// between all receipt list instances.
	buffers := new(receiptListBuffers)
	for i := range receiptLists {
		receiptLists[i].setBuffers(buffers)
	}

	if err := peer.bufferReceipts(res.RequestId, receiptLists, res.LastBlockIncomplete); err != nil {
		return err
	}
	if res.LastBlockIncomplete {
		// The response stops part way through a block, so ask the same peer to continue
		// it. Nothing is handed to the downloader until the block is whole.
		return peer.requestPartialReceipts(res.RequestId)
	}
	if complete := peer.flushReceipts(res.RequestId); complete != nil {
		receiptLists = complete
	}

	// The metadata function is a no-op for the same reason as in handleReceipts: the
	// block number that decides whether the state-sync receipt takes part in the receipt
	// hash is only known to the receipt queue, which validates the reassembled list.
	metadata := func() interface{} {
		return nil
	}

	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: ReceiptsMsg,
		Res:  &receiptLists,
	}, metadata)
}

// EncodeReceiptsAndPrepareHasher encodes a list of receipts to the storage format (does not
// include TxType field). It also returns a function which calculates `ReceiptHash` of a receipt list
// based on the whether we've crossed the Madhugiri hardfork or not.
func EncodeReceiptsAndPrepareHasher(packet interface{}, borCfg *params.BorConfig) (ReceiptsRLPResponse, func(int, *big.Int) common.Hash, error) {
	// Extract receipts based on type. Add/remove support for new types here as needed.
	var (
		receipts             ReceiptsRLPResponse
		getReceiptListHashes func(int, *big.Int) common.Hash
		err                  error
	)
	switch packet := packet.(type) {
	case []*ReceiptList68:
		receiptList := packet
		receipts, getReceiptListHashes, err = encodeReceiptsAndPrepareHasher(receiptList, borCfg)
	case *[]*ReceiptList68:
		receiptList := packet
		receipts, getReceiptListHashes, err = encodeReceiptsAndPrepareHasher(*receiptList, borCfg)
	case []*ReceiptList69:
		receiptList := packet
		receipts, getReceiptListHashes, err = encodeReceiptsAndPrepareHasher(receiptList, borCfg)
	case *[]*ReceiptList69:
		receiptList := packet
		receipts, getReceiptListHashes, err = encodeReceiptsAndPrepareHasher(*receiptList, borCfg)
	default:
		// This shouldn't happen unless there's a bug in identifying type of receipt list
		// or there's a new type which isn't handled here.
		log.Debug("EncodeReceiptsAndPrepareHasher: unsupported receipt list type", "type", fmt.Sprintf("%T", packet))
		return nil, nil, nil
	}
	return receipts, getReceiptListHashes, err
}

// encodeReceiptsAndPrepareHasher is an internal generic function for all receipt types
func encodeReceiptsAndPrepareHasher[L ReceiptsList](receipts []L, borCfg *params.BorConfig) (ReceiptsRLPResponse, func(int, *big.Int) common.Hash, error) {
	var encodedReceipts ReceiptsRLPResponse = make(ReceiptsRLPResponse, len(receipts))
	for i := range receipts {
		enc, err := receipts[i].EncodeForStorage()
		if err != nil {
			return nil, nil, fmt.Errorf("invalid receipt list %d: %w", i, err)
		}
		encodedReceipts[i] = enc
	}

	hasher := trie.NewStackTrie(nil)
	calculateReceiptHashes := func(index int, number *big.Int) common.Hash {
		// For non-bor chains or post-Madhugiri blocks, include all receipts in hash
		if borCfg == nil || borCfg.IsMadhugiri(number) {
			return types.DeriveSha(receipts[index].Derivable(), hasher)
		}
		// Pre-Madhugiri bor blocks: exclude state-sync receipt from hash calculation
		receipts[index].ExcludeStateSyncReceipt()
		return types.DeriveSha(receipts[index].Derivable(), hasher)
	}

	return encodedReceipts, calculateReceiptHashes, nil
}

func handleNewPooledTransactionHashes(backend Backend, msg Decoder, peer *Peer) error {
	// New transaction announcement arrived, make sure we have
	// a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		return nil
	}
	ann := new(NewPooledTransactionHashesPacket)
	if err := msg.Decode(ann); err != nil {
		return err
	}
	if len(ann.Hashes) != len(ann.Types) || len(ann.Hashes) != len(ann.Sizes) {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("NewPooledTransactionHashes: invalid len of fields in %v %v %v", len(ann.Hashes), len(ann.Types), len(ann.Sizes))
	}
	observeAnnouncements(msg, ann.Hashes)
	// Schedule all the unknown hashes for retrieval
	for _, hash := range ann.Hashes {
		peer.markTransaction(hash)
	}
	return backend.Handle(peer, ann)
}

func handleGetPooledTransactions(backend Backend, msg Decoder, peer *Peer) error {
	reservation, err := peer.reservePooledReply()
	if err != nil {
		return err
	}
	defer reservation.release()
	// Decode the pooled transactions retrieval message
	var query GetPooledTransactionsPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	hashes, txs := answerGetPooledTransactions(backend, query.GetPooledTransactionsRequest)
	return peer.replyPooledTransactionsRLP(query.RequestId, hashes, txs, reservation)
}

func answerGetPooledTransactions(backend Backend, query GetPooledTransactionsRequest) ([]common.Hash, []rlp.RawValue) {
	// Gather transactions until the fetch or network limits is reached
	var (
		bytes  int
		hashes []common.Hash
		txs    []rlp.RawValue
	)

	for lookups, hash := range query {
		if bytes >= softResponseLimit || lookups >= maxPooledTxsServe {
			break
		}
		// Retrieve the requested transaction, skipping if unknown to us
		encoded := backend.TxPool().GetRLP(hash)
		if len(encoded) == 0 {
			continue
		}
		hashes = append(hashes, hash)
		txs = append(txs, encoded)
		bytes += len(encoded)
	}

	return hashes, txs
}

func handleTransactions(backend Backend, msg Decoder, peer *Peer) error {
	// Transactions arrived, make sure we have a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		observeIgnoredTransactions(msg)
		return nil
	}
	// Transactions can be processed, parse all of them and deliver to the pool
	var txs TransactionsPacket
	if err := msg.Decode(&txs); err != nil {
		return err
	}
	if txs.Len() > maxTransactionAnnouncements {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Transactions: too many transactions")
	}
	list, err := txs.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("Transactions: %w", err)
	}
	if err := checkTransactions(msg, peer, list, "Transactions"); err != nil {
		return err
	}
	txs.decoded = list

	if err := backend.Handle(peer, &txs); err != nil {
		return err
	}
	observeForwardedTransactions(msg)
	return nil
}

func handlePooledTransactions(backend Backend, msg Decoder, peer *Peer) error {
	// Transactions arrived, make sure we have a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		observeIgnoredTransactions(msg)
		return nil
	}

	// Check against request and decode.
	var resp PooledTransactionsPacket
	if err := msg.Decode(&resp); err != nil {
		return err
	}
	// A reply that matches no pending request is not fatal: the fetcher
	// correlates deliveries itself and treats unknown ones as neutral.
	tresp := tracker.Response{
		ID:      resp.RequestId,
		MsgCode: PooledTransactionsMsg,
		Size:    resp.List.Len(),
	}
	if err := peer.tracker.Fulfil(tresp); err != nil && !errors.Is(err, tracker.ErrNoMatchingRequest) {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("PooledTransactions: %w", err)
	}
	// Unmatched replies skip the tracker's size check, so bound them by the
	// largest request we send before decoding.
	if resp.List.Len() > maxPooledTxsServe {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("PooledTransactions: too many transactions")
	}
	list, err := resp.List.Items()
	if err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return fmt.Errorf("PooledTransactions: %w", err)
	}
	if err := checkTransactions(msg, peer, list, "PooledTransactions"); err != nil {
		return err
	}
	resp.decoded = list

	if err := backend.Handle(peer, &resp); err != nil {
		return err
	}
	observeForwardedTransactions(msg)
	return nil
}

// observeUnmatchedResponse records evidence for a response that contradicts
// its request. A response to an expired request is not evidence, since honest
// peers can be slow.
func observeUnmatchedResponse(msg Decoder, err error) {
	if !errors.Is(err, tracker.ErrNoMatchingRequest) {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
	}
}

// checkTransactions rejects nil and duplicate transactions and marks the rest
// as known to the peer.
func checkTransactions(msg Decoder, peer *Peer, txs []*types.Transaction, kind string) error {
	seen := make(map[common.Hash]struct{})
	for i, tx := range txs {
		if tx == nil {
			observeInvalid(msg, peerpolicy.InvalidTransaction)
			return fmt.Errorf("%s: transaction %d is nil", kind, i)
		}

		hash := tx.Hash()
		if _, exists := seen[hash]; exists {
			observeInvalid(msg, peerpolicy.InvalidEncoding)
			return fmt.Errorf("%s: multiple copies of the same hash %v", kind, hash)
		}
		seen[hash] = struct{}{}
		peer.markTransaction(hash)
	}
	return nil
}

func handleBlockRangeUpdate(backend Backend, msg Decoder, peer *Peer) error {
	var update BlockRangeUpdatePacket
	if err := msg.Decode(&update); err != nil {
		return err
	}
	if err := update.Validate(); err != nil {
		observeInvalid(msg, peerpolicy.InvalidEncoding)
		return err
	}
	// We don't do anything with these messages for now, just store them on the peer.
	peer.lastRange.Store(&update)
	return nil
}
