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

// Package hostnames normalizes and classifies the hostnames Trickster routes
// on: precise names, one-label wildcards (*.example.com) and any-depth
// wildcards (**.example.com). Every component that accepts a hostname from
// configuration, a certificate or a Kubernetes object validates it here.
package hostnames

import (
	"errors"
	"net"
	"strings"
)

const (
	// Wildcard marks a hostname whose first label matches exactly one label
	Wildcard = "*."
	// AnyDepth marks a hostname whose leading labels match one or more labels
	AnyDepth = "**."
)

// Mode selects what Normalize accepts beyond a precise hostname
type Mode uint8

const (
	// AllowEmpty accepts an empty hostname, which means every hostname
	AllowEmpty Mode = iota
	// RequireHost rejects an empty hostname
	RequireHost
	// RequirePrecise rejects an empty hostname and any wildcard
	RequirePrecise
)

var (
	ErrEmpty      = errors.New("hostname is empty")
	ErrWhitespace = errors.New("hostname contains whitespace")
	ErrWildcard   = errors.New("a wildcard may only be a leading *. or **. label followed by a domain")
	ErrPrecise    = errors.New("hostname must name one host")
)

// Normalize lowercases and trims a hostname, drops a trailing dot, and
// rejects one the router cannot register under the given mode
func Normalize(h string, mode Mode) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" {
		if mode == AllowEmpty {
			return "", nil
		}
		return "", ErrEmpty
	}
	if strings.ContainsAny(h, " \t") {
		return "", ErrWhitespace
	}
	suffix, wild := cutWildcard(h)
	switch {
	case wild && mode == RequirePrecise:
		return "", ErrPrecise
	case wild && (suffix == "" || suffix[0] == '.' || strings.Contains(suffix, "*")):
		return "", ErrWildcard
	case !wild && strings.Contains(h, "*"):
		return "", ErrWildcard
	}
	return h, nil
}

func cutWildcard(h string) (string, bool) {
	if s, ok := strings.CutPrefix(h, AnyDepth); ok {
		return s, true
	}
	return strings.CutPrefix(h, Wildcard)
}

// IsWildcard reports whether the hostname is a wildcard of either depth
func IsWildcard(h string) bool {
	return strings.HasPrefix(h, Wildcard) || strings.HasPrefix(h, AnyDepth)
}

// IsAnyDepth reports whether the hostname is an any-depth wildcard
func IsAnyDepth(h string) bool {
	return strings.HasPrefix(h, AnyDepth)
}

// Suffix returns the domain a wildcard stands under, and a precise hostname unchanged
func Suffix(h string) string {
	s, _ := cutWildcard(h)
	return s
}

// ToAnyDepth returns the any-depth spelling of a one-label wildcard, and any
// other hostname unchanged
func ToAnyDepth(h string) string {
	if s, ok := strings.CutPrefix(h, Wildcard); ok {
		return AnyDepth + s
	}
	return h
}

// reservedTLD is the top-level domain reserved never to resolve
const reservedTLD = ".invalid"

// Reserved reports whether a host, or the host of a host:port address, is under the reserved
// .invalid domain and so can never be resolved or connected to.
func Reserved(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return strings.HasSuffix(host, reservedTLD)
}
