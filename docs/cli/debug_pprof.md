# Debug Pprof

The ```debug pprof <enode>``` command will create an archive containing bor pprof traces.

## Options

- ```address```: Address of the grpc endpoint (default: 127.0.0.1:3131)

- ```output```: Output directory

- ```seconds```: seconds to profile (default: 2)

- ```skiptrace```: Skip running the trace (default: false)

- ```token```: Bearer token to authenticate with the bor gRPC server (matches --grpc.token on the server). Falls back to the BOR_GRPC_TOKEN environment variable when unset.