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

// WithStepAlignment returns ctx carrying a step alignment mode that overrides the backend's own, so
// an ALB can apply one mode to every pool member
func WithStepAlignment(ctx context.Context, mode timeseries.StepAlignment) context.Context {
	return context.WithValue(ctx, stepAlignmentKey, mode)
}

// StepAlignment returns the step alignment mode ctx carries, or zero when it carries none
func StepAlignment(ctx context.Context) timeseries.StepAlignment {
	if ctx == nil {
		return 0
	}
	mode, _ := ctx.Value(stepAlignmentKey).(timeseries.StepAlignment)
	return mode
}
