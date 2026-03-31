package server

import (
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
)

// ReadConfigFile loads a server config from disk.
func ReadConfigFile(path string) (*Config, error) {
	return readConfigFile(path)
}

// LoadChain loads the chain genesis and network configuration.
func (c *Config) LoadChain() error {
	return c.loadChain()
}

// BuildNode creates a node.Config from the server config.
func (c *Config) BuildNode() (*node.Config, error) {
	return c.buildNode()
}

// BuildEth creates an ethconfig.Config from the server config.
func (c *Config) BuildEth(stack *node.Node, accountManager *accounts.Manager) (*ethconfig.Config, error) {
	return c.buildEth(stack, accountManager)
}

// Backend returns the running ethereum backend.
func (s *Server) Backend() *eth.Ethereum {
	return s.backend
}

// Node returns the running node instance.
func (s *Server) Node() *node.Node {
	return s.node
}
