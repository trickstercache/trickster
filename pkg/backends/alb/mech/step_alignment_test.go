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

package mech

import (
	"net/http"
	"net/http/httptest"
	"testing"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestAlign(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	truncate := &tctx.StepAlignmentOverride{Mode: timeseries.StepAlignmentTruncate}
	if Align(r, nil) != r || Align(nil, truncate) != nil {
		t.Fatal("a pool without a mode leaves the request as it is")
	}
	if got := tctx.StepAlignmentOverrideOf(Align(r, truncate).Context()); got != truncate {
		t.Fatalf("got %v", got)
	}
	// an ALB pooling this one chose first
	outer := r.WithContext(tctx.WithStepAlignment(r.Context(), timeseries.StepAlignmentOff))
	if Align(outer, truncate) != outer {
		t.Fatal("the outer ALB's mode was replaced")
	}
}
