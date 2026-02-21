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

package headers

import (
	"net/http"
	"strings"
)

// HasHeaderValue returns true if r's Header map contains a key matching name
// with a value that starts with val (case insensitive)
func HasHeaderValue(r *http.Request, name, val string) bool {
	if r == nil || len(r.Header) == 0 {
		return false
	}
	headerVal := r.Header.Get(name)
	if len(headerVal) < len(val) {
		return false
	}
	return strings.EqualFold(headerVal[:len(val)], val)
}

// AcceptsContentType returns true if r has an Accept header of contentType
func AcceptsContentType(r *http.Request, contentType string) bool {
	return HasHeaderValue(r, NameAccept, contentType)
}

// ProvidesContentType returns true if r has a Content-Type header of contentType
func ProvidesContentType(r *http.Request, contentType string) bool {
	return HasHeaderValue(r, NameContentType, contentType)
}

// AcceptsJSON returns true if r has an Accept: application/json header
func AcceptsJSON(r *http.Request) bool {
	return AcceptsContentType(r, ValueApplicationJSON)
}

// AcceptsYAML returns true if r has an Accept: application/yaml header
func AcceptsYAML(r *http.Request) bool {
	return AcceptsContentType(r, ValueApplicationYAML)
}

// AcceptsCSV returns true if r has an Accept: application/csv header
func AcceptsCSV(r *http.Request) bool {
	return AcceptsContentType(r, ValueApplicationCSV)
}

//

// ProvidesJSON returns true if r has a Content-Type: application/json header
func ProvidesJSON(r *http.Request) bool {
	return ProvidesContentType(r, ValueApplicationJSON)
}

// ProvidesURLEncodedForm returns true if r has a
// Content-Type: application/x-www-form-urlencoded header
func ProvidesURLEncodedForm(r *http.Request) bool {
	return ProvidesContentType(r, ValueXFormURLEncoded)
}
