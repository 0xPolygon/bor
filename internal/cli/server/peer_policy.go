package server

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/p2p"
)

func (c *Command) peerReputationFlag(f *flagset.Flagset) {
	f.DurationFlag(&flagset.DurationFlag{
		Name: "peer-idle-grace-period", Usage: "Grace period for peer rotation (minimum 2m; request-only peers require at least 10m)",
		Value: &c.cliConfig.P2P.PeerIdleGracePeriod, Default: c.cliConfig.P2P.PeerIdleGracePeriod, Group: "P2P",
	})
	f.DurationFlag(&flagset.DurationFlag{
		Name: "peer-idle-reconnect-cooldown", Usage: "Reconnect cooldown after activity-based peer removal (1s-24h)",
		Value: &c.cliConfig.P2P.PeerIdleReconnectCooldown, Default: c.cliConfig.P2P.PeerIdleReconnectCooldown, Group: "P2P",
	})
	f.IntFlag(&flagset.IntFlag{
		Name: "peer-idle-occupancy-threshold", Usage: "Peer slot occupancy percentage required to continue rotation (1-100; request-only batches must start at full capacity after a refusal)",
		Value: &c.cliConfig.P2P.PeerIdleOccupancyThreshold, Default: c.cliConfig.P2P.PeerIdleOccupancyThreshold, Group: "P2P",
	})
	f.IntFlag(&flagset.IntFlag{
		Name: "peer-idle-max-removals", Usage: "Maximum peers rotated per check (1-64); request-only batches are separated by at least the grace period or 10m, whichever is longer",
		Value: &c.cliConfig.P2P.PeerIdleMaxRemovals, Default: c.cliConfig.P2P.PeerIdleMaxRemovals, Group: "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-idle-eviction",
		Usage:   "Rotate silent peers under slot pressure and request-only inbound peers after capacity refusals at full slots (requires peer reputation; disable with --peer-idle-eviction=false)",
		Value:   &c.cliConfig.P2P.PeerIdleEviction,
		Default: c.cliConfig.P2P.PeerIdleEviction,
		Group:   "P2P",
	})
	f.BoolFlag(&flagset.BoolFlag{
		Name:    "peer-reputation",
		Usage:   "Track peer activity and observe reputation scores (disable to also stop activity-based peer rotation)",
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
	if c.PeerIdleGracePeriod < 2*time.Minute {
		return fmt.Errorf("peer-idle-grace-period must be at least 2m")
	}
	if c.PeerIdleMaxRemovals < 1 || c.PeerIdleMaxRemovals > p2p.MaxPeerIdleRemovals {
		return fmt.Errorf("peer-idle-max-removals must be between 1 and %d", p2p.MaxPeerIdleRemovals)
	}
	if c.PeerIdleReconnectCooldown < time.Second || c.PeerIdleReconnectCooldown > p2p.MaxPeerIdleReconnectCooldown {
		return fmt.Errorf("peer-idle-reconnect-cooldown must be between 1s and %s", p2p.MaxPeerIdleReconnectCooldown)
	}
	if c.PeerIdleOccupancyThreshold < 1 || c.PeerIdleOccupancyThreshold > 100 {
		return fmt.Errorf("peer-idle-occupancy-threshold must be between 1 and 100")
	}
	cfg.PeerIdleOccupancyThreshold = c.PeerIdleOccupancyThreshold
	cfg.PeerIdleReconnectCooldown = c.PeerIdleReconnectCooldown
	cfg.PeerIdleGracePeriod = c.PeerIdleGracePeriod
	cfg.PeerIdleMaxRemovals = c.PeerIdleMaxRemovals
	return nil
}
