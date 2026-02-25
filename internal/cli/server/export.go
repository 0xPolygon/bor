package server

import (
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/node"
)

// ReadConfigFile loads a server config from disk.
func ReadConfigFile(path string) (*Config, error) {
	return readConfigFile(path)
}

// Backend returns the running ethereum backend.
func (s *Server) Backend() *eth.Ethereum {
	return s.backend
}

// Node returns the running node instance.
func (s *Server) Node() *node.Node {
	return s.node
}
