# RemoveDB

The ```bor removedb``` command will remove the blockchain and state databases at the given datadir location

## Options

- ```address```: Address of the grpc endpoint (default: 127.0.0.1:3131)

- ```datadir```: Path of the data directory to store information

- ```token```: Bearer token to authenticate with the bor gRPC server (matches --grpc.token on the server). Falls back to the BOR_GRPC_TOKEN environment variable when unset.