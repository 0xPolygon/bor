package server

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestPeerPolicyDefaultStartup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		args    []string
		enabled bool
	}{
		{"default", "", nil, true},
		{"disabled-flag", "", []string{"--peer-reputation=false"}, false},
		{"old-config", "[p2p]\nmaxpeers = 50\n", nil, true},
		{"disabled-config", "[p2p]\npeer-reputation = false\n", nil, false},
		{"flag-overrides-config", "[p2p]\npeer-reputation = false\n", []string{"--peer-reputation=true"}, true},
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
			if ethConfig.PeerReputation != tc.enabled {
				t.Fatalf("enabled = %v, want %v", ethConfig.PeerReputation, tc.enabled)
			}
		})
	}
}

func TestPeerPolicyHCLDefault(t *testing.T) {
	const base = "gpo {}\nminer {}\njsonrpc {\nhttp {}\nws {}\ntimeouts {}\n}\n"
	for _, tc := range []struct {
		name, p2p string
		enabled   bool
	}{
		{"omitted-block", "", true},
		{"omitted-field", "p2p {}\n", true},
		{"disabled", "p2p { peer-reputation = false }\n", false},
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
		})
	}
}
