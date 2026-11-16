package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/log"
)

func TestPeerIdleEvictionConfig(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		args         []string
		want         bool
	}{
		{"default", "", nil, true},
		{"disabled-flag", "", []string{"--peer-idle-eviction=false"}, false},
		{"disabled-config", "[p2p]\npeer-idle-eviction = false\n", nil, false},
		{"flag-override", "[p2p]\npeer-idle-eviction = false\n", []string{"--peer-idle-eviction"}, true},
		{"disable-configured-policy", "[p2p]\npeer-idle-eviction = true\n", []string{"--peer-idle-eviction=false"}, false},
		{"reputation-disabled", "", []string{"--peer-reputation=false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if tc.config != "" {
				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append([]string{"--config", path}, args...)
			}
			cmd := &Command{}
			if err := cmd.extractFlags(args); err != nil {
				t.Fatal(err)
			}
			node, err := cmd.cliConfig.buildNode(log.Root())
			if err != nil {
				t.Fatal(err)
			}
			if node.P2P.PeerIdleEviction != tc.want {
				t.Fatalf("PeerIdleEviction = %v, want %v", node.P2P.PeerIdleEviction, tc.want)
			}
		})
	}
}

func TestPeerIdleEvictionHCL(t *testing.T) {
	const base = "gpo {}\nminer {}\njsonrpc {\nhttp {}\nws {}\ntimeouts {}\n}\n"
	for _, tc := range []struct {
		config string
		want   bool
	}{{"", true}, {"p2p {}\n", true}, {"p2p { peer-idle-eviction = false }\n", false}} {
		path := filepath.Join(t.TempDir(), "config.hcl")
		if err := os.WriteFile(path, []byte(base+tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
		config, err := readConfigFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if config.P2P.PeerIdleEviction != tc.want {
			t.Fatalf("PeerIdleEviction = %v, want %v", config.P2P.PeerIdleEviction, tc.want)
		}
	}
}
