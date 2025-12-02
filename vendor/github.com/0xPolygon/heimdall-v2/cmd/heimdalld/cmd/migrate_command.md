# Migration command

The command will take as input Heimdall v1 genesis and do all the necessary transformations to make it compatible with Heimdall v2.

## Modules status

- [x] auth
- [x] bank
- [x] gov
- [x] checkpoint
- [x] clerk
- [x] bor
- [x] topup
- [x] milestone
- [x] chainmanager
- [x] stake

## Modules' app state size on the mainnet

We don't have the exact height at which the export was generated, but the most recent checkpoint in the genesis
was generated at timestamp 1706745464 (Wednesday, January 31, 2024 11:57:44 PM).

- **auth**: 0.10 MB (107260 bytes)
- **bank**: 0.00 MB (27 bytes)
- **gov**: 0.01 MB (13407 bytes)
- **chainmanager**: 0.00 MB (772 bytes)
- **bor**: 424.81 MB (445450568 bytes)
- **topup**: 0.01 MB (7843 bytes)
- **clerk**: 2355.08 MB (2469477639 bytes)
- **checkpoint**: 15.85 MB (16620086 bytes)

## Consensus parameters

The following missing parameters, required by CometBFT, are added: `consensus_params.evidence.max_age_num_blocks`, `consensus_params.evidence.max_age_duration`, `consensus_params.evidence.max_bytes`.  
The following Tendermint parameters that are not used by CometBFT are removed: `consensus_params.evidence.max_age`, `consensus_params.block.time_iota_ms`.

## Modules migration steps

### checkpoint

Parsing `checkpoint_buffer_time`, which is in nanoseconds,
and converting it into seconds with suffix `s`, otherwise it cannot be unmarshalled into `time.Duration`.  
Renaming `child_chain_block_interval` to `child_block_interval`.  
Iterating over all the `checkpoints`, decoding the hex-encoded `root_hash` and encoding it as base64.

### clerk

Iterating over all the `records`, decoding the hex-encoded `data` property and encoding it as base64.

### bor

Iterating over all the `spans` and renaming `bor_chain_id` to `chain_id`, `span_id` to `id`.  
Converting every `validator` object by renaming the following properties: `power` to `voting_power`,  
`accum` to `proposer_priority`, `ID` to `val_id`, and converting each `pubKey` from a plain string to `secp256k1.PubKey`.

### topup

Renaming `tx_sequences` to `topup_sequences`.

### milestone

Milestone genesis is not exported from Heimdall v1, so we just add it to the genesis. It has just one property `params`, and we initialize it with the default params from Heimdall v2.

### chainmanager

Renaming `params.mainchain_tx_confirmations` to `params.main_chain_tx_confirmations` and `params.maticchain_tx_confirmations` to `params.bor_chain_tx_confirmations`.

### stake

Change the module name from `staking` to `stake` in the genesis itself.  
Renaming `current_val_set` to `current_validator_set`.  
Converting every `validator` object by renaming the following properties: `power` to `voting_power`,  
`accum` to `proposer_priority`, `ID` to `val_id`, and converting each `pubKey` from a plain string to `secp256k1.PubKey`.

### Cosmos SDK modules

#### v0.37 - v0.38

No migration required, genesis files are compatible. [Link](https://github.com/cosmos/cosmos-sdk/blob/37b7221abdda540270adb2d51bdc87a22e417339/x/genutil/client/cli/migrate.go#L31)

### auth

In a lot of the migrations, we can see logic regarding vesting accounts. That's not of interest to us since vesting logic is disabled in Heimdall, and we don't have such accounts.  
We skip the module accounts during the migration because Heimdall v2 will initialize them from zero anyway.

#### v0.38 - v0.39

None of the changes concern us in this migration.
They refactored vesting accounts, added a vesting account type,
and switched from the default Go JSON marshal/unmarshal package to using `legacy.Cdc`.
[Link](https://github.com/cosmos/cosmos-sdk/blob/v0.39.3/x/auth/legacy/v0_39/migrate.go)

#### v0.39 - v0.40

In this migration, the `Coins` property was dropped, and accounts start to get encoded into `codectypes.Any`. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.41.4/x/auth/legacy/v040/migrate.go#L106)

#### v0.40 - v0.43

None of the changes concern us in this migration. It's just about vesting accounts. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.45.16/x/auth/legacy/v043/store.go)

#### v0.43 - v0.46

There's a migration executed on the database that is just creating a mapping from account address to account ID. This is not interesting to us because such mappings are internal to the module and never exported. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.45.16/x/auth/legacy/v043/store.go)

#### v0.46 - v0.47

There's a migration executed on the database. It moves params from the `x/params` module to the `x/auth` module. It's not interesting to us, because we already store params in the `auth` module genesis. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.47.14/x/auth/migrations/v4/migrate.go)

#### v0.47 - v0.50

There's a migration executed on the database that changes how the global account number is stored. It's not interesting to us because during the import of the genesis, the `auth` module will find the highest account number and store it in the appropriate key. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.6/x/auth/migrations/v5/migrate.go)

### bank

#### v0.38 - v0.40

The majority of the changes in the bank module state happen in this migration. Users' balances are moved from `auth` to `bank`. There is a `supply` module that holds the total supply, which is also moved into the `bank` module. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.41.2/x/genutil/legacy/v040/migrate.go)

#### v0.40 - v0.41

There is no Cosmos SDK migration, but there is a Gaia migration to add denom metadata. This data is useful only for clients and wallets that display different denoms from the network, to know their exponent. It's not of use for us. [Link](https://github.com/cosmos/gaia/blob/6d46572f3273423ad9562cf249a86ecc8206e207/app/migrate.go#L133-L150)

#### v0.41 - v0.43

There is a migration, but it's only in the database, pruning zero balance accounts, changing different prefixes. It doesn't concern us. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.44.0/x/bank/legacy/v043/store.go)

#### v0.43 - v0.45

There is a migration, but it's only in the database, adding some additional prefixes. It doesn't concern us. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.10/x/bank/migrations/v3/store.go)

#### v0.47 - v0.50

There is a migration, but it's only in the database, migrating some parameters from the `params` module to the `bank` module store. It doesn't concern us. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.10/x/bank/migrations/v4/store.go)

### gov

#### v0.38 - v0.40

In this migration, there are a lot of changes in the `gov` module state. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.41.2/x/gov/legacy/v040/migrate.go)

#### v0.40 - v0.43

In this migration, only the proposal votes structure is modified, moving from simple votes to weighted votes. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.44.0/x/gov/legacy/v043/json.go)

#### v0.43 - v0.46

In this migration, the genesis structure is changed from the v1beta1 version to the v1 version. Proposals are moved to a message-based structure. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.10/x/gov/migrations/v3/json.go)

#### v0.46 - v0.47

More parameters are introduced. `DepositParams`, `VotingParams`, and `TallyParams` are being deprecated in favor of the new `Params` property. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.10/x/gov/migrations/v4/json.go)

#### v0.47 - v0.50

Adding some of the same parameters that were added during the previous migration and also introducing the `Constitution` property. [Link](https://github.com/cosmos/cosmos-sdk/blob/v0.50.10/x/gov/migrations/v5/store.go)
