# Relay Service - Transaction Preconfirmation and Private Transaction Relay

## Overview

The relay service provides infrastructure for submitting transactions to block producers with two key features:
1. **Preconfirmation (Preconf)**: Submit transactions to block producers and get acknowledgment that they will be included in upcoming blocks
2. **Private Transactions**: Submit transactions privately to block producers without broadcasting to the public mempool

This service acts as a relay between the node and multiple block producer RPC endpoints, handling parallel submissions, retries, status tracking, and caching.

## Architecture

### Core Components

```
eth/relay/
├── relay.go              # Main service wrapper and public API
├── service.go            # Core service with task queue and cache
├── multiclient.go        # Multi-RPC client for parallel submissions
├── private_tx_store.go   # Private transaction tracking store
└── *_test.go            # Test suite
```

### Package Structure

**relay.go**
- `RelayService`: Main service wrapper that coordinates all relay functionality
- Public API: `SubmitPreconfTransaction()`, `CheckPreconfStatus()`, `SubmitPrivateTransaction()`
- Configuration: Manages enable/accept flags for preconf and private tx features

**service.go**
- `Service`: Core service managing task queue, semaphore, and cache
- Task processing pipeline: Queue → Rate limiting → Submission → Cache update
- `TxGetter`: Function type for querying local database for included transactions
- `updateTaskInCache()`: Helper for safe cache updates preventing status downgrades

**multiclient.go**
- `multiClient`: Manages multiple RPC connections to block producers
- Parallel submission logic with atomic counters
- Retry mechanism for private transaction failures
- "Already known" error detection and handling

**private_tx_store.go**
- `PrivateTxStore`: Tracks private transactions with automatic cleanup
- Chain event subscription for detecting included transactions

## Key Features

### 1. Preconfirmation System

**Flow:**
- User submits transaction, it gets queued for processing
- Service submits to all block producers and updates cache
- On status check, service first checks cache (returns immediately if preconfirmed)
- If not in cache, checks local database for inclusion
- If not found locally, queries block producers and updates cache

**Key Behaviors:**
- **Consensus Requirement**: Preconf is only offered if ALL block producers acknowledge the transaction
- **Status Persistence**: Once `preconfirmed=true`, it never reverts to false
- **Cache-First**: Always checks cache before making external queries
- **Local DB Priority**: Checks local database for included transactions before querying block producers

### 2. Private Transaction Submission

**Flow:**
- Transaction submitted in parallel to all block producers
- Failed submissions are retried in background
- During retry, service checks local database for inclusion
- If found in local database, retry stops
- If not found, continues retrying up to maximum retry limit

**Key Behaviors:**
- **Parallel Submission**: All block producers receive the transaction simultaneously
- **Best Effort**: Returns success if at least one block producer accepts
- **Background Retry**: Failed submissions are retried in background (max 5 retries, 2s interval)
- **Inclusion Detection**: Uses `TxGetter` to check if transaction was included in a block (stops retry)
- **Already Known Handling**: "already known" errors are treated as successful submissions

### 3. Already Known Error Handling

**Concept:**
When submitting a transaction, block producers may return "already known" error if they already have the transaction in their mempool. This should NOT be treated as a failure.

**Behavior:**
- **Preconf Submission**: "already known" counts as preconfirmed for that block producer
- **Private Tx Submission**: "already known" counts as successful submission
- **Retry Logic**: "already known" during retry stops retry for that block producer

### 4. Transaction Getter (TxGetter)

**Purpose:**
Check local database for transaction inclusion BEFORE querying block producers via RPC.

**Usage:**
- **CheckTxPreconfStatus**: Checks local DB first, returns preconf=true if found
- **Retry Logic**: Stops retrying private tx if found in local DB
- **Performance**: Avoids unnecessary RPC calls for included transactions
- **Reliability**: Local DB is authoritative source for inclusion status

**Integration:**
- Set via `SetTxGetter()` after APIBackend is initialized
- Uses `eth.APIBackend.GetCanonicalTransaction` in production

## Configuration

### ServiceConfig

**Fields:**
- `expiryTickerInterval`: How often to run cleanup (default: 1 minute)
- `expiryInterval`: How long to keep tasks in cache (default: 10 minutes)
- `maxQueuedTasks`: Task queue buffer size (default: 40,000)
- `maxConcurrentTasks`: Semaphore limit for parallel processing (default: 1,024)

### Initialization

Service is initialized in `eth/backend.go` with:
- Enable/accept flags for preconf and private tx features
- List of block producer RPC URLs
- Transaction getter is set after APIBackend initialization

## Rate Limiting & Concurrency

### Task Queue
- Buffered channel with configurable size
- Non-blocking until full
- FIFO processing order

### Semaphore
- Limits concurrent task processing
- Prevents overwhelming block producers with parallel requests

### Cleanup
- Periodic ticker deletes expired tasks from cache
- Prevents unbounded cache growth

## Testing

### Test Coverage
- **service_test.go**: Task queue, cache, preconf status, concurrent cache updates
- **multiclient_test.go**: Parallel submissions, retries, already known handling
- **private_tx_store_test.go**: Private tx tracking and cleanup

### Key Test Scenarios
1. **Already Known**: Proper handling as success for both preconf and private tx
2. **Retry Logic**: Background retries with inclusion detection via TxGetter
3. **Cache Updates**: Preconfirmed status preservation across concurrent updates
4. **Queue Overflow**: Burst submissions exceeding queue capacity
5. **RPC Failures**: Timeouts and server failures
6. **TxGetter Integration**: Local DB checks and fallback to RPC

### Mock Infrastructure
- `mockRpcServer`: HTTP test server simulating block producer RPC
- Configurable handlers for different RPC methods
- Support for error injection and timeouts

## Performance Considerations

### Optimizations
1. **Cache-First**: Reduces RPC load by serving cached results
2. **Local DB Check**: Avoids RPC calls for included transactions
3. **Parallel Submissions**: Submits to all BPs concurrently
4. **Background Retry**: Non-blocking retry mechanism
5. **Atomic Operations**: Lock-free counters for high-concurrency scenarios

### Bottlenecks
1. **Queue Size**: Limited by `maxQueuedTasks`
2. **Semaphore**: Limited by `maxConcurrentTasks`
3. **RPC Timeout**: 2 second timeout per RPC call
4. **Cache Growth**: Mitigated by periodic cleanup

## Security Considerations

1. **Private Transactions**: Only submitted to trusted block producer endpoints
2. **No Broadcast**: Private txs never hit public mempool
3. **Rate Limiting**: Semaphore prevents DoS via task queue
4. **Queue Overflow**: Drops tasks when queue is full (no unbounded memory growth)

## Future Enhancements

Potential areas for improvement:
1. **Partial Preconf**: Offer preconf if majority (not all) BPs confirm
2. **Dynamic Retry**: Adjust retry interval based on network conditions
3. **BP Health**: Track block producer health and skip unhealthy ones
4. **Graceful Degradation**: Continue operation with partial BP availability

## References

**RPC Methods:**
- `eth_sendRawTransactionForPreconf`: Submit tx for preconfirmation
- `eth_sendRawTransactionPrivate`: Submit private transaction
- `txpool_txStatus`: Check transaction status in block producer pool

**Related Packages:**
- `core/types`: Transaction types
- `core/txpool`: Transaction pool status constants
- `rpc`: RPC client for block producer communication
- `eth`: Integration point (backend.go)
