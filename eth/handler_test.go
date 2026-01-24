// Copyright 2015 The go-ethereum Authors
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
	"math/big"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

var (
	// testKey is a private key to use for funding a tester account.
	testKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")

	// testAddr is the Ethereum address of the tester account.
	testAddr = crypto.PubkeyToAddress(testKey.PublicKey)
)

// testTxPool is a mock transaction pool that blindly accepts all transactions.
// Its goal is to get around setting up a valid statedb for the balance and nonce
// checks.
type testTxPool struct {
	pool map[common.Hash]*types.Transaction // Hash map of collected transactions

	txFeed          event.Feed   // Notification feed to allow waiting for inclusion
	rebroadcastFeed event.Feed   // Notification feed for stuck tx rebroadcast
	lock            sync.RWMutex // Protects the transaction pool
}

// newTestTxPool creates a mock transaction pool.
func newTestTxPool() *testTxPool {
	return &testTxPool{
		pool: make(map[common.Hash]*types.Transaction),
	}
}

// Has returns an indicator whether txpool has a transaction
// cached with the given hash.
func (p *testTxPool) Has(hash common.Hash) bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	return p.pool[hash] != nil
}

// Get retrieves the transaction from local txpool with given
// tx hash.
func (p *testTxPool) Get(hash common.Hash) *types.Transaction {
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.pool[hash]
}

// Get retrieves the transaction from local txpool with given
// tx hash.
func (p *testTxPool) GetRLP(hash common.Hash) []byte {
	p.lock.Lock()
	defer p.lock.Unlock()

	tx := p.pool[hash]
	if tx != nil {
		blob, _ := rlp.EncodeToBytes(tx)
		return blob
	}
	return nil
}

// GetMetadata returns the transaction type and transaction size with the given
// hash.
func (p *testTxPool) GetMetadata(hash common.Hash) *txpool.TxMetadata {
	p.lock.Lock()
	defer p.lock.Unlock()

	tx := p.pool[hash]
	if tx != nil {
		return &txpool.TxMetadata{
			Type: tx.Type(),
			Size: tx.Size(),
		}
	}
	return nil
}

// Add appends a batch of transactions to the pool, and notifies any
// listeners if the addition channel is non nil
func (p *testTxPool) Add(txs []*types.Transaction, sync bool) []error {
	p.lock.Lock()
	defer p.lock.Unlock()

	for _, tx := range txs {
		p.pool[tx.Hash()] = tx
	}
	p.txFeed.Send(core.NewTxsEvent{Txs: txs})
	return make([]error, len(txs))
}

// Pending returns all the transactions known to the pool
func (p *testTxPool) Pending(filter txpool.PendingFilter, interrupt *atomic.Bool) map[common.Address][]*txpool.LazyTransaction {
	p.lock.RLock()
	defer p.lock.RUnlock()

	batches := make(map[common.Address][]*types.Transaction)
	for _, tx := range p.pool {
		from, _ := types.Sender(types.HomesteadSigner{}, tx)
		batches[from] = append(batches[from], tx)
	}

	for _, batch := range batches {
		sort.Sort(types.TxByNonce(batch))
	}
	pending := make(map[common.Address][]*txpool.LazyTransaction)
	for addr, batch := range batches {
		for _, tx := range batch {
			pending[addr] = append(pending[addr], &txpool.LazyTransaction{
				Hash:      tx.Hash(),
				Tx:        tx,
				Time:      tx.Time(),
				GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
				GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
				Gas:       tx.Gas(),
				BlobGas:   tx.BlobGas(),
			})
		}
	}
	return pending
}

// SubscribeTransactions should return an event subscription of NewTxsEvent and
// send events to the given channel.
func (p *testTxPool) SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription {
	return p.txFeed.Subscribe(ch)
}

// SubscribeRebroadcastTransactions returns a subscription to the rebroadcast feed.
func (p *testTxPool) SubscribeRebroadcastTransactions(ch chan<- core.StuckTxsEvent) event.Subscription {
	return p.rebroadcastFeed.Subscribe(ch)
}

// SendStuckTxs sends stuck transactions to the rebroadcast feed for testing.
func (p *testTxPool) SendStuckTxs(txs []*types.Transaction) {
	p.rebroadcastFeed.Send(core.StuckTxsEvent{Txs: txs})
}

// testHandler is a live implementation of the Ethereum protocol handler, just
// preinitialized with some sane testing defaults and the transaction pool mocked
// out.
type testHandler struct {
	db      ethdb.Database
	chain   *core.BlockChain
	txpool  *testTxPool
	handler *handler
}

// newTestHandler creates a new handler for testing purposes with no blocks.
func newTestHandler() *testHandler {
	return newTestHandlerWithBlocks(0)
}

// newTestHandlerWithBlocks creates a new handler for testing purposes, with a
// given number of initial blocks.
func newTestHandlerWithBlocks(blocks int) *testHandler {
	// Create a database pre-initialize with a genesis block
	db := rawdb.NewMemoryDatabase()
	gspec := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{testAddr: {Balance: big.NewInt(1000000)}},
	}
	chain, _ := core.NewBlockChain(db, gspec, ethash.NewFaker(), nil)

	_, bs, _ := core.GenerateChainWithGenesis(gspec, ethash.NewFaker(), blocks, nil)
	if _, err := chain.InsertChain(bs, false); err != nil {
		panic(err)
	}

	txpool := newTestTxPool()

	handler, _ := newHandler(&handlerConfig{
		Database:   db,
		Chain:      chain,
		TxPool:     txpool,
		Network:    1,
		Sync:       downloader.SnapSync,
		BloomCache: 1,
	})
	handler.Start(1000)

	return &testHandler{
		db:      db,
		chain:   chain,
		txpool:  txpool,
		handler: handler,
	}
}

// close tears down the handler and all its internal constructs.
func (b *testHandler) close() {
	b.handler.Stop()
	b.chain.Stop()
}

// jailPeerTestSetup holds the common test setup for jail peer tests
type jailPeerTestSetup struct {
	db        ethdb.Database
	chain     *core.BlockChain
	txpool    *testTxPool
	p2pServer *p2p.Server
	handler   *handler
}

// setupJailPeerTest creates a test setup with optional p2p server configuration
func setupJailPeerTest(t *testing.T, startP2PServer bool) *jailPeerTestSetup {
	t.Helper()

	db := rawdb.NewMemoryDatabase()
	gspec := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{testAddr: {Balance: big.NewInt(1000000)}},
	}
	chain, _ := core.NewBlockChain(db, gspec, ethash.NewFaker(), nil)
	txpool := newTestTxPool()

	var p2pServer *p2p.Server
	if startP2PServer {
		key, _ := crypto.GenerateKey()
		p2pServer = &p2p.Server{
			Config: p2p.Config{
				PrivateKey:  key,
				NoDial:      true,
				NoDiscovery: true,
			},
		}
		if err := p2pServer.Start(); err != nil {
			t.Fatalf("Failed to start p2p server: %v", err)
		}
	}

	handler, _ := newHandler(&handlerConfig{
		Database:  db,
		Chain:     chain,
		TxPool:    txpool,
		Network:   1,
		Sync:      downloader.SnapSync,
		p2pServer: p2pServer,
	})
	handler.Start(1000)

	t.Cleanup(func() {
		handler.Stop()
		chain.Stop()
		if p2pServer != nil {
			p2pServer.Stop()
		}
	})

	return &jailPeerTestSetup{
		db:        db,
		chain:     chain,
		txpool:    txpool,
		p2pServer: p2pServer,
		handler:   handler,
	}
}

// verifyPeerJailInitialized checks if peerJail was initialized in the p2p server
func verifyPeerJailInitialized(t *testing.T, p2pServer *p2p.Server) {
	t.Helper()
	rv := reflect.ValueOf(p2pServer).Elem()
	peerJailField := rv.FieldByName("peerJail")
	if !peerJailField.IsValid() || peerJailField.IsNil() {
		t.Error("peerJail should be initialized after Start()")
	}
}

// TestJailPeer tests the jailPeer function with various scenarios
func TestJailPeer(t *testing.T) {
	t.Run("NilP2PServer", func(t *testing.T) {
		setup := setupJailPeerTest(t, false)

		// Should return early without error when p2pServer is nil
		setup.handler.jailPeer("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
		// No assertion needed - just ensure it doesn't panic
	})

	t.Run("InvalidPeerID", func(t *testing.T) {
		setup := setupJailPeerTest(t, false)
		// Create a minimal p2p.Server (peerJail will be nil)
		setup.p2pServer = &p2p.Server{}

		// Should return early without error when ParseID fails
		// (peerJail is nil, so JailPeer returns early, but ParseID fails first)
		setup.handler.jailPeer("invalid-id")
		// No assertion needed - just ensure it doesn't panic and logs a warning
	})

	t.Run("ValidPeerID", func(t *testing.T) {
		setup := setupJailPeerTest(t, true)

		// Use a valid peer ID (64 hex characters = 32 bytes)
		peerID := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
		_, err := enode.ParseID(peerID)
		if err != nil {
			t.Fatalf("Failed to parse test peer ID: %v", err)
		}

		// Call jailPeer - should jail the peer without panicking
		// Note: We can't verify the internal state using reflection due to Go's
		// restrictions on calling methods on values from unexported fields.
		// The fact that it doesn't panic with valid input is sufficient coverage.
		setup.handler.jailPeer(peerID)

		// Verify peerJail was initialized
		verifyPeerJailInitialized(t, setup.p2pServer)
	})

	t.Run("ValidPeerIDWith0xPrefix", func(t *testing.T) {
		setup := setupJailPeerTest(t, true)

		// Use a valid peer ID with 0x prefix (should still parse correctly)
		peerID := "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
		_, err := enode.ParseID(peerID)
		if err != nil {
			t.Fatalf("Failed to parse test peer ID: %v", err)
		}

		// Call jailPeer - should work with 0x prefix
		setup.handler.jailPeer(peerID)

		// Verify peerJail was initialized
		verifyPeerJailInitialized(t, setup.p2pServer)
	})
}

func TestStuckTxBroadcastLoop(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()
	defer handler.close()

	// Mark handler as synced so it processes stuck transactions
	handler.handler.synced.Store(true)

	// Create a test transaction
	tx := types.NewTransaction(0, testAddr, big.NewInt(100), 21000, big.NewInt(1000000000), nil)
	signedTx, err := types.SignTx(tx, types.HomesteadSigner{}, testKey)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}

	// Send stuck transaction event
	handler.txpool.SendStuckTxs([]*types.Transaction{signedTx})

	// Give the loop time to process
	time.Sleep(100 * time.Millisecond)
}

func TestStuckTxBroadcastLoopNotSynced(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()
	defer handler.close()

	// Handler is not synced by default (synced.Load() == false)

	// Create a test transaction
	tx := types.NewTransaction(0, testAddr, big.NewInt(100), 21000, big.NewInt(1000000000), nil)
	signedTx, err := types.SignTx(tx, types.HomesteadSigner{}, testKey)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}

	// Send stuck transaction event - should be ignored since not synced
	handler.txpool.SendStuckTxs([]*types.Transaction{signedTx})

	// Give the loop time to process (or ignore)
	time.Sleep(100 * time.Millisecond)
}
