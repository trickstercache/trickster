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

package tsm

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"
)

// RegistryEntry adapts ALB options to the time-series merge constructor.
func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{Name: Name, ShortName: ShortName, New: newFromOptions}
}

func newFromOptions(o *options.Options, factories rt.Lookup) (types.Mechanism, error) {
	if o == nil {
		return nil, errors.ErrInvalidOptions
	}
	return New(Config{
		Options:               o.TSMOptions,
		MaxCaptureBytes:       o.MaxCaptureBytes,
		MaxFanoutCaptureBytes: o.MaxFanoutCaptureBytes,
		OutputFormat:          o.OutputFormat,
	}, factories)
}
