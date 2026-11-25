//go:build !lls_shadow

package eth

import (
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
)

// attachShadow is a no-op outside -tags lls_shadow builds.
func (s *Ethereum) attachShadow(*node.Node, *ethconfig.Config) error { return nil }
