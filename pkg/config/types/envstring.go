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
