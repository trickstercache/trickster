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

package gateway

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

func intersectHostnames(listener string, route []string) ([]string, bool) {
	// a wildcard covers any depth
	if listener == "" {
		return translate.Unique(route), true
	}
	if len(route) == 0 {
		return []string{listener}, true
	}
	out := make([]string, 0, len(route))
	for _, h := range route {
		if m, ok := intersect(listener, h); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return translate.Unique(out), true
}

func intersect(listener, route string) (string, bool) {
	if listener == route {
		return route, true
	}
	lw, rw := hostnames.IsWildcard(listener), hostnames.IsWildcard(route)
	switch {
	case lw && rw:
		ls, rs := hostnames.Suffix(listener), hostnames.Suffix(route)
		if strings.HasSuffix(rs, "."+ls) {
			return route, true
		}
		if strings.HasSuffix(ls, "."+rs) {
			return listener, true
		}
	case lw:
		if strings.HasSuffix(route, "."+hostnames.Suffix(listener)) {
			return route, true
		}
	case rw:
		if strings.HasSuffix(listener, "."+hostnames.Suffix(route)) {
			return listener, true
		}
	}
	return "", false
}
