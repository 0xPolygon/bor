# Debug trace

The ```bor debug block <number>``` command will create an archive containing traces of a bor block.

## Options

- ```address```: Address of the grpc endpoint (default: 127.0.0.1:3131)

- ```output```: Output directory

- ```token```: Bearer token to authenticate with the bor gRPC server (matches --grpc.token on the server). Falls back to the BOR_GRPC_TOKEN environment variable when unset.