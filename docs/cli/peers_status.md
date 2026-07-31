# Peers status

The ```peers status <peer id>``` command displays the status of a peer by its id.

## Options

- ```address```: Address of the grpc endpoint (default: 127.0.0.1:3131)

- ```token```: Bearer token to authenticate with the bor gRPC server (matches --grpc.token on the server). Falls back to the BOR_GRPC_TOKEN environment variable when unset.