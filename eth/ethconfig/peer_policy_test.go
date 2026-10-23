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
	if Defaults.PeerReputationPerPeerMetrics {
		t.Fatal("per-peer metrics must default to disabled")
	}
	config := Defaults
	if err := toml.NewDecoder(bytes.NewBufferString("NetworkId = 137\n")).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if !config.PeerReputation {
		t.Fatal("omitted field disabled reputation")
	}
	if config.PeerReputationPerPeerMetrics {
		t.Fatal("omitted field enabled per-peer metrics")
	}
}

func TestPeerPolicyTOMLRoundTrip(t *testing.T) {
	// Match the nested Eth table and field naming used by cmd/geth.
	codec := toml.Config{
		NormFieldName: func(_ reflect.Type, key string) string { return key },
		FieldToKey:    func(_ reflect.Type, field string) string { return field },
	}
	for _, tc := range []struct{ enabled, perPeer bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("enabled=%t/per-peer=%t", tc.enabled, tc.perPeer), func(t *testing.T) {
			config := struct{ Eth Config }{Eth: Defaults}
			config.Eth.PeerReputation = !tc.enabled
			config.Eth.PeerReputationPerPeerMetrics = !tc.perPeer
			input := fmt.Sprintf("[Eth]\nPeerReputation = %t\nPeerReputationPerPeerMetrics = %t\n", tc.enabled, tc.perPeer)
			if err := codec.NewDecoder(bytes.NewBufferString(input)).Decode(&config); err != nil {
				t.Fatal(err)
			}
			if config.Eth.PeerReputation != tc.enabled || config.Eth.PeerReputationPerPeerMetrics != tc.perPeer {
				t.Fatalf("decoded reputation=%t, per-peer metrics=%t, want %+v", config.Eth.PeerReputation, config.Eth.PeerReputationPerPeerMetrics, tc)
			}
			encoded, err := codec.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			restored := struct{ Eth Config }{Eth: Defaults}
			restored.Eth.PeerReputation = !tc.enabled
			restored.Eth.PeerReputationPerPeerMetrics = !tc.perPeer
			if err := codec.NewDecoder(bytes.NewReader(encoded)).Decode(&restored); err != nil {
				t.Fatal(err)
			}
			if restored.Eth.PeerReputation != tc.enabled || restored.Eth.PeerReputationPerPeerMetrics != tc.perPeer {
				t.Fatalf("round trip reputation=%t, per-peer metrics=%t, want %+v", restored.Eth.PeerReputation, restored.Eth.PeerReputationPerPeerMetrics, tc)
			}
		})
	}
}
