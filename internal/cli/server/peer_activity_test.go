package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mitchellh/cli"

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

func TestPeerIdlePolicySettings(t *testing.T) {
	for _, tc := range []struct {
		name, config        string
		args                []string
		grace, cooldown     time.Duration
		removals, occupancy int
	}{
		{"defaults", "", nil, 10 * time.Minute, 5 * time.Minute, 1, 75},
		{"flags", "", []string{"--peer-idle-grace-period=4m", "--peer-idle-max-removals=5", "--peer-idle-reconnect-cooldown=90s", "--peer-idle-occupancy-threshold=80"}, 4 * time.Minute, 90 * time.Second, 5, 80},
		{"limits", "", []string{"--peer-idle-grace-period=2m", "--peer-idle-max-removals=64", "--peer-idle-reconnect-cooldown=1s", "--peer-idle-occupancy-threshold=1"}, 2 * time.Minute, time.Second, 64, 1},
		{"config", "[p2p]\npeer-idle-grace-period = '3m'\npeer-idle-max-removals = 7\npeer-idle-reconnect-cooldown = '7m'\npeer-idle-occupancy-threshold = 85\n", nil, 3 * time.Minute, 7 * time.Minute, 7, 85},
		{"override", "[p2p]\npeer-idle-grace-period = '3m'\npeer-idle-max-removals = 7\npeer-idle-reconnect-cooldown = '7m'\npeer-idle-occupancy-threshold = 85\n", []string{"--peer-idle-grace-period=2m", "--peer-idle-max-removals=2", "--peer-idle-reconnect-cooldown=24h", "--peer-idle-occupancy-threshold=100"}, 2 * time.Minute, 24 * time.Hour, 2, 100},
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
			if node.P2P.PeerIdleGracePeriod != tc.grace || node.P2P.PeerIdleMaxRemovals != tc.removals || node.P2P.PeerIdleReconnectCooldown != tc.cooldown || node.P2P.PeerIdleOccupancyThreshold != tc.occupancy {
				t.Fatalf("grace=%v removals=%d cooldown=%v occupancy=%d", node.P2P.PeerIdleGracePeriod, node.P2P.PeerIdleMaxRemovals, node.P2P.PeerIdleReconnectCooldown, node.P2P.PeerIdleOccupancyThreshold)
			}
		})
	}
}

func TestPeerIdleSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		grace    time.Duration
		removals int
	}{
		{0, 1}, {-time.Minute, 1}, {time.Second, 1}, {2*time.Minute - time.Nanosecond, 1},
		{2 * time.Minute, 0}, {2 * time.Minute, -1}, {2 * time.Minute, 65},
	} {
		config := DefaultConfig()
		config.P2P.PeerIdleGracePeriod, config.P2P.PeerIdleMaxRemovals = tc.grace, tc.removals
		if _, err := config.buildNode(log.Root()); err == nil {
			t.Fatalf("accepted grace=%v removals=%d", tc.grace, tc.removals)
		}
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
		if config.P2P.PeerIdleGracePeriod != 10*time.Minute || config.P2P.PeerIdleMaxRemovals != 1 || config.P2P.PeerIdleReconnectCooldown != 5*time.Minute || config.P2P.PeerIdleOccupancyThreshold != 75 {
			t.Fatal("HCL omitted fields lost idle policy defaults")
		}
	}
}

func TestPeerIdleSettingsHCL(t *testing.T) {
	config := "gpo {}\nminer {}\njsonrpc {\nhttp {}\nws {}\ntimeouts {}\n}\np2p {\npeer-idle-grace-period = \"3m\"\npeer-idle-max-removals = 5\npeer-idle-reconnect-cooldown = \"7m\"\npeer-idle-occupancy-threshold = 85\n}\n"
	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.P2P.PeerIdleGracePeriod != 3*time.Minute || got.P2P.PeerIdleMaxRemovals != 5 || got.P2P.PeerIdleReconnectCooldown != 7*time.Minute || got.P2P.PeerIdleOccupancyThreshold != 85 {
		t.Fatal("HCL idle policy settings were not decoded")
	}
}

func TestPeerIdleCooldownValidation(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "500ms", "24h1ns", "invalid"} {
		t.Run(value, func(t *testing.T) {
			cmd := &Command{UI: &cli.BasicUi{Writer: io.Discard, ErrorWriter: io.Discard}}
			err := cmd.extractFlags([]string{"--peer-idle-reconnect-cooldown=" + value})
			if err == nil {
				_, err = cmd.cliConfig.buildNode(log.Root())
			}
			if err == nil {
				t.Fatal("invalid cooldown was accepted")
			}
		})
	}
}

func TestPeerIdleOccupancyValidation(t *testing.T) {
	for _, value := range []string{"0", "-1", "101", "75.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			cmd := &Command{UI: &cli.BasicUi{Writer: io.Discard, ErrorWriter: io.Discard}}
			err := cmd.extractFlags([]string{"--peer-idle-occupancy-threshold=" + value})
			if err == nil {
				_, err = cmd.cliConfig.buildNode(log.Root())
			}
			if err == nil {
				t.Fatal("invalid occupancy threshold was accepted")
			}
		})
	}
}
