package wit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetWitnessMetadataRequest_Name(t *testing.T) {
	req := &GetWitnessMetadataRequest{}
	assert.Equal(t, "GetWitnessMetadata", req.Name())
}

func TestGetWitnessMetadataRequest_Kind(t *testing.T) {
	req := &GetWitnessMetadataRequest{}
	assert.Equal(t, byte(GetWitnessMetadataMsg), req.Kind())
}

func TestWitnessMetadataPacket_Name(t *testing.T) {
	packet := &WitnessMetadataPacket{}
	assert.Equal(t, "WitnessMetadata", packet.Name())
}

func TestWitnessMetadataPacket_Kind(t *testing.T) {
	packet := &WitnessMetadataPacket{}
	assert.Equal(t, byte(WitnessMetadataMsg), packet.Kind())
}
