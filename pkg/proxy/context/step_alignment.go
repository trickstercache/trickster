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

package context

import (
	"context"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// StepAlignmentOverride is the step alignment an ALB applies to its pool members: Mode, unless a
// query's directive names one of Allowed, the modes every member supports
type StepAlignmentOverride struct {
	Mode, Allowed timeseries.StepAlignment
}

// Requested returns the mode a member resolves for a query whose directive names directive (zero for
// none): the directive when o is nil or allows it, else o's Mode
func (o *StepAlignmentOverride) Requested(directive timeseries.StepAlignment) timeseries.StepAlignment {
	if o == nil || o.Mode == 0 || (directive != 0 && o.Allowed&directive != 0) {
		return directive
	}
	return o.Mode
}

// WithStepAlignmentOverride returns ctx carrying o, so an ALB can apply one mode to every pool member
func WithStepAlignmentOverride(ctx context.Context, o *StepAlignmentOverride) context.Context {
	return context.WithValue(ctx, stepAlignmentKey, o)
}

// WithStepAlignment returns ctx carrying mode as an override that only a directive naming mode matches
func WithStepAlignment(ctx context.Context, mode timeseries.StepAlignment) context.Context {
	return WithStepAlignmentOverride(ctx, &StepAlignmentOverride{Mode: mode, Allowed: mode})
}

// StepAlignmentOverrideOf returns the override ctx carries, or nil when it carries none
func StepAlignmentOverrideOf(ctx context.Context) *StepAlignmentOverride {
	if ctx == nil {
		return nil
	}
	o, _ := ctx.Value(stepAlignmentKey).(*StepAlignmentOverride)
	return o
}

// StepAlignment returns the mode ctx's override applies, or zero when it carries none
func StepAlignment(ctx context.Context) timeseries.StepAlignment {
	if o := StepAlignmentOverrideOf(ctx); o != nil {
		return o.Mode
	}
	return 0
}
