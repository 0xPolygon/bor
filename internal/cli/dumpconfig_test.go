package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPeerIdleDumpConfig(t *testing.T) {
	for _, tc := range []struct {
		name                string
		args                []string
		grace, cooldown     string
		removals, occupancy int
	}{
		{"default", nil, "10m0s", "5m0s", 1, 75},
		{"configured", []string{"--peer-idle-grace-period=2m", "--peer-idle-max-removals=5", "--peer-idle-reconnect-cooldown=7m", "--peer-idle-occupancy-threshold=85"}, "2m0s", "7m0s", 5, 85},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			os.Stdout = output
			t.Cleanup(func() {
				os.Stdout = original
				if err := output.Close(); err != nil {
					t.Error(err)
				}
			})
			if code := (&DumpconfigCommand{}).Run(tc.args); code != 0 {
				t.Fatalf("dumpconfig exit %d", code)
			}
			os.Stdout = original
			var config struct {
				P2P struct {
					Grace     string `toml:"peer-idle-grace-period"`
					Removals  int    `toml:"peer-idle-max-removals"`
					Cooldown  string `toml:"peer-idle-reconnect-cooldown"`
					Occupancy int    `toml:"peer-idle-occupancy-threshold"`
				}
			}
			if _, err := toml.DecodeFile(path, &config); err != nil {
				t.Fatal(err)
			}
			if config.P2P.Grace != tc.grace || config.P2P.Removals != tc.removals || config.P2P.Cooldown != tc.cooldown || config.P2P.Occupancy != tc.occupancy {
				t.Fatalf("exported %+v", config.P2P)
			}
		})
	}
}
