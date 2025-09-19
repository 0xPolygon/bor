# Bor Overview
Bor is the official Golang implementation of the Polygon PoS blockchain. It is a fork of [geth](https://github.com/ethereum/go-ethereum) and is EVM compatible (upto London fork).

[![API Reference](https://pkg.go.dev/badge/github.com/0xPolygon/bor)](https://pkg.go.dev/github.com/0xPolygon/bor)
[![Go Report Card](https://goreportcard.com/badge/github.com/0xPolygon/bor)](https://goreportcard.com/report/github.com/0xPolygon/bor)
![MIT License](https://img.shields.io/github/license/0xPolygon/bor)
[![Discord](https://img.shields.io/badge/discord-join%20chat-blue.svg)](https://discord.com/invite/0xpolygonrnd)
[![Twitter Follow](https://img.shields.io/twitter/follow/0xPolygon.svg?style=social)](https://twitter.com/0xPolygon)

### Installing bor using packaging

The easiest way to get started with bor is to install the packages using the command below. Please take a look at the [releases](https://github.com/0xPolygon/bor/releases) section to find the latest stable version of bor.
    
    curl -L https://raw.githubusercontent.com/0xPolygon/install/main/bor.sh | bash -s -- v2.0.0 <network> <node_type>

The network accepts `mainnet`, or `amoy` and the node type accepts `validator` or `sentry` or `archive`. The installation script does the following things:
- Create a new user named `bor`.
- Install the bor binary at `/usr/bin/bor`.
- Dump the suitable config file (based on the network and node type provided) at `/var/lib/bor` and use it as the home dir.
- Create a systemd service named `bor` at `/lib/systemd/system/bor.service` which starts bor using the config file as `bor` user.

The releases supports both the networks i.e. Polygon Mainnet, and Amoy (Testnet) unless explicitly specified. Before the stable release for mainnet, pre-releases will be available marked with `beta` tag for deploying on Amoy (testnet). On sufficient testing, stable release for mainnet will be announced with a forum post.

### Building from source

- Install Go (version 1.19 or later) and a C compiler.
- Clone the repository and build the binary using the following commands:
    ```shell
    make bor
    ```
- Start bor using the ideal config files for the validator and sentry provided in the `packaging` folder.
    ```shell
    ./build/bin/bor server --config ./packaging/templates/mainnet-v1/sentry/sentry/bor/config.toml
    ```
- To build full set of utilities, run:
    ```shell
    make all
    ```
- Run unit and integration tests
    ```shell
    make test && make test-integration
    ```

#### Using the new cli

Post `v0.3.0` release, bor uses a new command line interface (cli). The new-cli (located at `internal/cli`) has been built while keeping the flag usage similar to old-cli (located at `cmd/geth`) with a few notable changes. Please refer to [docs](./docs) section for the flag usage guide and example.

### Documentation

- The official documentation for the Polygon PoS chain can be found [here](https://wiki.polygon.technology/docs/pos/getting-started/). It contains all the conceptual and architectural details of the chain along with an operational guide for users running the nodes.
- New release announcements and discussions can be found on our [forum page](https://forum.polygon.technology/).
- Polygon improvement proposals can be found [here](https://github.com/maticnetwork/Polygon-Improvement-Proposals/)

### Contribution guidelines

Thank you for considering helping out with the source code! We welcome contributions from anyone on the internet, and are grateful for even the smallest of fixes! If you'd like to contribute to bor, please fork, fix, commit, and send a pull request for the maintainers to review and merge into the main code base. 

From the outset, we defined some guidelines to ensure new contributions only ever enhance the project:

* Quality: Code in the Polygon project should meet the style guidelines, with sufficient test-cases, descriptive commit messages, evidence that the contribution does not break any compatibility commitments or cause adverse feature interactions, and evidence of high-quality peer-review. Code must adhere to the official Go [formatting](https://golang.org/doc/effective_go.html#formatting) guidelines (i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
* Testing: Please ensure that the updated code passes all the tests locally before submitting a pull request. In order to run unit tests, run `make test` and to run integration tests, run `make test-integration`.
* Size: The Polygon project’s culture is one of small pull-requests, regularly submitted. The larger a pull-request, the more likely it is that you will be asked to resubmit as a series of self-contained and individually reviewable smaller PRs.
* Maintainability: If the feature will require ongoing maintenance (e.g. support for a particular brand of database), we may ask you to accept responsibility for maintaining this feature
* Pull requests need to be based on and opened against the `develop` branch.
* PR title should be prefixed with package(s) they modify.
  * E.g. "eth, rpc: make trace configs optional"


### Hardware Requirements

Minimum:

* CPU with 4+ cores
* 8GB RAM
* 1TB free storage space to sync the Mainnet
* 8 MBit/sec download Internet service

Recommended:

* Fast CPU with 8+ cores
* 16GB+ RAM
* High-performance SSD with at least 1TB of free space
* 25+ MBit/sec download Internet service

### Full node on the main Ethereum network

By far the most common scenario is people wanting to simply interact with the Ethereum
network: create accounts; transfer funds; deploy and interact with contracts. For this
particular use case, the user doesn't care about years-old historical data, so we can
sync quickly to the current state of the network. To do so:

```shell
$ geth console
```

This command will:
 * Start `geth` in snap sync mode (default, can be changed with the `--syncmode` flag),
   causing it to download more data in exchange for avoiding processing the entire history
   of the Ethereum network, which is very CPU intensive.
 * Start the built-in interactive [JavaScript console](https://geth.ethereum.org/docs/interacting-with-geth/javascript-console),
   (via the trailing `console` subcommand) through which you can interact using [`web3` methods](https://github.com/ChainSafe/web3.js/blob/0.20.7/DOCUMENTATION.md) 
   (note: the `web3` version bundled within `geth` is very old, and not up to date with official docs),
   as well as `geth`'s own [management APIs](https://geth.ethereum.org/docs/interacting-with-geth/rpc).
   This tool is optional and if you leave it out you can always attach it to an already running
   `geth` instance with `geth attach`.

### A Full node on the Holesky test network

Transitioning towards developers, if you'd like to play around with creating Ethereum
contracts, you almost certainly would like to do that without any real money involved until
you get the hang of the entire system. In other words, instead of attaching to the main
network, you want to join the **test** network with your node, which is fully equivalent to
the main network, but with play-Ether only.

```shell
$ geth --holesky console
```

The `console` subcommand has the same meaning as above and is equally
useful on the testnet too.

Specifying the `--holesky` flag, however, will reconfigure your `geth` instance a bit:

 * Instead of connecting to the main Ethereum network, the client will connect to the Holesky 
   test network, which uses different P2P bootnodes, different network IDs and genesis
   states.
 * Instead of using the default data directory (`~/.ethereum` on Linux for example), `geth`
   will nest itself one level deeper into a `holesky` subfolder (`~/.ethereum/holesky` on
   Linux). Note, on OSX and Linux this also means that attaching to a running testnet node
   requires the use of a custom endpoint since `geth attach` will try to attach to a
   production node endpoint by default, e.g.,
   `geth attach <datadir>/holesky/geth.ipc`. Windows users are not affected by
   this.

*Note: Although some internal protective measures prevent transactions from
crossing over between the main network and test network, you should always
use separate accounts for play and real money. Unless you manually move
accounts, `geth` will by default correctly separate the two networks and will not make any
accounts available between them.*

### Configuration

As an alternative to passing the numerous flags to the `geth` binary, you can also pass a
configuration file via:

```shell
$ geth --config /path/to/your_config.toml
```

To get an idea of how the file should look like you can use the `dumpconfig` subcommand to
export your existing configuration:

```shell
$ geth --your-favourite-flags dumpconfig
```

#### Docker quick start

One of the quickest ways to get Ethereum up and running on your machine is by using
Docker:

```shell
docker run -d --name ethereum-node -v /Users/alice/ethereum:/root \
           -p 8545:8545 -p 30303:30303 \
           ethereum/client-go
```

This will start `geth` in snap-sync mode with a DB memory allowance of 1GB, as the
above command does.  It will also create a persistent volume in your home directory for
saving your blockchain as well as map the default ports. There is also an `alpine` tag
available for a slim version of the image.

Do not forget `--http.addr 0.0.0.0`, if you want to access RPC from other containers
and/or hosts. By default, `geth` binds to the local interface and RPC endpoints are not
accessible from the outside.

### Programmatically interfacing `geth` nodes

As a developer, sooner rather than later you'll want to start interacting with `geth` and the
Ethereum network via your own programs and not manually through the console. To aid
this, `geth` has built-in support for a JSON-RPC based APIs ([standard APIs](https://ethereum.org/en/developers/docs/apis/json-rpc/)
and [`geth` specific APIs](https://geth.ethereum.org/docs/interacting-with-geth/rpc)).
These can be exposed via HTTP, WebSockets and IPC (UNIX sockets on UNIX based
platforms, and named pipes on Windows).

The IPC interface is enabled by default and exposes all the APIs supported by `geth`,
whereas the HTTP and WS interfaces need to manually be enabled and only expose a
subset of APIs due to security reasons. These can be turned on/off and configured as
you'd expect.

HTTP based JSON-RPC API options:

  * `--http` Enable the HTTP-RPC server
  * `--http.addr` HTTP-RPC server listening interface (default: `localhost`)
  * `--http.port` HTTP-RPC server listening port (default: `8545`)
  * `--http.api` API's offered over the HTTP-RPC interface (default: `eth,net,web3`)
  * `--http.corsdomain` Comma separated list of domains from which to accept cross-origin requests (browser enforced)
  * `--ws` Enable the WS-RPC server
  * `--ws.addr` WS-RPC server listening interface (default: `localhost`)
  * `--ws.port` WS-RPC server listening port (default: `8546`)
  * `--ws.api` API's offered over the WS-RPC interface (default: `eth,net,web3`)
  * `--ws.origins` Origins from which to accept WebSocket requests
  * `--ipcdisable` Disable the IPC-RPC server
  * `--ipcpath` Filename for IPC socket/pipe within the datadir (explicit paths escape it)

You'll need to use your own programming environments' capabilities (libraries, tools, etc) to
connect via HTTP, WS or IPC to a `geth` node configured with the above flags and you'll
need to speak [JSON-RPC](https://www.jsonrpc.org/specification) on all transports. You
can reuse the same connection for multiple requests!

**Note: Please understand the security implications of opening up an HTTP/WS based
transport before doing so! Hackers on the internet are actively trying to subvert
Ethereum nodes with exposed APIs! Further, all browser tabs can access locally
running web servers, so malicious web pages could try to subvert locally available
APIs!**

### Operating a private network

Maintaining your own private network is more involved as a lot of configurations taken for
granted in the official networks need to be manually set up.

Unfortunately since [the Merge](https://ethereum.org/en/roadmap/merge/) it is no longer possible
to easily set up a network of geth nodes without also setting up a corresponding beacon chain.

There are three different solutions depending on your use case:

  * If you are looking for a simple way to test smart contracts from go in your CI, you can use the [Simulated Backend](https://geth.ethereum.org/docs/developers/dapp-developer/native-bindings#blockchain-simulator).
  * If you want a convenient single node environment for testing, you can use our [Dev Mode](https://geth.ethereum.org/docs/developers/dapp-developer/dev-mode).
  * If you are looking for a multiple node test network, you can set one up quite easily with [Kurtosis](https://geth.ethereum.org/docs/fundamentals/kurtosis).

## Contribution

Thank you for considering helping out with the source code! We welcome contributions
from anyone on the internet, and are grateful for even the smallest of fixes!

If you'd like to contribute to go-ethereum, please fork, fix, commit and send a pull request
for the maintainers to review and merge into the main code base. If you wish to submit
more complex changes though, please check up with the core devs first on [our Discord Server](https://discord.gg/invite/nthXNEv)
to ensure those changes are in line with the general philosophy of the project and/or get
some early feedback which can make both your efforts much lighter as well as our review
and merge procedures quick and simple.

Please make sure your contributions adhere to our coding guidelines:

 * Code must adhere to the official Go [formatting](https://golang.org/doc/effective_go.html#formatting)
   guidelines (i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
 * Code must be documented adhering to the official Go [commentary](https://golang.org/doc/effective_go.html#commentary)
   guidelines.
 * Pull requests need to be based on and opened against the `master` branch.
 * Commit messages should be prefixed with the package(s) they modify.
   * E.g. "eth, rpc: make trace configs optional"

Please see the [Developers' Guide](https://geth.ethereum.org/docs/developers/geth-developer/dev-guide)
for more details on configuring your environment, managing project dependencies, and
testing procedures.

### Contributing to geth.ethereum.org

For contributions to the [go-ethereum website](https://geth.ethereum.org), please checkout and raise pull requests against the `website` branch.
For more detailed instructions please see the `website` branch [README](https://github.com/ethereum/go-ethereum/tree/website#readme) or the 
[contributing](https://geth.ethereum.org/docs/developers/geth-developer/contributing) page of the website.

## License

The go-ethereum library (i.e. all code outside of the `cmd` directory) is licensed under the
[GNU Lesser General Public License v3.0](https://www.gnu.org/licenses/lgpl-3.0.en.html),
also included in our repository in the `COPYING.LESSER` file.

The go-ethereum binaries (i.e. all code inside of the `cmd` directory) are licensed under the
[GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html), also
included in our repository in the `COPYING` file.

## Join our Discord server

Join Polygon community  – share your ideas or just say hi over on [Polygon Community Discord](https://discord.com/invite/0xPolygonCommunity) or on [Polygon R&D Discord](https://discord.com/invite/0xpolygonrnd).
