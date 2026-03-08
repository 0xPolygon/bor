package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

type mockRpcServer struct {
	server *httptest.Server
	mu     sync.RWMutex

	handleBlockNumber   func(w http.ResponseWriter, id int)
	handleSendPreconfTx func(w http.ResponseWriter, id int, params json.RawMessage)
	handleSendPrivateTx func(w http.ResponseWriter, id int, params json.RawMessage)
	handleTxStatus      func(w http.ResponseWriter, id int, params json.RawMessage)
	sendError           func(w http.ResponseWriter, id int, code int, message string)
}

func (m *mockRpcServer) setHandleBlockNumber(h func(w http.ResponseWriter, id int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handleBlockNumber = h
}

func (m *mockRpcServer) setHandleSendPreconfTx(h func(w http.ResponseWriter, id int, params json.RawMessage)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handleSendPreconfTx = h
}

func (m *mockRpcServer) setHandleSendPrivateTx(h func(w http.ResponseWriter, id int, params json.RawMessage)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handleSendPrivateTx = h
}

func (m *mockRpcServer) setHandleTxStatus(h func(w http.ResponseWriter, id int, params json.RawMessage)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handleTxStatus = h
}

func newMockRpcServer() *mockRpcServer {
	m := &mockRpcServer{
		handleBlockNumber:   defaultHandleBlockNumber,
		handleSendPreconfTx: defaultHandleSendPreconfTx,
		handleSendPrivateTx: defaultHandleSendPrivateTx,
		handleTxStatus:      defaultHandleTxStatus,
		sendError:           defaultSendError,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleRequests)
	m.server = httptest.NewServer(mux)

	return m
}

func (m *mockRpcServer) handleRequests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Snapshot handlers under read lock to avoid races with test goroutines
	m.mu.RLock()
	handleBlockNumber := m.handleBlockNumber
	handleSendPrivateTx := m.handleSendPrivateTx
	handleSendPreconfTx := m.handleSendPreconfTx
	handleTxStatus := m.handleTxStatus
	sendError := m.sendError
	m.mu.RUnlock()

	// Handle different RPC methods
	switch req.Method {
	case "eth_blockNumber":
		handleBlockNumber(w, req.ID)
	case "eth_sendRawTransactionPrivate":
		handleSendPrivateTx(w, req.ID, req.Params)
	case "eth_sendRawTransactionForPreconf":
		handleSendPreconfTx(w, req.ID, req.Params)
	case "txpool_txStatus":
		handleTxStatus(w, req.ID, req.Params)
	default:
		sendError(w, req.ID, -32601, "method not found")
	}
}

func (m *mockRpcServer) close() {
	m.server.Close()
}

func defaultHandleBlockNumber(w http.ResponseWriter, id int) {
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  "0x1",
	}
	json.NewEncoder(w).Encode(response)
}

func defaultHandleSendPreconfTx(w http.ResponseWriter, id int, params json.RawMessage) {
	// Extract the raw transaction from params
	var rawTxParams []string
	json.Unmarshal(params, &rawTxParams)
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(hexutil.MustDecode(rawTxParams[0])); err != nil {
		defaultSendError(w, id, -32602, err.Error())
		return
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"hash":         common.HexToHash("0x"),
			"preconfirmed": true,
		},
	}
	json.NewEncoder(w).Encode(response)
}

func handleSendPreconfTxWithRejection(w http.ResponseWriter, id int, params json.RawMessage) {
	// Extract the raw transaction from params
	var rawTxParams []string
	json.Unmarshal(params, &rawTxParams)
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(hexutil.MustDecode(rawTxParams[0])); err != nil {
		defaultSendError(w, id, -32602, err.Error())
		return
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"hash":         common.HexToHash("0x"),
			"preconfirmed": false,
		},
	}
	json.NewEncoder(w).Encode(response)
}

func defaultHandleSendPrivateTx(w http.ResponseWriter, id int, params json.RawMessage) {
	// Extract the raw transaction from params
	var rawTxParams []string
	json.Unmarshal(params, &rawTxParams)
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(hexutil.MustDecode(rawTxParams[0])); err != nil {
		defaultSendError(w, id, -32602, err.Error())
		return
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  tx.Hash(),
	}
	json.NewEncoder(w).Encode(response)
}

func defaultHandleTxStatus(w http.ResponseWriter, id int, params json.RawMessage) {
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  txpool.TxStatusPending,
	}
	json.NewEncoder(w).Encode(response)
}

func makeTxStatusHandler(statusMap map[common.Hash]txpool.TxStatus) func(w http.ResponseWriter, id int, params json.RawMessage) {
	return func(w http.ResponseWriter, id int, params json.RawMessage) {
		var inputs []common.Hash
		json.Unmarshal(params, &inputs)
		hash := inputs[0]

		status := txpool.TxStatusUnknown
		if s, ok := statusMap[hash]; ok {
			status = s
		}

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  status,
		}
		json.NewEncoder(w).Encode(response)
	}
}

func defaultSendError(w http.ResponseWriter, id int, code int, message string) {
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	json.NewEncoder(w).Encode(response)
}

func TestMockRpc(t *testing.T) {
	server := newMockRpcServer()
	url := server.server.URL

	client, err := rpc.Dial(url)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	var blockNumber string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = client.CallContext(ctx, &blockNumber, "eth_blockNumber")
	cancel()
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	number, _ := hexutil.DecodeUint64(blockNumber)
	require.Equal(t, uint64(1), number, "expected default block number to be 1")
}

func TestNewMulticlient(t *testing.T) {
	t.Parallel()

	// Initialize 4 healthy servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 4)
	var urls []string = make([]string, 4)
	for i := 0; i < 4; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}

	t.Run("initialise multiclient with empty set of urls", func(t *testing.T) {
		mc := newMultiClient([]string{})
		require.Nil(t, mc, "expected a nil multiclient")
	})

	t.Run("initialise multiclient with all healthy servers", func(t *testing.T) {
		mc := newMultiClient(urls)
		require.NotNil(t, mc, "expected non-nil multiclient given healthy urls")
		require.Equal(t, len(urls), len(mc.clients), "expected all clients given healthy urls")
		mc.close()
	})

	t.Run("initialise multiclient with few healthy servers", func(t *testing.T) {
		// Close one of the server to simulate failure
		rpcServers[0].close()
		mc := newMultiClient(urls)
		require.NotNil(t, mc, "expected non-nil multiclient given some healthy urls")
		require.Equal(t, 3, len(mc.clients), "expected 2 clients given 2 healthy urls")
		mc.close()
	})

	t.Run("initialise multiclient with failing call in rpc server", func(t *testing.T) {
		// Mock the `eth_blockNumber` call in one of the servers to send
		// an error instead of correct response simulating failure.
		rpcServers[1].setHandleBlockNumber(func(w http.ResponseWriter, id int) {
			defaultSendError(w, id, -32601, "internal server error")
		})
		mc := newMultiClient(urls)
		require.NotNil(t, mc, "expected non-nil multiclient given some healthy urls")
		require.Equal(t, 2, len(mc.clients), "expected 2 clients given 2 healthy urls")
		mc.close()
	})

	t.Run("initialise multiclient with timeout in rpc server", func(t *testing.T) {
		// Mock the `eth_blockNumber` call in one of the servers to sleep
		// for more than `rpcTimeout` duration simulating failure.
		rpcServers[1].setHandleBlockNumber(func(w http.ResponseWriter, id int) {
			time.Sleep(rpcTimeout + 100*time.Millisecond)
			defaultHandleBlockNumber(w, id)
		})
		mc := newMultiClient(urls)
		require.NotNil(t, mc, "expected non-nil multiclient given some healthy urls")
		require.Equal(t, 2, len(mc.clients), "expected 2 clients given 2 healthy urls")
		mc.close()
	})

	t.Run("initialise multiclient with all failed servers", func(t *testing.T) {
		rpcServers[1].close()
		rpcServers[2].close()
		rpcServers[3].close()
		mc := newMultiClient(urls)
		require.Nil(t, mc, "expected nil multiclient given all failing urls")
	})
}

func TestSubmitPreconfTx(t *testing.T) {
	t.Parallel()

	// Create a dummy tx
	tx1 := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
	rawTx, err := tx1.MarshalBinary()
	require.NoError(t, err, "error in marshalling dummy tx")

	// Initialize 4 healthy servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 4)
	var urls []string = make([]string, 4)
	for i := 0; i < 4; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}

	t.Run("submitPreconfTx with healthy BPs", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.NoError(t, err, "expected no error in submitting preconf tx")
		require.True(t, res, "expected preconf to be offered by all BPs")
	})

	t.Run("submitPreconfTx with invalid tx", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		invalidRawTx := []byte{0x01, 0x02, 0x03}
		res, err := mc.submitPreconfTx(invalidRawTx)
		require.Error(t, err, "expected error in submitting invalid preconf tx")
		require.False(t, res, "expected preconf to not be offered for invalid tx")
	})

	t.Run("submitPreconfTx with no preconfirmation", func(t *testing.T) {
		// Mock one of the server to reject preconfirmation
		rpcServers[0].setHandleSendPreconfTx(handleSendPreconfTxWithRejection)

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.NoError(t, err, "expected no error in submitting preconf tx")
		require.False(t, res, "expected preconf to be not offered by all BPs")
	})

	t.Run("submitPreconfTx with error in rpc server", func(t *testing.T) {
		// Mock one of the servers to return an error
		rpcServers[0].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.Error(t, err, "expected error in submitting preconf tx")
		require.ErrorContains(t, err, "internal server error", "expected internal server error")
		require.False(t, res, "expected preconf to be not offered by all BPs")
	})

	t.Run("submitPreconfTx with timeout in rpc server", func(t *testing.T) {
		// Mock one of the servers to timeout
		rpcServers[0].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			time.Sleep(rpcTimeout + 100*time.Millisecond)
			defaultHandleSendPreconfTx(w, id, params)
		})

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.Error(t, err, "expected error in submitting preconf tx")
		require.ErrorContains(t, err, "context deadline exceeded", "expected context deadline exceeded error")
		require.False(t, res, "expected preconf to be not offered by all BPs")
	})

	t.Run("submitPreconfTx runs in parallel", func(t *testing.T) {
		// Ensure all calls take almost 2s of time but don't exceed rpcTimeout
		for i := range rpcServers {
			rpcServers[i].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				time.Sleep(rpcTimeout - 100*time.Millisecond)
				defaultHandleSendPreconfTx(w, id, params)
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		start := time.Now()
		res, err := mc.submitPreconfTx(rawTx)
		elapsed := time.Since(start)

		require.NoError(t, err, "expected no error in submitting preconf tx")
		require.True(t, res, "expected preconf to be offered by all BPs")
		require.Less(t, elapsed, 2*time.Second, "expected parallel calls to finish below timeout")
		require.Greater(t, elapsed, rpcTimeout-100*time.Millisecond, "expected calls to take at least time taken by all calls")
	})

	t.Run("submitPreconfTx with already known error from one BP", func(t *testing.T) {
		// Reset all handlers to default
		for i := range rpcServers {
			rpcServers[i].setHandleSendPreconfTx(defaultHandleSendPreconfTx)
		}

		// Mock server 0 to return "already known" error
		rpcServers[0].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32000, "already known")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.NoError(t, err, "expected no error when one BP returns already known")
		require.True(t, res, "expected preconf to be offered when all BPs accept (including already known)")
	})

	t.Run("submitPreconfTx with already known error from all BPs", func(t *testing.T) {
		// Mock all servers to return "already known" error
		for i := range rpcServers {
			rpcServers[i].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				defaultSendError(w, id, -32000, "already known")
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.NoError(t, err, "expected no error when all BPs return already known")
		require.True(t, res, "expected preconf to be offered when all BPs return already known")
	})

	t.Run("submitPreconfTx with already known and different error", func(t *testing.T) {
		// Some BPs return already known, one returns a different error, rest succeed
		rpcServers[0].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32000, "already known")
		})
		rpcServers[1].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})
		rpcServers[2].setHandleSendPreconfTx(defaultHandleSendPreconfTx)
		rpcServers[3].setHandleSendPreconfTx(defaultHandleSendPreconfTx)

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.Error(t, err, "expected error when one BP returns an error which apart from already known")
		require.ErrorContains(t, err, "internal server error", "expected internal server error")
		require.False(t, res, "expected preconf to not be offered when one BP fails with non-already-known error")
	})

	t.Run("submitPreconfTx with already known and rejection", func(t *testing.T) {
		// Some BPs return already known, one rejects preconf
		rpcServers[0].setHandleSendPreconfTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32000, "already known")
		})
		rpcServers[1].setHandleSendPreconfTx(handleSendPreconfTxWithRejection)
		rpcServers[2].setHandleSendPreconfTx(defaultHandleSendPreconfTx)
		rpcServers[3].setHandleSendPreconfTx(defaultHandleSendPreconfTx)

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.submitPreconfTx(rawTx)
		require.NoError(t, err, "expected no error")
		require.False(t, res, "expected preconf to not be offered when one BP rejects")
	})

	t.Run("submitPreconfTx with some failing servers", func(t *testing.T) {
		// Set handlers back to default
		for i := range rpcServers {
			rpcServers[i].setHandleSendPreconfTx(defaultHandleSendPreconfTx)
		}

		// Initialise multiclient with healthy servers
		mc := newMultiClient(urls)
		defer mc.close()

		// Close one of the servers to simulate failure
		rpcServers[0].close()

		// Ensure all 4 clients are still available
		require.Equal(t, len(urls), len(mc.clients), "expected all clients given healthy urls")

		res, err := mc.submitPreconfTx(rawTx)
		require.Error(t, err, "expected error in submitting preconf tx")
		require.False(t, res, "expected preconf to be not offered by all BPs")
	})

	t.Run("submitPreconfTx with all failing servers", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		// Close all servers to simulate failure
		rpcServers[1].close()
		rpcServers[2].close()
		rpcServers[3].close()

		res, err := mc.submitPreconfTx(rawTx)
		require.Error(t, err, "expected error in submitting preconf tx")
		require.False(t, res, "expected preconf to be not offered by all BPs")
	})
}

func TestSubmitPrivateTx(t *testing.T) {
	t.Parallel()

	// Create a dummy tx
	tx1 := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
	rawTx, err := tx1.MarshalBinary()
	require.NoError(t, err, "error in marshalling dummy tx")

	// Initialize 4 healthy servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 4)
	var urls []string = make([]string, 4)
	for i := 0; i < 4; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}

	t.Run("submitPrivateTx with all healthy BPs", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.NoError(t, err, "expected no error in submitting private tx to all healthy BPs")
	})

	t.Run("submitPrivateTx with invalid tx", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		invalidRawTx := []byte{0x01, 0x02, 0x03}
		err, _ := mc.submitPrivateTx(invalidRawTx, common.Hash{}, false, nil)
		require.Error(t, err, "expected error in submitting invalid private tx")
	})

	t.Run("submitPrivateTx with error in one RPC server", func(t *testing.T) {
		// Mock one of the servers to return an error
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when one BP fails")
		require.ErrorContains(t, err, "internal server error", "expected internal server error")
	})

	t.Run("submitPrivateTx with timeout in one RPC server", func(t *testing.T) {
		// Reset server 0 to default first
		rpcServers[0].setHandleSendPrivateTx(defaultHandleSendPrivateTx)

		// Mock one server to timeout
		rpcServers[1].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			time.Sleep(rpcTimeout + 100*time.Millisecond)
			defaultHandleSendPrivateTx(w, id, params)
		})

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when one BP times out")
		require.ErrorContains(t, err, "context deadline exceeded", "expected context deadline exceeded error")
	})

	t.Run("submitPrivateTx runs in parallel", func(t *testing.T) {
		// Reset all handlers and make each call take almost rpcTimeout
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				time.Sleep(rpcTimeout - 100*time.Millisecond)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		start := time.Now()
		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		elapsed := time.Since(start)

		require.NoError(t, err, "expected no error in submitting private tx")
		require.Less(t, elapsed, 2*time.Second, "expected parallel calls to finish below total timeout")
		require.Greater(t, elapsed, rpcTimeout-100*time.Millisecond, "expected calls to take at least the time of one call")
	})

	t.Run("submitPrivateTx with multiple BPs failing", func(t *testing.T) {
		// Reset handlers first
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(defaultHandleSendPrivateTx)
		}

		// Make 2 servers fail
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})
		rpcServers[1].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32602, "another error")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when multiple BPs fail")
	})

	t.Run("submitPrivateTx with all BPs failing", func(t *testing.T) {
		// Make all servers fail
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				defaultSendError(w, id, -32601, "internal server error")
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when all BPs fail")
		require.ErrorContains(t, err, "internal server error", "expected error message from failing BPs")
	})

	t.Run("submitPrivateTx with already known error from one BP", func(t *testing.T) {
		// Reset all handlers to default
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(defaultHandleSendPrivateTx)
		}

		// Mock one server to return "already known" error
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32000, "already known")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.NoError(t, err, "expected no error when one BP returns already known")
	})

	t.Run("submitPrivateTx with already known error from all BPs", func(t *testing.T) {
		// Mock all servers to return "already known" error
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				defaultSendError(w, id, -32000, "already known")
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.NoError(t, err, "expected no error when all BPs return already known")
	})

	t.Run("submitPrivateTx with already known and different error", func(t *testing.T) {
		// Some BPs return already known, one returns a different error, rest succeed
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32000, "already known")
		})
		rpcServers[1].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})
		rpcServers[2].setHandleSendPrivateTx(defaultHandleSendPrivateTx)
		rpcServers[3].setHandleSendPrivateTx(defaultHandleSendPrivateTx)

		mc := newMultiClient(urls)
		defer mc.close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when one BP returns non-already-known error")
		require.ErrorContains(t, err, "internal server error", "expected internal server error")
	})

	t.Run("submitPrivateTx with some BPs failing after initialization", func(t *testing.T) {
		// Reset all handlers to default
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(defaultHandleSendPrivateTx)
		}

		// Initialize multiclient with all healthy servers
		mc := newMultiClient(urls)
		defer mc.close()

		// Close one server to simulate failure after initialization
		rpcServers[0].close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when BP fails after initialization")
	})

	t.Run("submitPrivateTx with all BPs failing after initialization", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		// Close all remaining servers
		rpcServers[1].close()
		rpcServers[2].close()
		rpcServers[3].close()

		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), false, nil)
		require.Error(t, err, "expected error when all BPs fail")
	})
}

func TestCheckTxStatus(t *testing.T) {
	t.Parallel()

	// Create a dummy tx
	tx1 := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)

	// Initialize 4 healthy servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 4)
	var urls []string = make([]string, 4)
	for i := 0; i < 4; i++ {
		rpcServers[i] = newMockRpcServer()
		// Mock all servers to return pending status for tx1
		rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
			tx1.Hash(): txpool.TxStatusPending,
		}))
		urls[i] = rpcServers[i].server.URL
	}

	t.Run("checkTxStatus with all BPs having tx as pending", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.NoError(t, err, "expected no error in checking tx status")
		require.True(t, res, "expected result to be true as status is pending in all BPs")
	})

	t.Run("checkTxStatus with unknown tx hash", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(common.HexToHash("0x1"))
		require.NoError(t, err, "expected no error in checking tx status")
		require.False(t, res, "expected result to be false as status is unknown")
	})

	t.Run("checkTxStatus with mixed statuses across BPs", func(t *testing.T) {
		// Some BPs have pending, some have unknown
		rpcServers[0].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
			tx1.Hash(): txpool.TxStatusPending,
		}))
		rpcServers[1].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
			tx1.Hash(): txpool.TxStatusPending,
		}))
		rpcServers[2].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{}))
		rpcServers[3].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{}))

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.NoError(t, err, "expected no error in checking tx status")
		require.False(t, res, "expected result to be false as not all BPs have tx in pending/included state")
	})

	t.Run("checkTxStatus with all BPs returning unknown status", func(t *testing.T) {
		// All servers return unknown status
		for i := range rpcServers {
			rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{}))
		}

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.NoError(t, err, "expected no error in checking tx status")
		require.False(t, res, "expected result to be false as all BPs return unknown status")
	})

	t.Run("checkTxStatus with tx queued in all BPs", func(t *testing.T) {
		// All servers return queued status (should not count as valid)
		for i := range rpcServers {
			rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
				tx1.Hash(): txpool.TxStatusQueued,
			}))
		}

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.NoError(t, err, "expected no error in checking tx status")
		require.False(t, res, "expected result to be false as queued is not accepted")
	})

	t.Run("checkTxStatus with error in one RPC server", func(t *testing.T) {
		// Reset to all pending
		for i := range rpcServers {
			rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
				tx1.Hash(): txpool.TxStatusPending,
			}))
		}

		// One server returns error
		rpcServers[0].setHandleTxStatus(func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.Error(t, err, "expected error in checking tx status")
		require.ErrorContains(t, err, "internal server error", "expected internal server error")
		require.False(t, res, "expected result to be false due to error")
	})

	t.Run("checkTxStatus with timeout in one RPC server", func(t *testing.T) {
		// One server times out
		rpcServers[0].setHandleTxStatus(func(w http.ResponseWriter, id int, params json.RawMessage) {
			time.Sleep(rpcTimeout + 100*time.Millisecond)
			makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
				tx1.Hash(): txpool.TxStatusPending,
			})(w, id, params)
		})

		mc := newMultiClient(urls)
		defer mc.close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.Error(t, err, "expected error due to timeout")
		require.ErrorContains(t, err, "context deadline exceeded", "expected context deadline exceeded error")
		require.False(t, res, "expected result to be false due to timeout")
	})

	t.Run("checkTxStatus runs in parallel", func(t *testing.T) {
		// All calls take almost rpcTimeout but don't exceed it
		for i := range rpcServers {
			rpcServers[i].setHandleTxStatus(func(w http.ResponseWriter, id int, params json.RawMessage) {
				time.Sleep(rpcTimeout - 100*time.Millisecond)
				makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
					tx1.Hash(): txpool.TxStatusPending,
				})(w, id, params)
			})
		}

		mc := newMultiClient(urls)
		defer mc.close()

		start := time.Now()
		res, err := mc.checkTxStatus(tx1.Hash())
		elapsed := time.Since(start)

		require.NoError(t, err, "expected no error in checking tx status")
		require.True(t, res, "expected result to be true")
		require.Less(t, elapsed, 2*time.Second, "expected parallel calls to finish below total timeout")
		require.Greater(t, elapsed, rpcTimeout-100*time.Millisecond, "expected calls to take at least the time of one call")
	})

	t.Run("checkTxStatus with some failing servers after initialization", func(t *testing.T) {
		// Reset all handlers to default
		for i := range rpcServers {
			rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
				tx1.Hash(): txpool.TxStatusPending,
			}))
		}

		// Initialize multiclient with all healthy servers
		mc := newMultiClient(urls)
		defer mc.close()

		// Close one server to simulate failure after initialization
		rpcServers[0].close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.Error(t, err, "expected error due to failed server")
		require.False(t, res, "expected result to be false due to failed server")
	})

	t.Run("checkTxStatus with all failing servers", func(t *testing.T) {
		mc := newMultiClient(urls)
		defer mc.close()

		// Close all remaining servers
		rpcServers[1].close()
		rpcServers[2].close()
		rpcServers[3].close()

		res, err := mc.checkTxStatus(tx1.Hash())
		require.Error(t, err, "expected error with all servers failing")
		require.False(t, res, "expected result to be false with all servers failing")
	})
}

func TestPrivateTxSubmissionRetry(t *testing.T) {
	t.Parallel()

	// Create a dummy tx
	tx1 := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
	rawTx, err := tx1.MarshalBinary()
	require.NoError(t, err, "error in marshalling dummy tx")

	// Create 4 servers that succeed on first attempt
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 4)
	var urls []string = make([]string, 4)
	for i := 0; i < 4; i++ {
		rpcServers[i] = newMockRpcServer()
		rpcServers[i].setHandleTxStatus(makeTxStatusHandler(map[common.Hash]txpool.TxStatus{}))
		urls[i] = rpcServers[i].server.URL
	}

	t.Run("retry succeeds after N attempts", func(t *testing.T) {
		// Track call counts for servers that will fail initially
		var callCounts [4]atomic.Int32

		// Servers 0 and 1 fail twice, then succeed
		for i := 0; i < 2; i++ {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				count := callCounts[i].Add(1)
				if count <= 2 {
					// Fail first 2 attempts
					defaultSendError(w, id, -32601, "internal server error")
				} else {
					// Succeed on 3rd attempt
					defaultHandleSendPrivateTx(w, id, params)
				}
			})
		}

		// Servers 2 and 3 always succeed. Just track call counts.
		for i := 2; i < 4; i++ {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for retries to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Verify that failing servers were called multiple times including initial submission
		require.Equal(t, int32(3), callCounts[0].Load(), "expected server 0 to be called 3 times")
		require.Equal(t, int32(3), callCounts[1].Load(), "expected server 1 to be called 3 times")

		// Verify that healthy servers were only called once during initial submission
		require.Equal(t, int32(1), callCounts[2].Load(), "expected server 2 to be called once")
		require.Equal(t, int32(1), callCounts[3].Load(), "expected server 3 to be called once")
	})

	t.Run("retry stops when tx found in local database", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 fails always
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			callCounts[0].Add(1)
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		// Set up txGetter that will return the transaction as found (simulating it got included)
		txGetter := func(hash common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64) {
			if hash == tx1.Hash() {
				return true, tx1, common.Hash{}, 0, 0
			}
			return false, nil, common.Hash{}, 0, 0
		}

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, txGetter)
		require.Error(t, err, "expected error on initial submission")

		// Wait for retry goroutine to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Since tx is found in local database, retry should stop early
		// Server 0 should be called only once during initial submission (no retries)
		require.Equal(t, int32(1), callCounts[0].Load(), "expected server 0 to be called only once, no retries after tx found")
		// All other servers should be called only once during initial submission
		for i := 1; i < 4; i++ {
			require.Equal(t, int32(1), callCounts[i].Load(), "expected server %d to be called only once during initial submission", i)
		}
	})

	t.Run("retry until max retries reached", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
			rpcServers[i].setHandleTxStatus(defaultHandleTxStatus)
		}

		// Server 0 always fails
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			callCounts[0].Add(1)
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for all retries to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Server 0 should be called 6 times (1 for initial submission and 5 retries)
		require.Equal(t, int32(6), callCounts[0].Load(), "expected server 0 to be called 6 times (1 initial + 5 retries)")
		// All other servers should be called exactly once during initial submission
		for i := 1; i < 4; i++ {
			require.Equal(t, int32(1), callCounts[i].Load(), "expected server %d to be called only once during initial submission", i)
		}
	})

	t.Run("retry with mixed success and failure", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 fails once, then succeeds
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			count := callCounts[0].Add(1)
			if count == 1 {
				defaultSendError(w, id, -32601, "temporary failure")
			} else {
				defaultHandleSendPrivateTx(w, id, params)
			}
		})

		// Server 1 always fails
		rpcServers[1].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			callCounts[1].Add(1)
			defaultSendError(w, id, -32602, "permanent failure")
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for all retries to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Server 0 should be called twice (initial + 1 retry that succeeds)
		require.Equal(t, int32(2), callCounts[0].Load(), "expected server 0 to succeed on second attempt")

		// Server 1 should be called multiple times (keeps failing)
		require.Equal(t, int32(6), callCounts[1].Load(), "expected server 1 to be retried multiple times")

		// Other servers should be called only once during initial submission
		require.Equal(t, int32(1), callCounts[2].Load(), "expected server 2 to be called only once during initial submission")
		require.Equal(t, int32(1), callCounts[3].Load(), "expected server 3 to be called only once during initial submission")
	})

	t.Run("retry with all BPs eventually succeeding", func(t *testing.T) {
		var callCounts [4]atomic.Int32

		// All servers fail once, then succeed
		for i := 0; i < 4; i++ {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				count := callCounts[i].Add(1)
				if count == 1 {
					defaultSendError(w, id, -32601, "temporary failure")
				} else {
					defaultHandleSendPrivateTx(w, id, params)
				}
			})
		}

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission when all BPs fail")

		// Wait for retry to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// All servers should be called exactly twice (initial + 1 successful retry)
		for i := 0; i < 4; i++ {
			require.Equal(t, int32(2), callCounts[i].Load(), "expected server %d to be called exactly twice", i)
		}
	})

	t.Run("retry handles timeout in failed BP", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 times out on first call, succeeds on retry
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			count := callCounts[0].Add(1)
			if count == 1 {
				time.Sleep(rpcTimeout + 100*time.Millisecond)
			}
			defaultHandleSendPrivateTx(w, id, params)
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected timeout error on initial submission")
		require.ErrorContains(t, err, "context deadline exceeded", "expected timeout error")

		// Wait for retry to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Server 0 should be retried and succeed
		require.Equal(t, int32(2), callCounts[0].Load(), "expected server 0 to be retried after timeout")
		for i := 1; i < 4; i++ {
			require.Equal(t, int32(1), callCounts[i].Load(), "expected server %d to be called only once", i)
		}
	})

	t.Run("retry receives already known error on first retry", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
			rpcServers[i].setHandleTxStatus(defaultHandleTxStatus)
		}

		// Server 0 fails first, then returns already known on retry
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			count := callCounts[0].Add(1)
			if count == 1 {
				defaultSendError(w, id, -32601, "internal server error")
			} else {
				// On retry, return already known
				defaultSendError(w, id, -32000, "already known")
			}
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for retry goroutine to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Server 0 should be called twice (initial + 1 retry with already known)
		require.Equal(t, int32(2), callCounts[0].Load(), "expected server 0 to be called twice")
		// Goroutine is done (confirmed by <-done above), so no further retries are possible
		require.Equal(t, int32(2), callCounts[0].Load(), "expected no further retries after already known")
	})

	t.Run("retry with already known error from multiple BPs", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Servers 0 and 1 fail initially, then return already known on retry
		for i := 0; i < 2; i++ {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				count := callCounts[i].Add(1)
				if count == 1 {
					defaultSendError(w, id, -32601, "temporary failure")
				} else {
					// On retry, return already known
					defaultSendError(w, id, -32000, "already known")
				}
			})
		}

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for retry to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Servers 0 and 1 should be called twice (initial + retry)
		require.Equal(t, int32(2), callCounts[0].Load(), "expected server 0 to be called twice")
		require.Equal(t, int32(2), callCounts[1].Load(), "expected server 1 to be called twice")
		// Other servers should be called only once
		require.Equal(t, int32(1), callCounts[2].Load(), "expected server 2 to be called once")
		require.Equal(t, int32(1), callCounts[3].Load(), "expected server 3 to be called once")
	})

	t.Run("retry with already known on initial submission", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 returns already known on initial submission
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			callCounts[0].Add(1)
			defaultSendError(w, id, -32000, "already known")
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		// When "already known" is returned on the initial submission, it counts as success,
		// so no retry goroutine is started and the done channel will be nil.
		err, _ := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.NoError(t, err, "expected no error when all submissions succeed or return already known")

		// No retry goroutine was started (no failed indices), assert directly
		require.Equal(t, int32(1), callCounts[0].Load(), "expected server 0 to be called only once")
	})

	t.Run("retry with mix of already known and successful retries", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 fails, then returns already known
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			count := callCounts[0].Add(1)
			if count == 1 {
				defaultSendError(w, id, -32601, "temporary failure")
			} else {
				defaultSendError(w, id, -32000, "already known")
			}
		})

		// Server 1 fails, then succeeds normally
		rpcServers[1].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			count := callCounts[1].Add(1)
			if count == 1 {
				defaultSendError(w, id, -32602, "temporary failure")
			} else {
				defaultHandleSendPrivateTx(w, id, params)
			}
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, nil)
		require.Error(t, err, "expected error on initial submission")

		// Wait for retry to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Both servers 0 and 1 should be called twice
		require.Equal(t, int32(2), callCounts[0].Load(), "expected server 0 to be called twice")
		require.Equal(t, int32(2), callCounts[1].Load(), "expected server 1 to be called twice")
		// Goroutine is done (confirmed by <-done above), so no further retries are possible
		require.Equal(t, int32(2), callCounts[0].Load(), "expected no further retries")
		require.Equal(t, int32(2), callCounts[1].Load(), "expected no further retries")
	})

	t.Run("retry with txGetter not finding tx continues retrying", func(t *testing.T) {
		// Reset handlers to default first
		var callCounts [4]atomic.Int32
		for i := range rpcServers {
			rpcServers[i].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
				callCounts[i].Add(1)
				defaultHandleSendPrivateTx(w, id, params)
			})
		}

		// Server 0 always fails
		rpcServers[0].setHandleSendPrivateTx(func(w http.ResponseWriter, id int, params json.RawMessage) {
			callCounts[0].Add(1)
			defaultSendError(w, id, -32601, "internal server error")
		})

		mc := newMultiClient(urls)
		mc.retryInterval = 10 * time.Millisecond
		defer mc.close()

		// Set up txGetter that doesn't find the transaction
		var txGetterCallCount atomic.Int32
		txGetter := func(hash common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64) {
			txGetterCallCount.Add(1)
			return false, nil, common.Hash{}, 0, 0
		}

		err, done := mc.submitPrivateTx(rawTx, tx1.Hash(), true, txGetter)
		require.Error(t, err, "expected error on initial submission")

		// Wait for all retries to complete deterministically
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("retry goroutine did not complete in time")
		}

		// Server 0 should be called 6 times (1 initial + 5 retries)
		require.Equal(t, int32(6), callCounts[0].Load(), "expected server 0 to be called 6 times")
		// TxGetter should be called 5 times (once per retry attempt)
		require.Equal(t, int32(5), txGetterCallCount.Load(), "expected txGetter to be called 5 times during retries")
	})
}
