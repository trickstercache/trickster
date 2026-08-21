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

// Package options provides the Graphite-specific backend options
package options

import (
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// Options stores information about Graphite Options. Fields are added as the
// decisions that shape them are recorded in
// trickster-data/todos/graphite-backend-implementation.md (Phase 0); the
// proxy-only provider needs none.
type Options struct{}

// New returns a new Graphite Options with default values
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	return pointers.Clone(o)
}

// UnmarshalYAML decodes the Options over a set of defaults
func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
