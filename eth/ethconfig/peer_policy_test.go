package ethconfig

import (
	"bytes"
	"fmt"
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
}

func TestPeerPolicyTOMLRoundTrip(t *testing.T) {
	// Match the nested Eth table and field naming used by cmd/geth.
	codec := toml.Config{
		NormFieldName: func(_ reflect.Type, key string) string { return key },
		FieldToKey:    func(_ reflect.Type, field string) string { return field },
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			config := struct{ Eth Config }{Eth: Defaults}
			config.Eth.PeerReputation = !enabled
			input := fmt.Sprintf("[Eth]\nPeerReputation = %t\n", enabled)
			if err := codec.NewDecoder(bytes.NewBufferString(input)).Decode(&config); err != nil {
				t.Fatal(err)
			}
			if config.Eth.PeerReputation != enabled {
				t.Fatalf("decoded PeerReputation = %t, want %t", config.Eth.PeerReputation, enabled)
			}
			encoded, err := codec.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			restored := struct{ Eth Config }{Eth: Defaults}
			restored.Eth.PeerReputation = !enabled
			if err := codec.NewDecoder(bytes.NewReader(encoded)).Decode(&restored); err != nil {
				t.Fatal(err)
			}
			if restored.Eth.PeerReputation != enabled {
				t.Fatalf("round trip PeerReputation = %t, want %t", restored.Eth.PeerReputation, enabled)
			}
		})
	}
}
