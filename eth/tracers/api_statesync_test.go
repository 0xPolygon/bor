package tracers

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

var (
	key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	address = crypto.PubkeyToAddress(key.PublicKey)

	// State receiver address from BorConfig — must match what the tracer reads
	stateReceiverAddr = common.HexToAddress("0x0000000000000000000000000000000000001001")

	// Target contract that the state receiver forwards calls to.
	// Bytecode: PUSH1(0) PUSH1(0) LOG0 STOP — emits an empty LOG0 on any call.
	targetAddr = common.HexToAddress("0x0000000000000000000000000000000000002000")
	targetCode = []byte{0x60, 0x00, 0x60, 0x00, 0xa0, 0x00}

	// The state receiver contract bytecode which can be used for processing state sync events. It
	// forwards calls via: call(txGas, receiver, 0, add(data, 0x20), mload(data), 0, 0) to the
	// child chain contract.
	stateReceiverCode = common.FromHex("0x608060405234801561001057600080fd5b50600436106100415760003560e01c806319494a17146100465780633434735f146100e15780635407ca671461012b575b600080fd5b6100c76004803603604081101561005c57600080fd5b81019080803590602001909291908035906020019064010000000081111561008357600080fd5b82018360208201111561009557600080fd5b803590602001918460018302840111640100000000831117156100b757600080fd5b9091929391929390505050610149565b604051808215151515815260200191505060405180910390f35b6100e961047a565b604051808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200191505060405180910390f35b610133610492565b6040518082815260200191505060405180910390f35b600073fffffffffffffffffffffffffffffffffffffffe73ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614610200576040517f08c379a00000000000000000000000000000000000000000000000000000000081526004018080602001828103825260128152602001807f4e6f742053797374656d2041646465737321000000000000000000000000000081525060200191505060405180910390fd5b606061025761025285858080601f016020809104026020016040519081016040528093929190818152602001838380828437600081840152601f19601f82011690508083019250505050505050610498565b6104c6565b905060006102788260008151811061026b57fe5b60200260200101516105a3565b905080600160005401146102f4576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040180806020018281038252601b8152602001807f537461746549647320617265206e6f742073657175656e7469616c000000000081525060200191505060405180910390fd5b600080815480929190600101919050555060006103248360018151811061031757fe5b6020026020010151610614565b905060606103458460028151811061033857fe5b6020026020010151610637565b9050610350826106c3565b1561046f576000624c4b409050606084836040516024018083815260200180602001828103825283818151815260200191508051906020019080838360005b838110156103aa57808201518184015260208101905061038f565b50505050905090810190601f1680156103d75780820380516001836020036101000a031916815260200191505b5093505050506040516020818303038152906040527f26c53bea000000000000000000000000000000000000000000000000000000007bffffffffffffffffffffffffffffffffffffffffffffffffffffffff19166020820180517bffffffffffffffffffffffffffffffffffffffffffffffffffffffff8381831617835250505050905060008082516020840160008887f1965050505b505050509392505050565b73fffffffffffffffffffffffffffffffffffffffe81565b60005481565b6104a0610943565b600060208301905060405180604001604052808451815260200182815250915050919050565b60606104d1826106dc565b6104da57600080fd5b60006104e58361072a565b905060608160405190808252806020026020018201604052801561052357816020015b61051061095d565b8152602001906001900390816105085790505b5090506000610535856020015161079b565b8560200151019050600080600090505b848110156105965761055683610824565b915060405180604001604052808381526020018481525084828151811061057957fe5b602002602001018190525081830192508080600101915050610545565b5082945050505050919050565b60008082600001511180156105bd57506021826000015111155b6105c657600080fd5b60006105d5836020015161079b565b9050600081846000015103905060008083866020015101905080519150602083101561060857826020036101000a820491505b81945050505050919050565b6000601582600001511461062757600080fd5b610630826105a3565b9050919050565b6060600082600001511161064a57600080fd5b6000610659836020015161079b565b905060008184600001510390506060816040519080825280601f01601f19166020018201604052801561069b5781602001600182028038833980820191505090505b50905060008160200190506106b78487602001510182856108dc565b81945050505050919050565b600080823b905060008163ffffffff1611915050919050565b600080826000015114156106f35760009050610725565b60008083602001519050805160001a915060c060ff168260ff16101561071e57600092505050610725565b6001925050505b919050565b600080826000015114156107415760009050610796565b60008090506000610755846020015161079b565b84602001510190506000846000015185602001510190505b8082101561078f5761077e82610824565b82019150828060010193505061076d565b8293505050505b919050565b600080825160001a9050608060ff168110156107bb57600091505061081f565b60b860ff168110806107e0575060c060ff1681101580156107df575060f860ff1681105b5b156107ef57600191505061081f565b60c060ff1681101561080f5760018060b80360ff1682030191505061081f565b60018060f80360ff168203019150505b919050565b6000806000835160001a9050608060ff1681101561084557600191506108d2565b60b860ff16811015610862576001608060ff1682030191506108d1565b60c060ff168110156108925760b78103600185019450806020036101000a855104600182018101935050506108d0565b60f860ff168110156108af57600160c060ff1682030191506108cf565b60f78103600185019450806020036101000a855104600182018101935050505b5b5b5b8192505050919050565b60008114156108ea5761093e565b5b602060ff16811061091a5782518252602060ff1683019250602060ff1682019150602060ff16810390506108eb565b6000600182602060ff16036101000a03905080198451168184511681811785525050505b505050565b604051806040016040528060008152602001600081525090565b60405180604001604052806000815260200160008152509056fea265627a7a7231582083fbdacb76f32b4112d0f7db9a596937925824798a0026ba0232322390b5263764736f6c634300050b0032")
)

// borTestBackend extends testBackend with Bor-specific configuration for testing
// state sync transaction handling across the Madhugiri hardfork.
type borTestBackend struct {
	testBackend

	modifiedBlocks map[uint64]bool
	modifiedHashes map[common.Hash]uint64
}

// BlockByNumber overrides testBackend to read modified blocks from DB.
func (b *borTestBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	if number == rpc.PendingBlockNumber || number == rpc.LatestBlockNumber {
		return b.chain.GetBlockByNumber(b.chain.CurrentBlock().Number.Uint64()), nil
	}

	blockNum := uint64(number)
	if b.modifiedBlocks != nil && b.modifiedBlocks[blockNum] {
		header := b.chain.GetHeaderByNumber(blockNum)
		if header == nil {
			return nil, nil
		}
		body := rawdb.ReadBody(b.chaindb, header.Hash(), blockNum)
		if body == nil {
			return nil, nil
		}
		return types.NewBlockWithHeader(header).WithBody(*body), nil
	}

	return b.chain.GetBlockByNumber(blockNum), nil
}

// BlockByHash overrides testBackend to read modified blocks from DB.
func (b *borTestBackend) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	if b.modifiedHashes != nil {
		if blockNum, ok := b.modifiedHashes[hash]; ok {
			header := b.chain.GetHeaderByNumber(blockNum)
			if header == nil {
				return nil, nil
			}
			body := rawdb.ReadBody(b.chaindb, header.Hash(), blockNum)
			if body == nil {
				return nil, nil
			}
			return types.NewBlockWithHeader(header).WithBody(*body), nil
		}
	}

	return b.chain.GetBlockByHash(hash), nil
}

// newBorChainConfig creates a chain config suitable for Bor state sync testing.
func newBorChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:             big.NewInt(137),
		HomesteadBlock:      big.NewInt(0),
		DAOForkBlock:        big.NewInt(0),
		DAOForkSupport:      true,
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		MuirGlacierBlock:    big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			JaipurBlock:       big.NewInt(0),
			DelhiBlock:        big.NewInt(0),
			IndoreBlock:       big.NewInt(0),
			AhmedabadBlock:    big.NewInt(0),
			BhilaiBlock:       big.NewInt(0),
			RioBlock:          big.NewInt(0),
			MadhugiriBlock:    big.NewInt(0),
			MadhugiriProBlock: big.NewInt(0),
			DandeliBlock:      big.NewInt(0),
			Period: map[string]uint64{
				"0": 2,
			},
			ProducerDelay: map[string]uint64{
				"0": 2,
			},
			Sprint: map[string]uint64{
				"0": 16,
			},
			BackupMultiplier: map[string]uint64{
				"0": 2,
			},
			ValidatorContract:     "0x0000000000000000000000000000000000001000",
			StateReceiverContract: "0x0000000000000000000000000000000000001001",
			BurntContract: map[string]string{
				"0": "0x000000000000000000000000000000000000dead",
			},
			Coinbase: map[string]string{
				"0": "0x0000000000000000000000000000000000000000",
			},
		},
	}
}

// newBorTestBackend creates a test backend that:
// 1. Inserts blocks without Bor validation (to avoid state-sync processing issues)
// 2. Exposes a Bor chain config to the tracer API (to test Madhugiri logic)
// 3. Allows manual injection of state-sync txs into block bodies after insertion
func newBorTestBackend(t *testing.T, n int, gspec *core.Genesis, generator func(i int, b *core.BlockGen)) *borTestBackend {
	borCfg := newBorChainConfig()
	gspec.Config = borCfg

	backend := &borTestBackend{
		testBackend: testBackend{
			// Use Bor config for API queries (this is what the tracer API sees)
			chainConfig: borCfg,
			engine:      ethash.NewFaker(),
			chaindb:     rawdb.NewMemoryDatabase(),
		},
		modifiedBlocks: make(map[uint64]bool),
		modifiedHashes: make(map[common.Hash]uint64),
	}

	// Generate blocks with insertion config (no Bor)
	_, blocks, _ := core.GenerateChainWithGenesis(gspec, backend.engine, n, generator)

	// Import the canonical chain
	options := &core.BlockChainConfig{
		TrieCleanLimit: 256,
		TrieDirtyLimit: 256,
		TrieTimeLimit:  5 * time.Minute,
		SnapshotLimit:  0,
		ArchiveMode:    true,
	}

	// Create chain with insertion config (no Bor validation)
	chain, err := core.NewBlockChain(backend.chaindb, gspec, backend.engine, options)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}

	if len(blocks) > 0 {
		if n, err := chain.InsertChain(blocks, false); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", n, err)
		}
	}

	backend.chain = chain
	return backend
}

// injectStateSyncTx appends a state-sync transaction to the specified block's body.
// This simulates post-Madhugiri blocks that have state-sync txs in their body.
func (b *borTestBackend) injectStateSyncTx(blockNum uint64, stateSyncTx *types.Transaction) error {
	block := b.chain.GetBlockByNumber(blockNum)
	if block == nil {
		return nil
	}

	// Read existing body and append state-sync tx
	existingBody := block.Body()
	newTxs := make([]*types.Transaction, len(existingBody.Transactions)+1)
	copy(newTxs, existingBody.Transactions)
	newTxs[len(newTxs)-1] = stateSyncTx

	newBody := &types.Body{
		Transactions: newTxs,
		Uncles:       existingBody.Uncles,
		Withdrawals:  existingBody.Withdrawals,
	}

	// Write modified body back to database
	rawdb.WriteBody(b.chaindb, block.Hash(), blockNum, newBody)

	// Track this block as modified so BlockByNumber/BlockByHash reads from DB
	b.modifiedBlocks[blockNum] = true
	b.modifiedHashes[block.Hash()] = blockNum
	return nil
}

// newStateSyncTestSetup creates a common test setup for state-sync tracing tests.
// It returns the backend, api, stateSyncTx, and the block number where the state-sync tx was injected.
func newStateSyncTestSetup(t *testing.T, n int) (*borTestBackend, *API, uint64) {
	t.Helper()

	gspec := &core.Genesis{
		Alloc: types.GenesisAlloc{
			address:           {Balance: big.NewInt(params.Ether)},
			stateReceiverAddr: {Code: stateReceiverCode, Balance: big.NewInt(0)},
			targetAddr:        {Code: targetCode, Balance: big.NewInt(0)},
		},
	}

	backend := newBorTestBackend(t, n, gspec, func(i int, b *core.BlockGen) {
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i),
			To:       &address,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: new(big.Int).Mul(b.BaseFee(), big.NewInt(2)),
		}), b.Signer(), key)
		b.AddTx(tx)
	})

	stateSyncTx := types.NewTx(&types.StateSyncTx{
		StateSyncData: []*types.StateSyncData{
			{
				ID:       1,
				Contract: targetAddr,
				Data:     []byte("event-one"),
				TxHash:   common.HexToHash("0xaaa1"),
			},
			{
				ID:       2,
				Contract: targetAddr,
				Data:     []byte("event-two"),
				TxHash:   common.HexToHash("0xaaa2"),
			},
		},
	})

	stateSyncBlock := uint64(2)
	err := backend.injectStateSyncTx(stateSyncBlock, stateSyncTx)
	require.NoError(t, err, "failed to inject state-sync tx")

	api := NewAPI(backend)
	return backend, api, stateSyncBlock
}

// TestTraceBlockByNumber_WithStateSyncTx tests end-to-end state-sync tracing using the actual
// StateReceiver contract bytecode and mirrors what happens in actual networks. During the test
// we do the following things:
//
//  1. Deploy StateReceiver at 0x1001 with mainnet bytecode (which has `stateReceive` method).
//  2. A simple "target" contract is deployed at a separate address — it just emits LOG0 on any call.
//  3. A StateSyncTx carries bridge events whose Contract field points to the target is injected.
//  4. During tracing, the bridge events are executed inside EVM which calls the StateReceiver
//     contract, which further calls the target contract and generates expected trace.
//  5. We later verify if the trace is correctly generated or not.
func TestTraceBlockByNumber_WithStateSyncTx(t *testing.T) {
	t.Parallel()

	backend, api, stateSyncBlock := newStateSyncTestSetup(t, 3)
	defer backend.chain.Stop()

	// Ensure block body contains transactions
	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(stateSyncBlock))
	require.NotNil(t, block, "block %d not found", stateSyncBlock)
	transactions := block.Transactions()
	require.Equal(t, 2, len(transactions), "expected %d transactions in block body, got %d", 2, len(transactions))

	// Trace block by number
	results, err := api.TraceBlockByNumber(context.Background(), rpc.BlockNumber(stateSyncBlock), nil)
	require.NoError(t, err, "TraceBlockByNumber failed: %v", err)
	require.NotNil(t, results, "TraceBlockByNumber returned nil traces")

	// Validate if the trace is correct or not
	require.Equal(t, len(transactions), len(results), "expected %d trace results, got: %d", len(transactions), len(results))
	for i, result := range results {
		require.Equal(t, "", result.Error, "expected nil error in trace result %d, got: %s", i, result.Error)
		require.Equal(t, transactions[i].Hash(), result.TxHash, "trace result %d tx hash mismatch: got %s, want %s", i, result.TxHash.Hex(), transactions[i].Hash().Hex())
	}

	// Validate the state-sync trace specifically
	raw, ok := results[1].Result.(json.RawMessage)
	require.True(t, ok, "expected json.RawMessage for state-sync trace result, got %T", results[1].Result)

	var execResult struct {
		Gas         uint64            `json:"gas"`
		Failed      bool              `json:"failed"`
		StructLogs  []json.RawMessage `json:"structLogs"`
		ReturnValue string            `json:"returnValue"`
	}
	err = json.Unmarshal(raw, &execResult)
	require.NoError(t, err, "failed to unmarshal state-sync trace: %v", err)
	require.Greater(t, len(execResult.StructLogs), 0, "state-sync trace has no struct logs — EVM code was not executed")

	// The target contract emits LOG0 for each onStateReceive call.
	// With 2 bridge events, we expect 2 LOG0 from the target contract.
	var log0Count int
	for _, entry := range execResult.StructLogs {
		var op struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(entry, &op); err == nil && op.Op == "LOG0" {
			log0Count++
		}
	}
	require.Equal(t, 2, log0Count, "expected 2 LOG0 opcodes (one per bridge event forwarded to target), got %d", log0Count)
	t.Logf("state-sync trace: %d struct logs, %d LOG0 opcodes, gas used: %d", len(execResult.StructLogs), log0Count, execResult.Gas)
}

// TestTraceBlockByHash_WithStateSyncTx tests end-to-end state-sync tracing using the actual
// StateReceiver contract bytecode and mirrors what happens in actual networks. Follows same
// steps as trace by block.
func TestTraceBlockByHash_WithStateSyncTx(t *testing.T) {
	t.Parallel()

	backend, api, stateSyncBlock := newStateSyncTestSetup(t, 3)
	defer backend.chain.Stop()

	// Ensure block body contains transactions
	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(stateSyncBlock))
	require.NotNil(t, block, "block %d not found", stateSyncBlock)
	transactions := block.Transactions()
	require.Equal(t, 2, len(transactions), "expected %d transactions in block body, got %d", 2, len(transactions))

	// Trace block by hash
	results, err := api.TraceBlockByHash(context.Background(), block.Hash(), nil)
	require.NoError(t, err, "TraceBlockByHash failed: %v", err)
	require.NotNil(t, results, "TraceBlockByHash returned nil traces")

	// Validate if the trace is correct or not
	require.Equal(t, len(transactions), len(results), "expected %d trace results, got: %d", len(transactions), len(results))
	for i, result := range results {
		require.Equal(t, "", result.Error, "expected nil error in trace result %d, got: %s", i, result.Error)
		require.Equal(t, transactions[i].Hash(), result.TxHash, "trace result %d tx hash mismatch: got %s, want %s", i, result.TxHash.Hex(), transactions[i].Hash().Hex())
	}

	// Validate the state-sync trace specifically
	raw, ok := results[1].Result.(json.RawMessage)
	require.True(t, ok, "expected json.RawMessage for state-sync trace result, got %T", results[1].Result)

	var execResult struct {
		Gas         uint64            `json:"gas"`
		Failed      bool              `json:"failed"`
		StructLogs  []json.RawMessage `json:"structLogs"`
		ReturnValue string            `json:"returnValue"`
	}
	err = json.Unmarshal(raw, &execResult)
	require.NoError(t, err, "failed to unmarshal state-sync trace: %v", err)
	require.Greater(t, len(execResult.StructLogs), 0, "state-sync trace has no struct logs — EVM code was not executed")

	// The target contract emits LOG0 for each onStateReceive call.
	// With 2 bridge events, we expect 2 LOG0 from the target contract.
	var log0Count int
	for _, entry := range execResult.StructLogs {
		var op struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(entry, &op); err == nil && op.Op == "LOG0" {
			log0Count++
		}
	}
	require.Equal(t, 2, log0Count, "expected 2 LOG0 opcodes (one per bridge event forwarded to target), got %d", log0Count)
	t.Logf("state-sync trace: %d struct logs, %d LOG0 opcodes, gas used: %d", len(execResult.StructLogs), log0Count, execResult.Gas)
}

// TestTraceChain_WithStateSyncTx tests end-to-end state-sync tracing using the actual
// StateReceiver contract bytecode and mirrors what happens in actual networks. It generates
// traces of range of blocks.
func TestTraceChain_WithStateSyncTx(t *testing.T) {
	t.Parallel()

	backend, api, stateSyncBlock := newStateSyncTestSetup(t, 3)
	defer backend.chain.Stop()

	// Get the from and to blocks
	from, err := backend.BlockByNumber(context.Background(), rpc.BlockNumber(0))
	require.NoError(t, err, "failed to get block: %d, err: %v", 0, err)
	to, err := backend.BlockByNumber(context.Background(), rpc.BlockNumber(3))
	require.NoError(t, err, "failed to get block: %d, err: %v", 3, err)

	// Trace full chain (from, to] (i.e. [0, 3])
	results := api.traceChain(from, to, nil, nil)
	require.NotNil(t, results, "TraceChain returned nil traces")

	// Validate if the trace is correct or not
	for res := range results {
		traces := res.Traces
		block, err := backend.BlockByNumber(context.Background(), rpc.BlockNumber(uint64(res.Block)))
		require.NoError(t, err, "failed to get block: %d, err: %v", res.Block, err)
		transactions := block.Transactions()
		require.Equal(t, len(transactions), len(traces), "expected %d trace results, got: %d", len(transactions), len(traces))
		for i, result := range traces {
			require.Equal(t, "", result.Error, "expected nil error in trace result %d of block: %d, got: %s", i, block.NumberU64(), result.Error)
			require.Equal(t, transactions[i].Hash(), result.TxHash, "trace result %d tx hash mismatch of block: %d: got %s, want %s", i, block.NumberU64(), result.TxHash.Hex(), transactions[i].Hash().Hex())
		}
		if res.Block == hexutil.Uint64(stateSyncBlock) {
			// Validate the state-sync trace specifically
			raw, ok := traces[1].Result.(json.RawMessage)
			require.True(t, ok, "expected json.RawMessage for state-sync trace result, got %T", traces[1].Result)

			var execResult struct {
				Gas         uint64            `json:"gas"`
				Failed      bool              `json:"failed"`
				StructLogs  []json.RawMessage `json:"structLogs"`
				ReturnValue string            `json:"returnValue"`
			}
			err = json.Unmarshal(raw, &execResult)
			require.NoError(t, err, "failed to unmarshal state-sync trace: %v", err)
			require.Greater(t, len(execResult.StructLogs), 0, "state-sync trace has no struct logs — EVM code was not executed")

			// The target contract emits LOG0 for each onStateReceive call.
			// With 2 bridge events, we expect 2 LOG0 from the target contract.
			var log0Count int
			for _, entry := range execResult.StructLogs {
				var op struct {
					Op string `json:"op"`
				}
				if err := json.Unmarshal(entry, &op); err == nil && op.Op == "LOG0" {
					log0Count++
				}
			}
			require.Equal(t, 2, log0Count, "expected 2 LOG0 opcodes (one per bridge event forwarded to target), got %d", log0Count)
		}
	}
}

// TestIntermediateRoots_WithStateSyncTx tests end-to-end state-sync tracing using the actual
// StateReceiver contract bytecode and mirrors what happens in actual networks. Follows same
// steps as trace by block.
func TestIntermediateRoots_WithStateSyncTx(t *testing.T) {
	t.Parallel()

	backend, api, stateSyncBlock := newStateSyncTestSetup(t, 3)
	defer backend.chain.Stop()

	// Ensure block body contains transactions
	block, _ := backend.BlockByNumber(context.Background(), rpc.BlockNumber(stateSyncBlock))
	require.NotNil(t, block, "block %d not found", stateSyncBlock)
	transactions := block.Transactions()
	require.Equal(t, 2, len(transactions), "expected %d transactions in block body, got %d", 2, len(transactions))

	// Trace block by hash
	results, err := api.IntermediateRoots(context.Background(), block.Hash(), nil)
	require.NoError(t, err, "IntermediateRoots failed: %v", err)
	require.NotNil(t, results, "IntermediateRoots returned nil traces")

	// Validate that correct state roots are returned
	expectedStateRoots := []common.Hash{
		common.HexToHash("0x23eda0b1dbe747a8daedaf94b811a393de400047812394476dac190a5e9a8fd4"),
		common.HexToHash("0x1b5bcf33b31f2d38b498594a348bc176b9e05b46cba3ed3701ba739c012bc757"),
	}
	require.Equal(t, 2, len(results), "expected 2 intermediate roots, got: %d", len(results))
	for i, result := range results {
		require.Equal(t, expectedStateRoots[i], result, "state root mismatch, index: %d, expected: %v, got: %v", i, expectedStateRoots[i].Hex(), result.Hex())
	}
}
