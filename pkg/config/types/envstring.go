package types

import (
	"os"

	"gopkg.in/yaml.v2"
)

// EnvString is a string that should automatically have any environment variable references
// expanded as it is decoded from YAML. For example, if the YAML contains
//
//	foo: ${BAR}
//
// then the value of foo will be the value of the BAR environment variable.
type EnvString string

func (s *EnvString) Unmarshal(data []byte) error {
	if err := yaml.Unmarshal(data, s); err != nil {
		return err
	}
	if len(*s) != 0 {
		*s = EnvString(os.ExpandEnv(string(*s)))
	}
	return nil
}

// EnvStringMap is a map of strings that should automatically expand environment variables
// as it is decoded from YAML (like EnvString).
type EnvStringMap map[string]string

func (s *EnvStringMap) Unmarshal(data []byte) error {
	if err := yaml.Unmarshal(data, s); err != nil {
		return err
	}
	for k, v := range *s {
		(*s)[k] = os.ExpandEnv(v)
	}
	return nil
}
