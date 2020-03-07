/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package methods provides functionality for handling HTTP methods
package methods

import "net/http"

const (

	// Methods not currently in the base golang http package

	// MethodPurge is the PURGE HTTP Method
	MethodPurge = "PURGE"
)

// AllHTTPMethods returns a list of all known HTTP methods
func AllHTTPMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch, MethodPurge}
}

// CacheableHTTPMethods returns a list of HTTP methods that are generally considered cacheable
func CacheableHTTPMethods() []string {
	return []string{http.MethodGet, http.MethodHead}
}

// UncacheableHTTPMethods returns a list of HTTP methods that are generally considered uncacheable
func UncacheableHTTPMethods() []string {
	return []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace, http.MethodPatch, MethodPurge}
}
