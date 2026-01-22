package server

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/ethereum/go-ethereum/log"
)

func readLegacyConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	tomlData := string(data)

	if err != nil {
		return nil, fmt.Errorf("failed to read toml config file: %v", err)
	}

	conf := *DefaultConfig()

	meta, err := toml.Decode(tomlData, &conf)
	if err != nil {
		return nil, fmt.Errorf("failed to decode toml config file: %v", err)
	}

	for _, key := range meta.Undecoded() {
		log.Warn("Unrecognised config value", "key", key.String())
	}

	if err := conf.fillBigInt(); err != nil {
		return nil, err
	}

	if err := conf.fillTimeDurations(); err != nil {
		return nil, err
	}

	return &conf, nil
}
