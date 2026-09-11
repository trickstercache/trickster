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

package manager

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sizeconv"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// RotationOptions is the YAML-facing configuration for when a managed log
// file is rotated; nil fields inherit defaults, explicit zeros disable
type RotationOptions struct {
	// Size rotates the log when the live file would exceed this size
	Size *sizeconv.Size `yaml:"size,omitempty"`
	// Interval rotates the log when the live file has been open this long
	Interval *timeconv.Duration `yaml:"interval,omitempty"`
}

// RetentionOptions is the YAML-facing configuration for how many rotated
// archives are kept; nil fields inherit defaults, explicit zeros disable
type RetentionOptions struct {
	// Count is the maximum archived files kept; zero disables count pruning
	Count *int `yaml:"count,omitempty"`
	// Age prunes archived files older than this
	Age *timeconv.Duration `yaml:"age,omitempty"`
}

// Clone returns a copy of the RotationOptions
func (o *RotationOptions) Clone() *RotationOptions {
	if o == nil {
		return nil
	}
	return &RotationOptions{
		Size:     pointers.Clone(o.Size),
		Interval: pointers.Clone(o.Interval),
	}
}

// Clone returns a copy of the RetentionOptions
func (o *RetentionOptions) Clone() *RetentionOptions {
	if o == nil {
		return nil
	}
	return &RetentionOptions{
		Count: pointers.Clone(o.Count),
		Age:   pointers.Clone(o.Age),
	}
}

// ResolveOptions maps YAML-facing rotation/retention/compression settings
// onto a manager Options for the provided filename, applying defaults for
// any nil field
func ResolveOptions(filename string, rot *RotationOptions,
	ret *RetentionOptions, compress *bool,
) *Options {
	o := NewOptions()
	o.Filename = filename
	if rot != nil {
		if rot.Size != nil {
			o.MaxSizeBytes = int64(*rot.Size)
		}
		if rot.Interval != nil {
			o.Interval = time.Duration(*rot.Interval)
		}
	}
	if ret != nil {
		if ret.Count != nil {
			o.RetentionCount = *ret.Count
		}
		if ret.Age != nil {
			o.RetentionAge = time.Duration(*ret.Age)
		}
	}
	if compress != nil {
		o.Compress = *compress
	}
	return o
}
