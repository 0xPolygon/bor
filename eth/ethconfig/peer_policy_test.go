package ethconfig

import (
	"bytes"
	"reflect"
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
	if err := toml.NewDecoder(bytes.NewBufferString("PeerReputation = false\n")).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if config.PeerReputation {
		t.Fatal("explicit false did not disable reputation")
	}
}

func TestPeerPolicyConfigRoundTrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		encoded, err := (Config{PeerReputation: enabled}).MarshalTOML()
		if err != nil {
			t.Fatal(err)
		}
		if reflect.ValueOf(encoded).Elem().FieldByName("PeerReputation").Bool() != enabled {
			t.Fatal("missing marshaled setting")
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(struct{ PeerReputation bool }{enabled}); err != nil {
			t.Fatal(err)
		}
		var decoded Config
		if err := toml.NewDecoder(&buf).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.PeerReputation != enabled {
			t.Fatal("peer reputation lost on round trip")
		}
	}
}
