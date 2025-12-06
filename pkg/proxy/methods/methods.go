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

import (
	"net/http"
	"strings"
)

const (
	get uint16 = 1 << iota
	head
	post
	put
	patch
	del
	options
	connect
	trace
	purge
)

const (
	cacheableMethods = get + head
	bodyMethods      = post + put + patch
)

const (
	// Methods not currently in the base golang http package

	// MethodPurge is the PURGE HTTP Method
	MethodPurge = "PURGE"
)

func getMethodLogicalID(method string) uint16 {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return get
	case http.MethodHead:
		return head
	case http.MethodPost:
		return post
	case http.MethodPut:
		return put
	case http.MethodOptions:
		return options
	case http.MethodPatch:
		return patch
	case http.MethodDelete:
		return del
	case http.MethodConnect:
		return connect
	case http.MethodTrace:
		return trace
	case MethodPurge:
		return purge
	}
	return 0
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
	if m := getMethodLogicalID(method); m > 0 {
		return (cacheableMethods&m != 0)
	}
	return false
}

// HasBody returns true if the method is POST, PUT or PATCH
func HasBody(method string) bool {
	if m := getMethodLogicalID(method); m > 0 {
		return (bodyMethods&m != 0)
	}
	return false
}

// MethodMask returns the integer representation of the collection of methods
// based on the iota bitmask defined above
func MethodMask(methods ...string) uint16 {
	var i uint16
	for _, ms := range methods {
		if m := getMethodLogicalID(ms); m > 0 {
			i ^= m
		}
	}
	return i
}

// IsValidMethod returns true if the provided method is recognized in methodsMap
func IsValidMethod(method string) bool {
	return getMethodLogicalID(method) > 0
}

func AreEqual(l1, l2 []string) bool {
	if len(l1) != len(l2) {
		return false
	}
	return MethodMask(l1...) == MethodMask(l2...)
}

// HasAll returns true if methods2 contains all methods from methods1
func HasAll(methods1, methods2 []string) bool {
	if len(methods1) == 0 {
		return true
	}
	if len(methods2) == 0 {
		return false
	}
	mask1 := MethodMask(methods1...)
	mask2 := MethodMask(methods2...)
	return (mask1 & mask2) == mask1
}

// HasAny returns true if methods2 contains any methods from methods1
func HasAny(methods1, methods2 []string) bool {
	if len(methods1) == 0 || len(methods2) == 0 {
		return false
	}
	mask1 := MethodMask(methods1...)
	mask2 := MethodMask(methods2...)
	return (mask1 & mask2) != 0
}
