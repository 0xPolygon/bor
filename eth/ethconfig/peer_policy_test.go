package ethconfig

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/naoina/toml"
)

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
