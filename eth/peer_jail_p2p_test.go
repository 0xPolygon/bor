// Copyright 2026 The go-ethereum Authors
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
	"errors"
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/downloader"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
)

func TestFakeEthPeerPrunedSidechainIsLocallyJailed(t *testing.T) {
	fixture := newPrunedSidechainP2PFixture(t)

	handler, cleanup := newPeerJailP2PHandler(t, fixture.db, fixture.genesis, fixture.chain)
	defer cleanup()

	host := startEthP2PServer(t, handler)
	fake := startFakeETH69Server(t, fixture)

	host.AddPeer(fake.server.Self())
	waitForEthPeer(t, handler, fake.server.Self().ID().String())

	peerID := fake.server.Self().ID().String()
	beforePeers := host.PeerCount()
	if beforePeers != 1 {
		t.Fatalf("p2p peer count mismatch before sync: have %d, want 1", beforePeers)
	}

	err := handler.downloader.LegacySync(peerID, fixture.head.Hash(), fixture.td, nil, downloader.FullSync)
	if err == nil || !strings.Contains(err.Error(), "sidechain ghost-state attack") {
		t.Fatalf("sync error mismatch: have %v, want sidechain ghost-state attack", err)
	}
	if afterPeers := host.PeerCount(); afterPeers != beforePeers {
		t.Fatalf("p2p peer count changed: have %d, want %d", afterPeers, beforePeers)
	}
	if handler.peers.peer(peerID) == nil {
		t.Fatalf("fake peer was removed from eth peer set")
	}

	err = handler.downloader.LegacySync(peerID, fixture.head.Hash(), fixture.td, nil, downloader.FullSync)
	if !errors.Is(err, downloader.ErrPeerBackedOff) {
		t.Fatalf("backed off sync error mismatch: have %v, want %v", err, downloader.ErrPeerBackedOff)
	}
}

type prunedSidechainP2PFixture struct {
	db      ethdb.Database
	genesis *core.Genesis
	chain   *core.BlockChain
	remote  *fakeEthChain
	head    *types.Block
	td      *big.Int
}

func newPrunedSidechainP2PFixture(t *testing.T) *prunedSidechainP2PFixture {
	t.Helper()

	chainConfig := *params.TestChainConfig
	chainConfig.TerminalTotalDifficulty = big.NewInt(math.MaxInt64)
	engine := ethash.NewFaker()
	genesis, addTx := newSidechainP2PGenesis(t, &chainConfig)
	genDB, blocks := generateCanonicalP2PChain(genesis, engine, addTx)
	db := openP2PFixtureDB(t)
	chain := newP2PFixtureChain(t, db, genesis, engine, blocks)
	fork := generatePrunedSidechain(t, chain, genesis, engine, genDB, blocks, addTx)

	remote := newFakeEthChain(chain.Genesis(), blocks[:len(blocks)-state.TriesInMemory], fork)
	head := fork[len(fork)-1]
	td := new(big.Int).Add(chain.GetTd(chain.CurrentBlock().Hash(), chain.CurrentBlock().Number.Uint64()), big.NewInt(1))

	return &prunedSidechainP2PFixture{
		db:      db,
		genesis: genesis,
		chain:   chain,
		remote:  remote,
		head:    head,
		td:      td,
	}
}

func newSidechainP2PGenesis(t *testing.T, chainConfig *params.ChainConfig) (*core.Genesis, func(uint64, *core.BlockGen)) {
	t.Helper()

	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	genesis := &core.Genesis{
		Config: chainConfig,
		Alloc: types.GenesisAlloc{
			addr: {Balance: big.NewInt(math.MaxInt64)},
		},
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	signer := types.LatestSigner(genesis.Config)
	addTx := func(nonce uint64, b *core.BlockGen) {
		tx, err := types.SignTx(types.NewTransaction(nonce, common.HexToAddress("deadbeef"), big.NewInt(100), 21000, b.BaseFee(), nil), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		b.AddTx(tx)
	}
	return genesis, addTx
}

func generateCanonicalP2PChain(genesis *core.Genesis, engine consensus.Engine, addTx func(uint64, *core.BlockGen)) (ethdb.Database, []*types.Block) {
	genDB, blocks, _ := core.GenerateChainWithGenesis(genesis, engine, 2*state.TriesInMemory, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{1})
		addTx(uint64(i), b)
		b.SetExtra([]byte("canonical"))
	})
	return genDB, blocks
}

func openP2PFixtureDB(t *testing.T) ethdb.Database {
	t.Helper()

	datadir := t.TempDir()
	pdb, err := pebble.New(datadir, 0, 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := rawdb.Open(pdb, rawdb.OpenOptions{Ancient: filepath.Join(datadir, "ancient")})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newP2PFixtureChain(t *testing.T, db ethdb.Database, genesis *core.Genesis, engine consensus.Engine, blocks []*types.Block) *core.BlockChain {
	t.Helper()

	chain, err := core.NewBlockChain(db, genesis, engine, core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		chain.Stop()
		db.Close()
	})

	if n, err := chain.InsertChain(blocks, false); err != nil {
		t.Fatalf("failed to insert canonical block %d: %v", n, err)
	}
	return chain
}

func generatePrunedSidechain(t *testing.T, chain *core.BlockChain, genesis *core.Genesis, engine consensus.Engine, genDB ethdb.Database, blocks []*types.Block, addTx func(uint64, *core.BlockGen)) []*types.Block {
	t.Helper()

	parent := blocks[len(blocks)-state.TriesInMemory-1]
	if chain.HasBlockAndState(parent.Hash(), parent.NumberU64()) {
		t.Fatalf("parent state is still available: number %d", parent.NumberU64())
	}
	fork, _ := core.GenerateChain(genesis.Config, parent, engine, genDB, state.TriesInMemory+2, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{1})
		addTx(parent.NumberU64()+uint64(i), b)
		b.SetExtra([]byte("side"))
	})

	canonical := chain.GetBlockByNumber(fork[0].NumberU64())
	if canonical == nil {
		t.Fatalf("canonical block %d not found", fork[0].NumberU64())
	}
	if canonical.Hash() == fork[0].Hash() {
		t.Fatalf("side block hash matches canonical block hash")
	}
	if canonical.Root() != fork[0].Root() {
		t.Fatalf("side block root mismatch: have %v, want %v", fork[0].Root(), canonical.Root())
	}
	return fork
}

func newPeerJailP2PHandler(t *testing.T, db ethdb.Database, genesis *core.Genesis, chain *core.BlockChain) (*handler, func()) {
	t.Helper()

	handler, err := newHandler(&handlerConfig{
		Database:   db,
		Chain:      chain,
		TxPool:     newTestTxPool(),
		Network:    genesis.Config.ChainID.Uint64(),
		Sync:       downloader.FullSync,
		BloomCache: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.Start(1000)

	return handler, func() {
		handler.Stop()
	}
}

func startEthP2PServer(t *testing.T, handler *handler) *p2p.Server {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	server := &p2p.Server{
		Config: p2p.Config{
			PrivateKey:  key,
			ListenAddr:  "127.0.0.1:0",
			NoDiscovery: true,
			MaxPeers:    10,
			Protocols:   ethproto.MakeProtocols((*ethHandler)(handler), handler.networkID, nil),
		},
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)

	return server
}

type fakeETH69Server struct {
	server *p2p.Server
}

func startFakeETH69Server(t *testing.T, fixture *prunedSidechainP2PFixture) *fakeETH69Server {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	server := &p2p.Server{
		Config: p2p.Config{
			PrivateKey:  key,
			ListenAddr:  "127.0.0.1:0",
			NoDial:      true,
			NoDiscovery: true,
			MaxPeers:    1,
			Protocols: []p2p.Protocol{{
				Name:    ethproto.ProtocolName,
				Version: ethproto.ETH69,
				Length:  18,
				Run: func(peer *p2p.Peer, rw p2p.MsgReadWriter) error {
					return runFakeETH69Peer(fixture, rw)
				},
			}},
		},
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)

	return &fakeETH69Server{server: server}
}

func serveGetBlockHeaders(fixture *prunedSidechainP2PFixture, rw p2p.MsgReadWriter, msg p2p.Msg) error {
	var req ethproto.GetBlockHeadersPacket
	if err := msg.Decode(&req); err != nil {
		msg.Discard()
		return err
	}
	msg.Discard()
	return p2p.Send(rw, ethproto.BlockHeadersMsg, &ethproto.BlockHeadersPacket{
		RequestId:           req.RequestId,
		BlockHeadersRequest: fixture.remote.headers(req.GetBlockHeadersRequest),
	})
}

func serveGetBlockBodies(fixture *prunedSidechainP2PFixture, rw p2p.MsgReadWriter, msg p2p.Msg) error {
	var req ethproto.GetBlockBodiesPacket
	if err := msg.Decode(&req); err != nil {
		msg.Discard()
		return err
	}
	msg.Discard()
	return p2p.Send(rw, ethproto.BlockBodiesMsg, &ethproto.BlockBodiesPacket{
		RequestId:           req.RequestId,
		BlockBodiesResponse: fixture.remote.bodies(req.GetBlockBodiesRequest),
	})
}

func runFakeETH69Peer(fixture *prunedSidechainP2PFixture, rw p2p.MsgReadWriter) error {
	status := &ethproto.StatusPacket69{
		ProtocolVersion: ethproto.ETH69,
		NetworkID:       fixture.genesis.Config.ChainID.Uint64(),
		TD:              fixture.td,
		Genesis:         fixture.chain.Genesis().Hash(),
		ForkID:          forkid.NewID(fixture.chain.Config(), fixture.chain.Genesis(), fixture.head.NumberU64(), fixture.head.Time()),
		EarliestBlock:   0,
		LatestBlock:     fixture.head.NumberU64(),
		LatestBlockHash: fixture.head.Hash(),
	}
	if err := fakeETH69Handshake(rw, status); err != nil {
		return err
	}
	for {
		msg, err := rw.ReadMsg()
		if err != nil {
			return err
		}
		switch msg.Code {
		case ethproto.GetBlockHeadersMsg:
			if err := serveGetBlockHeaders(fixture, rw, msg); err != nil {
				return err
			}
		case ethproto.GetBlockBodiesMsg:
			if err := serveGetBlockBodies(fixture, rw, msg); err != nil {
				return err
			}
		default:
			msg.Discard()
		}
	}
}

func fakeETH69Handshake(rw p2p.MsgReadWriter, status *ethproto.StatusPacket69) error {
	errc := make(chan error, 2)
	go func() {
		errc <- p2p.Send(rw, ethproto.StatusMsg, status)
	}()
	go func() {
		msg, err := rw.ReadMsg()
		if err != nil {
			errc <- err
			return
		}
		defer msg.Discard()
		if msg.Code != ethproto.StatusMsg {
			errc <- fmt.Errorf("unexpected status code %d", msg.Code)
			return
		}
		var remote ethproto.StatusPacket69
		errc <- msg.Decode(&remote)
	}()

	var first error
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil && first == nil {
			first = err
		}
	}
	return first
}

type fakeEthChain struct {
	headersByHash   map[common.Hash]*types.Header
	headersByNumber map[uint64]*types.Header
	blocksByHash    map[common.Hash]*types.Block
}

func newFakeEthChain(genesis *types.Block, canonicalPrefix []*types.Block, fork []*types.Block) *fakeEthChain {
	chain := &fakeEthChain{
		headersByHash:   make(map[common.Hash]*types.Header),
		headersByNumber: make(map[uint64]*types.Header),
		blocksByHash:    make(map[common.Hash]*types.Block),
	}
	chain.addBlock(genesis)
	for _, block := range canonicalPrefix {
		chain.addBlock(block)
	}
	for _, block := range fork {
		chain.addBlock(block)
	}
	return chain
}

func (c *fakeEthChain) addBlock(block *types.Block) {
	header := block.Header()
	c.headersByHash[block.Hash()] = header
	c.headersByNumber[block.NumberU64()] = header
	c.blocksByHash[block.Hash()] = block
}

func (c *fakeEthChain) headers(req *ethproto.GetBlockHeadersRequest) ethproto.BlockHeadersRequest {
	if req == nil || req.Amount == 0 {
		return nil
	}
	var number uint64
	if req.Origin.Hash != (common.Hash{}) {
		header := c.headersByHash[req.Origin.Hash]
		if header == nil {
			return nil
		}
		number = header.Number.Uint64()
	} else {
		number = req.Origin.Number
	}

	headers := make([]*types.Header, 0, req.Amount)
	step := req.Skip + 1
	for uint64(len(headers)) < req.Amount {
		header := c.headersByNumber[number]
		if header == nil {
			break
		}
		headers = append(headers, header)
		if req.Reverse {
			if number < step {
				break
			}
			number -= step
			continue
		}
		number += step
	}
	return headers
}

func (c *fakeEthChain) bodies(hashes ethproto.GetBlockBodiesRequest) ethproto.BlockBodiesResponse {
	bodies := make(ethproto.BlockBodiesResponse, 0, len(hashes))
	for _, hash := range hashes {
		block := c.blocksByHash[hash]
		if block == nil {
			continue
		}
		bodies = append(bodies, &ethproto.BlockBody{
			Transactions: block.Transactions(),
			Uncles:       block.Uncles(),
			Withdrawals:  block.Withdrawals(),
		})
	}
	return bodies
}

func waitForEthPeer(t *testing.T, handler *handler, id string) {
	t.Helper()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for eth peer %s", id)
		case <-ticker.C:
			if handler.peers.peer(id) != nil {
				return
			}
		}
	}
}
