package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := DefaultConfig()
		config.P2P.PeerReputation = enabled
		config.P2P.PeerReputationPerPeerMetrics = enabled
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
		if ethConfig.PeerReputationPerPeerMetrics != enabled {
			t.Fatal("per-peer metrics setting lost")
		}
	}
}

func TestPeerPolicyDefaultStartup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		args    []string
		enabled bool
		perPeer bool
	}{
		{"default", "", nil, true, false},
		{"disabled-flag", "", []string{"--peer-reputation=false"}, false, false},
		{"old-config", "[p2p]\nmaxpeers = 50\n", nil, true, false},
		{"disabled-config", "[p2p]\npeer-reputation = false\n", nil, false, false},
		{"flag-overrides-config", "[p2p]\npeer-reputation = false\n", []string{"--peer-reputation=true"}, true, false},
		{"per-peer-flag", "", []string{"--peer-reputation-per-peer-metrics"}, true, true},
		{"per-peer-config", "[p2p]\npeer-reputation-per-peer-metrics = true\n", nil, true, true},
		{"per-peer-flag-overrides-config", "[p2p]\npeer-reputation-per-peer-metrics = true\n", []string{"--peer-reputation-per-peer-metrics=false"}, true, false},
		{"per-peer-with-scoring-disabled", "", []string{"--peer-reputation=false", "--peer-reputation-per-peer-metrics"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config *Config
			if tc.config != "" {
				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				config, err = readConfigFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd := &Command{}
			if err := cmd.Flags(config).Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if err := cmd.cliConfig.loadChain(); err != nil {
				t.Fatal(err)
			}
			ethConfig, err := cmd.cliConfig.buildEth(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			require.Equal(t, tc.enabled, ethConfig.PeerReputation, "peer reputation")
			require.Equal(t, tc.perPeer, ethConfig.PeerReputationPerPeerMetrics, "per-peer metrics")
		})
	}
}

func TestPeerPolicyHCLDefault(t *testing.T) {
	const base = "gpo {}\nminer {}\njsonrpc {\nhttp {}\nws {}\ntimeouts {}\n}\n"
	for _, tc := range []struct {
		name, p2p string
		enabled   bool
		perPeer   bool
	}{
		{"omitted-block", "", true, false},
		{"omitted-field", "p2p {}\n", true, false},
		{"disabled", "p2p { peer-reputation = false }\n", false, false},
		{"per-peer-enabled", "p2p { peer-reputation-per-peer-metrics = true }\n", true, true},
		{"per-peer-disabled", "p2p { peer-reputation-per-peer-metrics = false }\n", true, false},
		{"per-peer-with-scoring-disabled", "p2p {\npeer-reputation = false\npeer-reputation-per-peer-metrics = true\n}\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.hcl")
			if err := os.WriteFile(path, []byte(base+tc.p2p), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := readConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if config.P2P.PeerReputation != tc.enabled {
				t.Fatalf("enabled = %v, want %v", config.P2P.PeerReputation, tc.enabled)
			}
			if config.P2P.PeerReputationPerPeerMetrics != tc.perPeer {
				t.Fatalf("per-peer metrics = %v, want %v", config.P2P.PeerReputationPerPeerMetrics, tc.perPeer)
			}
		})
	}
}

func TestPeerPolicyTOMLRoundTrip(t *testing.T) {
	for _, tc := range []struct{ enabled, perPeer bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("enabled=%t/per-peer=%t", tc.enabled, tc.perPeer), func(t *testing.T) {
			config := struct{ P2P P2PConfig }{P2P: P2PConfig{
				PeerReputation: tc.enabled, PeerReputationPerPeerMetrics: tc.perPeer,
			}}
			var encoded bytes.Buffer
			if err := toml.NewEncoder(&encoded).Encode(config); err != nil {
				t.Fatal(err)
			}
			restored := struct{ P2P P2PConfig }{P2P: P2PConfig{
				PeerReputation: !tc.enabled, PeerReputationPerPeerMetrics: !tc.perPeer,
			}}
			if _, err := toml.Decode(encoded.String(), &restored); err != nil {
				t.Fatal(err)
			}
			if restored.P2P.PeerReputation != tc.enabled || restored.P2P.PeerReputationPerPeerMetrics != tc.perPeer {
				t.Fatalf("round trip reputation=%t, per-peer metrics=%t, want %+v", restored.P2P.PeerReputation, restored.P2P.PeerReputationPerPeerMetrics, tc)
			}
		})
	}
}
