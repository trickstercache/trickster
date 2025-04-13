/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
