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

package pgwire

import (
	"errors"
	"fmt"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

const classRoute = "route"

type routeTable struct {
	mtx      sync.Mutex
	resolver backends.RouteResolver
	targets  map[string]*Server
}

// NewRoutedServer returns a server that authenticates each client itself and then runs the
// session on the target backend a User Router picks for the startup user.
func NewRoutedServer(config Config, resolver backends.RouteResolver, targets map[string]Config) (*Server, error) {
	if resolver == nil {
		return nil, errors.New("postgres routed server requires a route resolver")
	}
	if len(targets) == 0 {
		return nil, errors.New("postgres routed server has no targets")
	}
	if !config.Terminated() {
		return nil, errors.New("postgres routed server has no listener-facing users")
	}
	// the router relays nothing itself, so it inspects no statements
	config.Analyzer = nil
	server, err := newServer(config)
	if err != nil {
		return nil, err
	}
	server.routes = &routeTable{resolver: resolver, targets: make(map[string]*Server, len(targets))}
	for name, target := range targets {
		if target.Upstream.User == "" {
			return nil, fmt.Errorf("postgres route target %q has no origin credentials", name)
		}
		// clients were authenticated by the router; the target only logs in to its origin
		target.Users = nil
		if server.routes.targets[name], err = NewServer(target); err != nil {
			return nil, fmt.Errorf("postgres route target %q: %w", name, err)
		}
	}
	return server, nil
}

// UpdateRouteResolver switches the resolver used for new sessions; established
// sessions keep the target they were given.
func (s *Server) UpdateRouteResolver(resolver backends.RouteResolver) {
	if s.routes == nil || resolver == nil {
		return
	}
	s.routes.mtx.Lock()
	s.routes.resolver = resolver
	s.routes.mtx.Unlock()
}

func (s *session) route() bool {
	// picks the authenticated client's target once, for the life of the session.
	routes, router := s.front.routes, s.front.config.BackendName
	routes.mtx.Lock()
	resolver := routes.resolver
	routes.mtx.Unlock()
	decision, resolved := resolver.ResolveRoute(backends.RouteInput{
		RouterName: router, Username: s.user, Authenticated: true,
	})
	var target *Server
	if resolved && decision.Target.Available() {
		target = routes.targets[decision.Target.Backend.Name()]
	}
	if target == nil {
		outcome := decision.Outcome
		if outcome == "" || outcome == backends.RouteOutcomeSelected || outcome == backends.RouteOutcomeDefault {
			outcome = backends.RouteOutcomeNoRoute
		}
		metrics.PGWireRouteSelections.WithLabelValues(router, "", string(outcome)).Inc()
		s.front.countError(classRoute)
		s.fatal(sqlstateInvalidAuthSpec, fmt.Sprintf("no available route for user %q", s.user))
		return false
	}
	if decision.Outcome == "" {
		decision.Outcome = backends.RouteOutcomeSelected
	}
	metrics.PGWireRouteSelections.WithLabelValues(router, target.config.BackendName, string(decision.Outcome)).Inc()
	s.server = target
	if target.config.Analyzer != nil {
		s.tracker = newSessionTracker(s.user, s.database, s.params, s.server.config.Engine)
	}
	return true
}
