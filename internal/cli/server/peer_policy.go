package server

import "github.com/ethereum/go-ethereum/internal/cli/flagset"

func (c *Command) peerReputationFlag(f *flagset.Flagset) {
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-reputation",
		Usage:   "Observe peer reputation without changing serving or connection decisions",
		Value:   &c.cliConfig.P2P.PeerReputation,
		Default: c.cliConfig.P2P.PeerReputation,
		Group:   "P2P",
	})
}
