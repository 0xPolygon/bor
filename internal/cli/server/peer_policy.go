package server

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/p2p"
)

func (c *Command) peerReputationFlag(f *flagset.Flagset) {
	f.DurationFlag(&flagset.DurationFlag{
		Name: "peer-idle-grace-period", Usage: "Grace period before a silent peer becomes eligible for rotation",
		Value: &c.cliConfig.P2P.PeerIdleGracePeriod, Default: c.cliConfig.P2P.PeerIdleGracePeriod, Group: "P2P",
	})
	f.DurationFlag(&flagset.DurationFlag{
		Name: "peer-idle-reconnect-cooldown", Usage: "Reconnect cooldown after silent-peer removal (1s-24h)",
		Value: &c.cliConfig.P2P.PeerIdleReconnectCooldown, Default: c.cliConfig.P2P.PeerIdleReconnectCooldown, Group: "P2P",
	})
	f.IntFlag(&flagset.IntFlag{
		Name: "peer-idle-max-removals", Usage: "Maximum idle peers removed per one-minute check (1-64)",
		Value: &c.cliConfig.P2P.PeerIdleMaxRemovals, Default: c.cliConfig.P2P.PeerIdleMaxRemovals, Group: "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-idle-eviction",
		Usage:   "Rotate inactive peers when slots are nearly full (requires peer reputation; disable with --peer-idle-eviction=false)",
		Value:   &c.cliConfig.P2P.PeerIdleEviction,
		Default: c.cliConfig.P2P.PeerIdleEviction,
		Group:   "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-reputation",
		Usage:   "Track peer activity and observe reputation scores (disable to also stop idle-peer eviction)",
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

func (c *P2PConfig) applyPeerIdle(cfg *p2p.Config) error {
	if c.PeerIdleGracePeriod < time.Second {
		return fmt.Errorf("peer-idle-grace-period must be at least 1s")
	}
	if c.PeerIdleMaxRemovals < 1 || c.PeerIdleMaxRemovals > p2p.MaxPeerIdleRemovals {
		return fmt.Errorf("peer-idle-max-removals must be between 1 and %d", p2p.MaxPeerIdleRemovals)
	}
	if c.PeerIdleReconnectCooldown < time.Second || c.PeerIdleReconnectCooldown > p2p.MaxPeerIdleReconnectCooldown {
		return fmt.Errorf("peer-idle-reconnect-cooldown must be between 1s and %s", p2p.MaxPeerIdleReconnectCooldown)
	}
	cfg.PeerIdleReconnectCooldown = c.PeerIdleReconnectCooldown
	cfg.PeerIdleGracePeriod = c.PeerIdleGracePeriod
	cfg.PeerIdleMaxRemovals = c.PeerIdleMaxRemovals
	return nil
}
