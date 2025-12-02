# Chainmanager module

## Table of Contents

* [Overview](#overview)
* [Query commands](#query-commands)
  * [CLI Commands](#cli-commands)
  * [GRPC Endpoints](#grpc-endpoints)
  * [REST Endpoints](#rest-endpoints)

## Overview

The chainmanager module is responsible for fetching the PoS protocol parameters.
These params include addresses of contracts deployed on mainchain (Ethereum) and bor chain (Bor),
chain ids, mainchain and bor chain confirmation blocks.

```protobuf
message ChainParams {
  option (gogoproto.equal) = true;
  string bor_chain_id = 1 [ (amino.dont_omitempty) = true ];
  string heimdall_chain_id = 2 [ (amino.dont_omitempty) = true ];
  string pol_token_address = 3 [ (amino.dont_omitempty) = true ];
  string staking_manager_address = 4 [ (amino.dont_omitempty) = true ];
  string slash_manager_address = 5 [ (amino.dont_omitempty) = true ];
  string root_chain_address = 6 [ (amino.dont_omitempty) = true ];
  string staking_info_address = 7 [ (amino.dont_omitempty) = true ];
  string state_sender_address = 8 [ (amino.dont_omitempty) = true ];
  string state_receiver_address = 9 [ (amino.dont_omitempty) = true ];
  string validator_set_address = 10 [ (amino.dont_omitempty) = true ];
}

message Params {
  option (gogoproto.equal) = true;
  ChainParams chain_params = 1
  [ (amino.dont_omitempty) = true, (gogoproto.nullable) = false ];
  uint64 main_chain_tx_confirmations = 2 [ (amino.dont_omitempty) = true ];
  uint64 bor_chain_tx_confirmations = 3 [ (amino.dont_omitempty) = true ];
}
```

## Query commands

One can run the following query commands from the chainmanager module :

* `params` - Fetch the parameters associated with the chainmanager module.

### CLI commands

```bash
heimdalld query chainmanager params
```

### GRPC Endpoints

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.chainmanager.Query/GetChainManagerParams

```

### REST endpoints

```bash
curl localhost:1317/heimdallv2/chainmanager/params
```
