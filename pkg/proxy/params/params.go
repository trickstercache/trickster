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

// Package params provides support for handling URL Parameters
package params

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// UpdateParams updates the provided query parameters collection with the provided updates
func UpdateParams(params url.Values, updates map[string]string) {
	if params == nil || len(updates) == 0 {
		return
	}
	for k, v := range updates {
		if len(k) == 0 {
			continue
		}
		if k[0:1] == "-" {
			k = k[1:]
			params.Del(k)
			continue
		}
		if k[0:1] == "+" {
			k = k[1:]
			params.Add(k, v)
			continue
		}
		params.Set(k, v)
	}
}

// isMultipartOrForm checks if the request may contain form data.
func isMultipartOrForm(r *http.Request) bool {
	h := r.Header
	if ct := h.Get(headers.NameContentType); strings.Contains(ct, headers.ValueMultipartFormData) {
		return true
	} else if strings.Contains(ct, headers.ValueXFormURLEncoded) {
		return true
	}
	return false
}

// isQueryBody checks if the request body is a query string or actual content.
func isQueryBody(r *http.Request) bool {
	nqbs := []string{
		headers.ValueApplicationCSV, headers.ValueApplicationJSON,
		headers.ValueApplicationFlux,
	}
	ct := r.Header.Get(headers.NameContentType)
	for _, nqb := range nqbs {
		if strings.HasPrefix(ct, nqb) {
			return false
		}
	}
	return true
}

// GetRequestValues returns the Query Parameters for the request
// regardless of method
func GetRequestValues(r *http.Request) (url.Values, []byte, bool) {
	switch {
	case !methods.HasBody(r.Method):
		return r.URL.Query(), []byte(r.URL.RawQuery), false
	case isMultipartOrForm(r):
		// r.ParseMultipartForm doesn't reset the request body, so this handles:
		b, _ := request.GetBody(r)
		r.ParseMultipartForm(10 * 1024 * 1024)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		return r.PostForm, []byte(r.PostForm.Encode()), true
	default:
		v := r.URL.Query()
		b, err := request.GetBody(r)
		if err != nil {
			return v, nil, false
		}
		if !isQueryBody(r) {
			return v, b, true
		}
		if vs, err := url.ParseQuery(string(b)); err == nil && isQueryBody(r) {
			for vsk := range vs {
				for _, vsv := range vs[vsk] {
					if !v.Has(vsk) {
						v.Set(vsk, vsv)
					} else {
						v.Add(vsk, vsv)
					}
				}
			}
		}
		return v, b, true
	}
}

// SetRequestValues Values sets the Query Parameters for the request
// regardless of method
func SetRequestValues(r *http.Request, v url.Values) {
	s := v.Encode()
	if !methods.HasBody(r.Method) {
		r.URL.RawQuery = s
	} else {
		// reset the body
		if r.Body != nil {
			r.Body.Close()
		}
		r.ContentLength = int64(len(s))
		r.Body = io.NopCloser(bytes.NewReader([]byte(s)))
	}
}
