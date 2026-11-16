package server

import "github.com/ethereum/go-ethereum/internal/cli/flagset"

func (c *Command) peerReputationFlag(f *flagset.Flagset) {
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-idle-eviction",
		Usage:   "Rotate peers with no protocol traffic for ten minutes when total or directional slots are nearly full (disable with --peer-idle-eviction=false)",
		Value:   &c.cliConfig.P2P.PeerIdleEviction,
		Default: c.cliConfig.P2P.PeerIdleEviction,
		Group:   "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-reputation",
		Usage:   "Observe peer reputation without changing serving or connection decisions (disable with --peer-reputation=false)",
		Value:   &c.cliConfig.P2P.PeerReputation,
		Default: c.cliConfig.P2P.PeerReputation,
		Group:   "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-reputation-per-peer-metrics",
		Usage:   "Export peer reputation metrics labeled by peer ID (requires metrics and peer reputation)",
		Value:   &c.cliConfig.P2P.PeerReputationPerPeerMetrics,
		Default: c.cliConfig.P2P.PeerReputationPerPeerMetrics,
		Group:   "P2P",
	})
}
