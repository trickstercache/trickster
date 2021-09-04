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

package handlers

import (
	"context"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type redirectKey int

const (
	redirectLocation redirectKey = iota
	redirectCode
)

// HandleRedirectResponse responds to an HTTP Request with a 302 Redirect with
// 30x code and Location header inserted into the context by a previous handler
// Requires redirectCode and redirectLocation have been set
func HandleRedirectResponse(w http.ResponseWriter, r *http.Request) {
	if rc, ok := r.Context().Value(redirectCode).(int); ok {
		if rl, ok := r.Context().Value(redirectLocation).(string); ok {
			w.Header().Set(headers.NameLocation, rl)
			w.WriteHeader(rc)
			return
		}
	}
	w.WriteHeader(http.StatusBadRequest)
	w.Write(nil)
}

// WithRedirects will attach the configured redirect code and location information to
// the provided Context
func WithRedirects(ctx context.Context, rc int, rl string) context.Context {
	return context.WithValue(context.WithValue(ctx, redirectCode, rc), redirectLocation, rl)
}
