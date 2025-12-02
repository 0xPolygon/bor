# cmd


The `cmd` package is responsible for starting the heimdall application and provides the CLI framework (based on [cobra](https://github.com/spf13/cobra)).

## heimdalld

`heimdalld` is the service that is responsible for starting the heimdall application and also to interact with the heimdall application.

To check the available commands, run:

```bash
heimdalld --help
```

The output is something like this:

```bash
Usage:
  heimdalld [command]

Available Commands:
  approve                Approve the tokens to stake
  comet                  CometBFT subcommands
  completion             Generate the autocompletion script for the specified shell
  config                 Utilities for managing application configuration
  create-testnet         Initialize files for a Heimdall testnet
  debug                  Tool for helping with debugging your application
  export                 Export state to JSON
  generate-keystore      Generates keystore file
  generate-validator-key Generate validator key
  heimdall-bridge        Heimdall bridge daemon
  help                   Help about any command
  import-keystore        Import keystore from a private key stored in file (without 0x prefix)
  import-validator-key   Import private key from a private key stored in file (without 0x prefix)
  init                   Initialize private validator, p2p, genesis, and application configuration files
  keys                   Manage your application's keys
  migrate                Migrate application state
  prune                  Prune app history states by keeping the recent heights and deleting old heights
  query                  Querying subcommands
  rollback               rollback Cosmos SDK and CometBFT state by one height
  show-private-key       Print the account's private key
  snapshots              Manage local snapshots
  stake                  Stake pol tokens for your account
  start                  Run the full node
  status                 Query remote node for status
  tx                     Transactions subcommands
  ve-decode              Decode VEs for a specific block height
  verify-genesis         Verify if the genesis matches
  version                Print the app version

Flags:
      --amqp_url string                   Set AMQP endpoint
      --app string                        Override of Heimdall app config file (default <home>/config/config.json)
      --bor_grpc_flag string              gRPC flag for bor chain
      --bor_grpc_url string               Set gRPC endpoint for bor chain
      --bor_rpc_url string                Set RPC endpoint for bor chain
      --chain string                      Set one of the chains: [mainnet,mumbai,amoy,local]
      --checkpoint_poll_interval string   Set check point pull interval
      --clerk_poll_interval string        Set clerk pull interval
      --comet_bft_rpc_url string          Set RPC endpoint for CometBFT
      --eth_rpc_url string                Set RPC endpoint for ethereum chain
      --grpc_server string                Set GRPC Server Endpoint
      --heimdall_rest_server string       Set Heimdall REST server endpoint
  -h, --help                              help for heimdalld
      --home string                       directory for config and data (default "/var/lib/heimdall")
      --log_format string                 The logging format (json|plain) (default "plain")
      --log_level string                  The logging level (trace|debug|info|warn|error|fatal|panic|disabled) (default "info")
      --log_no_color                      Disable colored logs
      --logs_writer_file string           Set logs writer file, Default is os.Stdout
      --main_chain_gas_limit uint         Set main chain gas limit
      --main_chain_max_gas_price int      Set main chain max gas limit
      --milestone_poll_interval string    Set milestone interval (default "30s")
      --no_ack_wait_time string           Set time ack service waits to clear buffer and elect new proposer
      --noack_poll_interval string        Set no acknowledge pull interval
      --producer_votes string             Set comma-separated list of producer IDs
      --seeds string                      Override seeds
      --span_poll_interval string         Set span pull interval
      --syncer_poll_interval string       Set syncer pull interval
      --trace                             print out full stack trace on errors
```
