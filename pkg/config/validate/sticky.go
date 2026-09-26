/*
 * Copyright 2026 The Trickster Authors
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

package validate

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

// issuesTokens reports whether an ALB's sticky mode is set to one that issues tokens, which only
// an http listener can carry
func issuesTokens(o *ao.Options) bool {
	return o.Sticky != nil && (o.Sticky.Mode == so.ModeCookie || o.Sticky.Mode == so.ModeHeader)
}

// stickyOnRequests holds an ALB's sticky block to an http listener, which reads a table key from
// each request when the ALB keeps its sessions in a table there
func stickyOnRequests(backendName, listenerName string, o *ao.Options) error {
	if o.Sticky == nil || o.Sticky.ModeFor(true) != so.ModeTable || o.Sticky.Table.KeySource.OnHTTP() {
		return nil
	}
	return fmt.Errorf("alb backend %q: sticky.table.key %q cannot be read from a request, which http "+
		"listener %q serves", backendName, o.Sticky.Table.Key, listenerName)
}

// stickyOnFlows holds an ALB's sticky block to a stream or native listener, which keeps sessions
// only in a table, by a key that readable reports it can read
func stickyOnFlows(listenerName, protocol, backendName string, o *ao.Options, readable func() bool) error {
	switch {
	case o.Sticky == nil:
		return nil
	case issuesTokens(o):
		return fmt.Errorf("listener %q with protocol %q cannot carry the tokens of alb backend %q's "+
			"sticky.mode %q; leave sticky.mode unset to keep sessions in a table there", listenerName,
			protocol, backendName, o.Sticky.Mode)
	case !readable():
		return fmt.Errorf("listener %q with protocol %q cannot read alb backend %q's sticky.table.key %q",
			listenerName, protocol, backendName, o.Sticky.Table.Key)
	}
	return nil
}

// cookieID is what a browser tells cookies apart by, on one listener
type cookieID struct {
	listener, name, domain, path string
}

// stickyCookies refuses two ALBs that set the same cookie on one http listener: in a browser,
// each one's token would replace the other's, and neither session would last.
func stickyCookies(c *config.Config) error {
	owners := make(map[cookieID]string)
	for _, backendName := range slices.Sorted(maps.Keys(c.Backends)) {
		backend := c.Backends[backendName]
		if backend == nil || backend.Provider != providers.ALB || backend.ALBOptions == nil {
			continue
		}
		s := backend.ALBOptions.Sticky
		if s == nil || s.ModeFor(true) != so.ModeCookie {
			continue
		}
		for _, listenerName := range httpListenerNames(c, backend) {
			id := cookieID{listenerName, s.Cookie.Name, strings.ToLower(s.Cookie.Domain), s.Cookie.Path}
			if other, ok := owners[id]; ok {
				return fmt.Errorf("alb backends %q and %q both set sticky cookie %q on http listener %q; "+
					"give one of them its own sticky.cookie.name", other, backendName, s.Cookie.Name,
					listenerName)
			}
			owners[id] = backendName
		}
	}
	return nil
}
