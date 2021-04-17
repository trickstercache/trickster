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

// Package methods provides functionality for handling HTTP methods
package methods

import "net/http"

const (
	get uint16 = 1 << iota
	head
	post
	put
	patch
	delete
	options
	connect
	trace
	purge
)

const (
	cacheableMethods   = get + head
	bodyMethods        = post + put + patch
	uncacheableMethods = bodyMethods + delete + options + connect + trace + purge
	allMethods         = cacheableMethods + uncacheableMethods
)

const (
	// Methods not currently in the base golang http package

	// MethodPurge is the PURGE HTTP Method
	MethodPurge = "PURGE"
)

var methodsMap = map[string]uint16{
	http.MethodGet:     get,
	http.MethodHead:    head,
	http.MethodPost:    post,
	http.MethodPut:     put,
	http.MethodPatch:   patch,
	http.MethodDelete:  delete,
	http.MethodOptions: options,
	http.MethodConnect: connect,
	http.MethodTrace:   trace,
	MethodPurge:        purge,
}

// AllHTTPMethods returns a list of all known HTTP methods
func AllHTTPMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch, MethodPurge}
}

// GetAndPost returns a string slice containing "GET" and "POST"
func GetAndPost() []string {
	return []string{http.MethodGet, http.MethodPost}
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

// IsCacheable returns true if the method is HEAD or GET
func IsCacheable(method string) bool {
	if m, ok := methodsMap[method]; ok {
		return (cacheableMethods&m != 0)
	}
	return false
}

// HasBody returns true if the method is POST, PUT or PATCH
func HasBody(method string) bool {
	if m, ok := methodsMap[method]; ok {
		return (bodyMethods&m != 0)
	}
	return false
}

// MethodMask returns the integer representation of the collection of methods
// based on the iota bitmask defined above
func MethodMask(methods ...string) uint16 {
	var i uint16
	for _, ms := range methods {
		if m, ok := methodsMap[ms]; ok {
			i ^= m
		}
	}
	return i
}
