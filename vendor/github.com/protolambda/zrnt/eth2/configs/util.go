package configs

import (
	"gopkg.in/yaml.v3"
)

func mustYAML[T any](data []byte) T {
	var elem T
	if err := yaml.Unmarshal(data, &elem); err != nil {
		panic(err)
	}
	return elem
}
