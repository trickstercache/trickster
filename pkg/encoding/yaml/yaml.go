/*
 * Copyright 2026 The Trickster Authors
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

// Package yaml provides YAML encoding with Trickster's established formatting.
package yaml

import (
	"bytes"

	goyaml "go.yaml.in/yaml/v3"
)

// Marshal serializes value using the two-space indentation emitted by the
// YAML v2 package used by earlier Trickster releases.
func Marshal(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := goyaml.NewEncoder(&output)
	encoder.CompactSeqIndent()
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
