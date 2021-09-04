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

package merge

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/util/copiers"
)

// ResponseGate is a Request/ResponseWriter Pair that must be handled in its entirety
// before its respective response pool can be merged.
type ResponseGate struct {
	http.ResponseWriter
	Request   *http.Request
	Response  *http.Response
	Resources *request.Resources
	body      []byte
	header    http.Header
}

// ResponseGates represents a slice of type *ResponseGate
type ResponseGates []*ResponseGate

// NewResponseGate provides a new ResponseGate object
func NewResponseGate(w http.ResponseWriter, r *http.Request, rsc *request.Resources) *ResponseGate {
	rg := &ResponseGate{ResponseWriter: w, Request: r, Resources: rsc}
	if w != nil {
		rg.header = w.Header().Clone()
	}
	return rg
}

// Header returns the ResponseGate's Header map
func (rg *ResponseGate) Header() http.Header {
	return rg.header
}

// WriteHeader is not used with a ResponseGate
func (rg *ResponseGate) WriteHeader(i int) {
}

// Body returns the stored body for merging
func (rg *ResponseGate) Body() []byte {
	return rg.body
}

// Write is not used with a ResponseGate
func (rg *ResponseGate) Write(b []byte) (int, error) {

	l := len(b)

	if l == 0 {
		return 0, nil
	}

	if rg.body == nil {
		rg.body = copiers.CopyBytes(b)
	} else {
		rg.body = append(rg.body, b...)
	}

	return len(b), nil
}
