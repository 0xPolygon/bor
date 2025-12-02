package configs

import (
	_ "embed"
	"github.com/protolambda/zrnt/eth2/beacon/common"
)

//go:embed yamls/presets/mainnet/phase0.yaml
var mainnetPhase0Preset []byte

//go:embed yamls/presets/mainnet/altair.yaml
var mainnetAltairPreset []byte

//go:embed yamls/presets/mainnet/bellatrix.yaml
var mainnetBellatrixPreset []byte

//go:embed yamls/presets/mainnet/capella.yaml
var mainnetCapellaPreset []byte

//go:embed yamls/presets/mainnet/deneb.yaml
var mainnetDenebPreset []byte

//go:embed yamls/presets/mainnet/electra.yaml
var mainnetElectraPreset []byte

//go:embed yamls/configs/mainnet.yaml
var mainnetConfig []byte

var Mainnet = &common.Spec{
	Phase0Preset:    mustYAML[common.Phase0Preset](mainnetPhase0Preset),
	AltairPreset:    mustYAML[common.AltairPreset](mainnetAltairPreset),
	BellatrixPreset: mustYAML[common.BellatrixPreset](mainnetBellatrixPreset),
	CapellaPreset:   mustYAML[common.CapellaPreset](mainnetCapellaPreset),
	DenebPreset:     mustYAML[common.DenebPreset](mainnetDenebPreset),
	ElectraPreset:   mustYAML[common.ElectraPreset](mainnetElectraPreset),
	Config:          mustYAML[common.Config](mainnetConfig),
	ExecutionEngine: nil,
}
