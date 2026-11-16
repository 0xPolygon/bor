package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPeerIdleDumpConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		grace    string
		removals int
	}{
		{"default", nil, "2m0s", 1},
		{"configured", []string{"--peer-idle-grace-period=10m", "--peer-idle-max-removals=5"}, "10m0s", 5},
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
					Grace    string `toml:"peer-idle-grace-period"`
					Removals int    `toml:"peer-idle-max-removals"`
				}
			}
			if _, err := toml.DecodeFile(path, &config); err != nil {
				t.Fatal(err)
			}
			if config.P2P.Grace != tc.grace || config.P2P.Removals != tc.removals {
				t.Fatalf("exported %+v", config.P2P)
			}
		})
	}
}
