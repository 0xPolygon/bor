package configs

import (
	_ "embed"
	"github.com/protolambda/zrnt/eth2/beacon/common"
)

//go:embed yamls/presets/minimal/phase0.yaml
var minimalPhase0Preset []byte

//go:embed yamls/presets/minimal/altair.yaml
var minimalAltairPreset []byte

//go:embed yamls/presets/minimal/bellatrix.yaml
var minimalBellatrixPreset []byte

//go:embed yamls/presets/minimal/capella.yaml
var minimalCapellaPreset []byte

//go:embed yamls/presets/minimal/deneb.yaml
var minimalDenebPreset []byte

//go:embed yamls/presets/minimal/electra.yaml
var minimalElectraPreset []byte

//go:embed yamls/configs/minimal.yaml
var minimalConfig []byte

var Minimal = &common.Spec{
	Phase0Preset:    mustYAML[common.Phase0Preset](minimalPhase0Preset),
	AltairPreset:    mustYAML[common.AltairPreset](minimalAltairPreset),
	BellatrixPreset: mustYAML[common.BellatrixPreset](minimalBellatrixPreset),
	CapellaPreset:   mustYAML[common.CapellaPreset](minimalCapellaPreset),
	DenebPreset:     mustYAML[common.DenebPreset](minimalDenebPreset),
	ElectraPreset:   mustYAML[common.ElectraPreset](minimalElectraPreset),
	Config:          mustYAML[common.Config](minimalConfig),
	ExecutionEngine: nil,
}
