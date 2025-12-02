# Topup module

## Table of Contents

* [Overview](#overview)
* [Interact with the Node](#interact-with-the-node)
  * [Tx Commands](#tx-commands)
  * [CLI Query Commands](#cli-query-commands)
  * [GRPC Endpoints](#grpc-endpoints)
  * [REST Endpoints](#rest-apis)

## Overview

Heimdall Topup is an amount that will be used to pay fees on the heimdall chain.

There are two ways to top up your account:

1. When new validator joins, they can mention a `topup` amount as top-up in addition to the staked amount, which will be
   moved as balance on the heimdall chain to pay fees on Heimdall.
2. A user can directly call the top-up function on the staking smart contract on Ethereum to increase top-up balance on
   Heimdall.

## Flow

The Heimdall Top-up Mechanism facilitates the management of validator fees on the Heimdall v2 chain by allowing deposits from the Ethereum (L1) root chain.  
This mechanism ensures validators have enough balances on Heimdall to cover operational fees.  
The system integrates staking smart contract on Ethereum with Heimdall custom x/topup and x/checkpoint modules and a bridge component to enable seamless cross-chain fee management.  

### Top-Up Funding Methods
There are two primary ways to fund a validator’s fee balance on Heimdall:  
- During Validator Initialization: When a new validator joins, they can specify a top-up amount in addition to the staked amount. This top-up is transferred to Heimdall as an initial balance.
- Direct Top-Up: Any user can invoke the top-up function on the Ethereum staking smart contract to increase the validators’ top-up balance on Heimdall.

### Bridge Processing
Top-up events on the Ethereum layer trigger automated processing through the bridge process:  
- The Root Chain Log Listener monitors for `StakinginfoTopUpFee` events.  
- the Top-up Fee Processor task, upon detecting such an event, triggers the `sendTopUpFeeToHeimdall` execution.
The task:
— Decodes the Ethereum log.  
– Verifies the event hasn’t already been processed.  
— Broadcasts a `MsgTopupTx` to the Heimdall chain.  
In addition to the automated broadcasting of `MsgTopupTx` transactions, it is also possible to manually craft and submit these transactions at the Heimdall layer.  
This fallback mechanism is used in scenarios where issues arise in bridging or processing Ethereum events.  

### Heimdall x/topup Module Implementation
Two core messages are defined in the x/topup module for fee management:
- `MsgTopupTx`: Handles minting the top-up amount on Heimdall based on Ethereum events.  
Each top-up is uniquely identified by a sequence number built from `TxHash` and `LogIndex` to prevent duplicate processing.  
`MsgTopupTx` is a side-transaction, ensuring state changes only after the successful pre-commit majority of the votes are collected and final validation and post-tx handler execution in the following block height.  
When broadcasting the `MsgTopupTx` - sender (proposer of the topup) must sign it, and additional user address must be sent.  
For the top-up to be accepted, the `MsgTopupTx.Fee` must be at least equal to the `DefaultFeeWantedPerTx` amount. The top-up processing on Heimdall v2 involves:  
- Minting the top-up number of pol tokens to the top-up module account.  
- Transferring the entire amount from the top-up module account to the user account.  
- Transferring the `DefaultFeeWantedPerTx` amount from the user account to the proposer validator account.  
The remaining top-up amount stays on the user account.

- `MsgWithdrawFeeTx`: Allows validators to withdraw fees from Heimdall back to Ethereum.
The withdrawal process involves:
- Transferring the amount from the validator to the top-up module account.  
- Burning the amount from the top-up module account.  
- Updating the validator’s dividend “account” with the withdrawn amount.  
- No impact on the user account used during the `MsgTopupTx`.

## Messages

### MsgTopupTx

`MsgTopupTx` is responsible for minting balance to an address on Heimdall based on Ethereum chain's `TopUpEvent` on
staking manager contract.

Handler for this transaction processes top-up and increases the balance only once for any given `msg.TxHash`
and `msg.LogIndex`. It throws an error if trying to process the top-up more than once.

Here is the structure for the top-up transaction message:

```protobuf
message MsgTopupTx {
  option (cosmos.msg.v1.signer) = "proposer";
  option (amino.name) = "heimdallv2/topup/MsgTopupTx";
  string proposer = 1 [
    (cosmos_proto.scalar) = "cosmos.AddressString",
    (amino.dont_omitempty) = true
  ];
  string user = 2 [
    (cosmos_proto.scalar) = "cosmos.AddressString",
    (amino.dont_omitempty) = true
  ];
  string fee = 3 [
    (gogoproto.customtype) = "cosmossdk.io/math.Int",
    (gogoproto.nullable) = false,
    (amino.dont_omitempty) = true
  ];
  bytes tx_hash = 4 [ (amino.dont_omitempty) = true ];
  uint64 log_index = 5 [ (amino.dont_omitempty) = true ];
  uint64 block_number = 6 [ (amino.dont_omitempty) = true ];
}
```

### MsgWithdrawFeeTx

`MsgWithdrawFeeTx` is responsible for withdrawing balance from Heimdall to the Ethereum chain.
A Validator can withdraw any amount from Heimdall.

Handler processes the withdrawal by deducting the balance from the given validator and prepares the state to send the next
checkpoint. The next possible checkpoint will contain the withdrawal-related state for the specific validator.

Handler gets validator information based on `ValidatorAddress` and processes the withdrawal.

```protobuf
message MsgWithdrawFeeTx {
  option (cosmos.msg.v1.signer) = "proposer";
  option (amino.name) = "heimdallv2/topup/MsgWithdrawFeeTx";
  string proposer = 1 [ (amino.dont_omitempty) = true ];
  string amount = 2 [
    (gogoproto.customtype) = "cosmossdk.io/math.Int",
    (gogoproto.nullable) = false,
    (amino.dont_omitempty) = true
  ];
}
```

## Interact with the Node

### Tx Commands

#### Topup fee

```bash
heimdalld tx topup handle-topup-tx [proposer] [user] [fee] [tx_hash] [log_index] [block_number]
```

#### Withdraw fee

```bash
heimdalld tx topup withdraw-fee [proposer] [amount]
```

### CLI Query Commands

One can run the following query commands from the topup module:

* `topup-sequence` - Query the sequence of a topup tx
* `is-old-tx` - Check if a tx is old (already submitted)
* `dividend-account` - Query a dividend account by its address
* `dividend-account-root` - Query dividend account root hash
* `account-proof` - Query account proof
* `verify-account-proof` - Verify account proof

```bash
heimdalld query topup topup-sequence [tx_hash] [log_index]
```

```bash
heimdalld query topup is-old-tx [tx_hash] [log_index]
```

```bash
heimdalld query topup dividend-account [address]
```

```bash
heimdalld query topup dividend-account-root
```

```bash
heimdalld query topup account-proof [address]
```

```bash
heimdalld query topup verify-account-proof [address] [proof]
```

### GRPC Endpoints

The endpoints and the params are defined in the [topup/query.proto](/proto/heimdallv2/topup/query.proto) file.
Please refer to them for more information about the optional params.

```bash
grpcurl -plaintext -d '{"tx_hash": <>, "log_index": <>}' localhost:9090 heimdallv2.topup.Query/IsTopupTxOld
```

```bash
grpcurl -plaintext -d '{"tx_hash": <>, "log_index": <>}' localhost:9090 heimdallv2.topup.Query/GetTopupTxSequence
```

```bash
grpcurl -plaintext -d '{"address": <>}' localhost:9090 heimdallv2.topup.Query/GetDividendAccountByAddress
```

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.topup.Query/GetDividendAccountRootHash
```

```bash
grpcurl -plaintext -d '{"address": <>, "proof": <>}' localhost:9090 heimdallv2.topup.Query/VerifyAccountProofByAddress
```

```bash
grpcurl -plaintext -d '{"address": <>}' localhost:9090 heimdallv2.topup.Query/GetAccountProofByAddress
```

### REST APIs

The endpoints and the params are defined in the [topup/query.proto](/proto/heimdallv2/topup/query.proto) file.
Please refer to them for more information about the optional params.

```bash
curl localhost:1317/topup/is-old-tx?tx_hash=<tx-hash>&log_index=<log-index>
```

```bash
curl localhost:1317/topup/sequence?tx_hash=<tx-hash>&log_index=<log-index>
```

```bash
curl localhost:1317/topup/dividend-account/{address}
```

```bash
curl localhost:1317/topup/dividend-account-root
```

```bash
curl localhost:1317/topup/account-proof/{address}/verify
```

```bash
curl localhost:1317/topup/account-proof/{address}
```
