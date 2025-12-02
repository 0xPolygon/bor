# Milestone module

## Table of Contents

* [Overview](#overview)
* [Interact with the Node](#interact-with-the-node)
  * [Tx Commands](#tx-commands)
  * [CLI Query Commands](#cli-query-commands)
  * [GRPC Endpoints](#grpc-endpoints)
  * [REST Endpoints](#rest-apis)

## Overview

This module enables deterministic finality by leveraging Polygon PoS’s dual client architecture.  
This is done using a hybrid system that uses Peppermint layer consensus,  
along with an additional fork choice rule within the execution layer.
With the introduction of milestones, finality is deterministic even before a checkpoint is submitted to L1.  
After a certain number of blocks (minimum 1), a milestone is proposed and voted by Heimdall.  
Once 2/3+ of the network agrees, the milestone is finalized, and all transactions up to that milestone are considered final, with no chance of reorganization.

## Flow

Milestones are a lightweight alternative to checkpoints in Heimdall, used to finalize blocks more efficiently.  
With the introduction of milestones, finality is deterministic even before a checkpoint is submitted to L1.  
Unlike the original transaction-based design in Heimdall-v1, the current design operates without transactions, relying entirely on ABCI++ flow and validator vote extensions.  
Each validator proposes a milestone independently. Milestones are proposed as a series of recent, up to 10 block hashes, and a majority (2/3 of voting power) agreement on a consecutive sequence of these hashes is required to finalize a milestone.  
The system tolerates duplication to increase reliability and includes logic for resolving forks and ensuring milestone continuity.  

### Milestone Proposals and Duplication
Validators independently propose a sequence of block hashes, at most `MaxMilestonePropositionLength` starting from the last finalized milestone:  
```protobuf
message MilestoneProposition {
option (gogoproto.equal) = true;
option (gogoproto.goproto_getters) = true;
repeated bytes block_hashes = 1 [ (amino.dont_omitempty) = true ];
uint64 start_block_number = 2 [ (amino.dont_omitempty) = true ];
}
```
Proposals are handled in the `ExtendVoteHandler`, executed at each block.  
A milestone proposed in block `N` is finalized in block `N+1` (or later), introducing acceptable duplication of proposed milestones to improve reliability.  
This duplication ensures that even if a milestone isn’t finalized in `N+1`, it may succeed in `N+2` or later.  
In cases of failed milestone propositions, the node still participates in the consensus.  

### Proposed Milestone validation checks
Proposed milestone validation is performed in N block with `ValidateMilestoneProposition` function in`ExtendVoteHandler` and in `VerifyVoteExtensionHandler`:  
- Length is validated: the milestone proposal, if created should not contain more block hashes than `MaxMilestonePropositionLength`;  
- Each block hash length is validated to be of the appropriate length.  

### Majority Determination in the following block
Vote extensions from other validators are collected and unmarshalled.  
Duplicate vote extensions from the same validator are ignored.  
The algorithm uses data structures keyed by `(block_number, block_hash)` to handle forks (same block number may have different hashes), so that fork-resilience is achieved by:  
- Separating vote data by hash and block number.
- Ensuring the finalized milestones continue from the last one with no gaps.  
The core algorithm looks for:  
- The longest consecutive sequence of block hashes.  
- Supported by >= 2/3 of the total voting power.  

### Milestones Validation and Finalization
Once a consensus over the milestone is reached, the majority milestone is validated with `ValidateMilestoneProposition` checks again for integrity in `PreBlocker`.  
If the validation passes, the milestone is persisted.  

## Messages

### Milestone

`Milestone` defines a message for submitting a milestone
```protobuf
message Milestone {
  option (gogoproto.equal) = true;
  option (gogoproto.goproto_getters) = true;
  string proposer = 1 [
    (amino.dont_omitempty) = true,
    (cosmos_proto.scalar) = "cosmos.AddressString"
  ];
  uint64 start_block = 2 [ (amino.dont_omitempty) = true ];
  uint64 end_block = 3 [ (amino.dont_omitempty) = true ];
  bytes hash = 4 [ (amino.dont_omitempty) = true ];
  string bor_chain_id = 5 [ (amino.dont_omitempty) = true ];
  string milestone_id = 6 [ (amino.dont_omitempty) = true ];
  uint64 timestamp = 7 [ (amino.dont_omitempty) = true ];
}
```

## Interact with the Node

### Tx Commands

#### Send Milestone Transaction 
```bash
heimdalld tx milestone milestone [proposer] [startBlock] [endBlock] [hash] [borChainId] [milestoneId]
```

### CLI Query Commands

One can run the following query commands from the milestone module:

* `get-params` - Get milestone params
* `get-count` - Get milestone count
* `get-latest-milestone` - Get latest milestone
* `get-milestone-by-number` - Get the milestone by number
* `get-milestone-proposer` - Get the milestone proposer
* `get-latest-no-ack-milestone` - Get the latest no ack milestone
* `get-no-ack-milestone-by-id` - Get the no ack milestone by id

```bash
heimdalld query milestone get-params
```

```bash
heimdalld query milestone get-count
```

```bash
heimdalld query milestone get-latest-milestone
```

```bash
heimdalld query milestone get-milestone-by-number
```

```bash
heimdalld query milestone get-milestone-proposer
```

```bash
heimdalld query milestone get-latest-no-ack-milestone
```

```bash
heimdalld query milestone get-no-ack-milestone-by-id
```

### GRPC Endpoints

The endpoints and the params are defined in the [milestone/query.proto](/proto/heimdallv2/milestone/query.proto) file.
Please refer to them for more information about the optional params.

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.milestone.Query/GetMilestoneParams
```

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.milestone.Query/GetMilestoneCount
```

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.milestone.Query/GetLatestMilestone
```

```bash
grpcurl -plaintext -d '{}' localhost:9090 heimdallv2.milestone.Query/GetMilestoneByNumber
```

### REST APIs

The endpoints and the params are defined in the [milestone/query.proto](/proto/heimdallv2/milestone/query.proto) file.
Please refer to them for more information about the optional params.

```bash
curl localhost:1317/milestones/params
```

```bash
curl localhost:1317/milestones/count
```

```bash
curl localhost:1317/milestones/latest
```

```bash
curl localhost:1317/milestones/{number}
```
