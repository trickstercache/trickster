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

package sqlanalyzer

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestQueryPlanRenderExtentRequiresRenderer(t *testing.T) {
	var nilPlan *QueryPlan
	if _, err := nilPlan.RenderExtent(timeseries.Extent{}); !errors.Is(err, ErrMissingRenderer) {
		t.Errorf("nil plan error = %v, want %v", err, ErrMissingRenderer)
	}
	if _, err := (&QueryPlan{}).RenderExtent(timeseries.Extent{}); !errors.Is(err, ErrMissingRenderer) {
		t.Errorf("missing renderer error = %v, want %v", err, ErrMissingRenderer)
	}
}

func TestCacheModeString(t *testing.T) {
	tests := []struct {
		mode CacheMode
		want string
	}{
		{CacheModeNone, "none"},
		{CacheModeObject, "object"},
		{CacheModeDelta, "delta"},
		{CacheMode(255), "unknown"},
	}
	for _, test := range tests {
		if got := test.mode.String(); got != test.want {
			t.Errorf("CacheMode(%d).String() = %q, want %q", test.mode, got, test.want)
		}
	}
}
