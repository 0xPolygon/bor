package server

import "testing"

func TestPeerPolicyConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := DefaultConfig()
		config.P2P.PeerReputation = enabled
		if err := config.loadChain(); err != nil {
			t.Fatal(err)
		}
		ethConfig, err := config.buildEth(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ethConfig.PeerReputation != enabled {
			t.Fatal("peer reputation setting lost")
		}
	}
}
