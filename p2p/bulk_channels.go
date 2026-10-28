package p2p

import "fmt"

// bulkChannels is the allowlist of sidecar lanes, mapped to the devp2p
// protocol that must be negotiated before a peer may open one. Keep it in sync
// with the ETH, SNAP and WIT routing tables; the QUIC stream limit and the
// per-session channel cap are both derived from its size, so adding a lane here
// is all that is needed.
var bulkChannels = map[string]string{
	"eth-control":   "eth",
	"eth-blocks":    "eth",
	"eth-tx":        "eth",
	"eth-tx-fetch":  "eth",
	"eth-bulk":      "eth",
	"snap-accounts": "snap",
	"snap-storage":  "snap",
	"snap-code":     "snap",
	"snap-trie":     "snap",
	"wit-bulk":      "wit",
}

func bulkChannelProtocol(channel string) string {
	return bulkChannels[channel]
}

func validateBulkChannel(peer *Peer, channel string) error {
	protocol := bulkChannelProtocol(channel)
	if protocol == "" {
		return fmt.Errorf("unsupported bulk channel %q", channel)
	}
	if peer == nil || peer.running[protocol] == nil {
		return fmt.Errorf("bulk channel protocol %q not negotiated", protocol)
	}
	select {
	case <-peer.Done():
		return errBulkSidecarNoPeer
	default:
		return nil
	}
}
