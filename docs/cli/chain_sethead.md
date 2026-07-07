# Chain sethead

The ```chain sethead <number>``` command sets the current chain to a certain block.

## Arguments

- ```number```: The block number to roll back.

## Options

- ```address```: Address of the grpc endpoint (default: 127.0.0.1:3131)

- ```token```: Bearer token to authenticate with the bor gRPC server (matches --grpc.token on the server). Falls back to the BOR_GRPC_TOKEN environment variable when unset.

- ```yes```: Force set head (default: false)