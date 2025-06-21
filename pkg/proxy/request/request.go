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

// Package request provides functionality for handling HTTP Requests
// including the insertion of configuration options into the request
package request

import (
	"context"
	"net/http"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

// Clone wraps the builtin Clone to use a deep clone of both the request body
// reader (when present) and the Trickster context data
func Clone(r *http.Request) (*http.Request, error) {
	if r == nil {
		return nil, nil
	}
	rsc := GetResources(r)
	if rsc != nil {
		rsc = rsc.Clone()
	}
	ctx := context.Background()
	if rsc != nil {
		ctx = tctx.WithResources(ctx, rsc.Clone())
	}
	out := r.Clone(context.Background()).WithContext(ctx)
	if r.Method == http.MethodPost || r.Method == http.MethodPut ||
		r.Method == http.MethodPatch {
		br, err := GetBodyReader(r)
		if err != nil {
			return nil, err
		}
		out.Body = br
	}
	return out, nil
}
