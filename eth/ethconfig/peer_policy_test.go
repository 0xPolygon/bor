package ethconfig

import (
	"bytes"
	"testing"

	"github.com/naoina/toml"
)

func TestPeerPolicyDefault(t *testing.T) {
	if !Defaults.PeerReputation {
		t.Fatal("peer reputation must default to enabled")
	}
	config := Defaults
	if err := toml.NewDecoder(bytes.NewBufferString("NetworkId = 137\n")).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if !config.PeerReputation {
		t.Fatal("omitted field disabled reputation")
	}
}
