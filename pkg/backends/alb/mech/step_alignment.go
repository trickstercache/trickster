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

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Align returns r carrying the step alignment of the pool or pick it is dispatched through, so no
// swap pairs a membership with another's mode; an outer ALB's mode stays
func Align(r *http.Request, mode timeseries.StepAlignment) *http.Request {
	if mode == 0 || r == nil {
		// inlined, so a request through a pool without a mode costs one comparison
		return r
	}
	return align(r, mode)
}

func align(r *http.Request, mode timeseries.StepAlignment) *http.Request {
	if tctx.StepAlignment(r.Context()) != 0 {
		return r
	}
	return r.WithContext(tctx.WithStepAlignment(r.Context(), mode))
}
