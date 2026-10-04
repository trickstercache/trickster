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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestStepAlignment(t *testing.T) {
	//nolint:staticcheck // a nil context must be tolerated
	if got := StepAlignment(nil); got != 0 {
		t.Errorf("nil context: got %s", got)
	}
	ctx := context.Background()
	if got := StepAlignment(ctx); got != 0 {
		t.Errorf("no override: got %s", got)
	}
	ctx = WithStepAlignment(ctx, timeseries.StepAlignmentOff)
	if got := StepAlignment(ctx); got != timeseries.StepAlignmentOff {
		t.Errorf("override: got %s", got)
	}
}

func TestStepAlignmentOverrideRequested(t *testing.T) {
	const drop, off, truncate = timeseries.StepAlignmentDrop, timeseries.StepAlignmentOff,
		timeseries.StepAlignmentTruncate
	var none *StepAlignmentOverride
	o := &StepAlignmentOverride{Mode: truncate, Allowed: truncate | drop}
	for _, test := range []struct {
		name      string
		o         *StepAlignmentOverride
		directive timeseries.StepAlignment
		want      timeseries.StepAlignment
	}{
		{"no override, no directive", none, 0, 0},
		{"no override", none, off, off},
		{"an override without a mode", &StepAlignmentOverride{Allowed: drop}, off, off},
		{"no directive", o, 0, truncate},
		{"a directive every member supports", o, drop, drop},
		{"a directive a member lacks", o, off, truncate},
	} {
		if got := test.o.Requested(test.directive); got != test.want {
			t.Errorf("%s: got %s", test.name, got)
		}
	}
	ctx := WithStepAlignmentOverride(context.Background(), o)
	if StepAlignmentOverrideOf(ctx) != o || StepAlignment(ctx) != truncate {
		t.Fatal("the override was not carried")
	}
	//nolint:staticcheck // a nil context must be tolerated
	if StepAlignmentOverrideOf(nil) != nil {
		t.Fatal("a nil context carries no override")
	}
}
