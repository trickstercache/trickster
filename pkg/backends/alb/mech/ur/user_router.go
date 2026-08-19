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

package ur

import (
	"maps"
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const URName types.Name = "user_router"

// UserRoute holds runtime state for one configured user mapping.
type UserRoute = backends.RouteTarget

// UserRoutes maps usernames to their resolved runtime route state.
type UserRoutes map[string]UserRoute

type Handler struct {
	// mu guards runtime handler state against concurrent
	// reads on the request path and writes during SIGHUP config reload via
	// ValidateAndStartPool.
	mu                 sync.RWMutex
	authenticator      at.Authenticator
	routerName         string
	defaultTarget      backends.RouteTarget
	defaultHandler     http.Handler
	enableReplaceCreds bool
	noRouteStatusCode  int
	options            *uropt.Options
	userRoutes         UserRoutes
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{
		Name:      URName,
		ShortName: names.MechanismUR,
		New:       New,
	}
}

func New(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	if o == nil || o.UserRouter == nil {
		return nil, errors.ErrInvalidOptions
	}
	out := &Handler{
		noRouteStatusCode: o.UserRouter.NoRouteStatusCode,
		options:           o.UserRouter,
	}
	return out, nil
}

func (h *Handler) Name() types.Name {
	return names.MechanismUR
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	auth := h.authenticator
	routerName := h.routerName
	h.mu.RUnlock()

	var username, cred string
	var authenticated bool

	rsc := request.GetResources(r)
	// this checks if an authenticator has already handled the request, and if
	// so, uses the Authenticator data. Otherwise, it asks the backend-provider-
	// default authenticator (usually Basic Auth) for the username.
	if rsc != nil && rsc.AuthResult != nil && rsc.AuthResult.Username != "" {
		username = rsc.AuthResult.Username
		authenticated = rsc.AuthResult.Status == at.AuthSuccess
	} else if auth != nil {
		u, c, err := auth.ExtractCredentials(r)
		if err == nil && u != "" {
			username = u
			cred = c
			// enableReplaceCreds remains false since credentials were not verified
		}
	}
	decision, ok := h.ResolveRoute(backends.RouteInput{
		RouterName: routerName, Username: username, Credential: cred, Authenticated: authenticated,
	})
	if !ok {
		h.handleDefault(w, r)
		return
	}
	if decision.ReplaceCredentials && auth != nil {
		auth.Sanitize(r)
		if err := auth.SetCredentials(r, decision.OutboundUsername,
			decision.OutboundCredential); err != nil {
			h.handleDefault(w, r)
			return
		}
	}
	if decision.Target.Backend == nil || decision.Target.Backend.Router() == nil {
		h.handleDefault(w, r)
		return
	}
	decision.Target.Backend.Router().ServeHTTP(w, r)
}

// ResolveRoute selects a backend without assuming an HTTP request or native
// protocol session. It is safe to call concurrently with configuration reload.
func (h *Handler) ResolveRoute(input backends.RouteInput) (backends.RouteDecision, bool) {
	h.mu.RLock()
	opts := h.options
	routes := h.userRoutes
	defaultTarget := h.defaultTarget
	replaceAllowed := h.enableReplaceCreds
	h.mu.RUnlock()

	useDefault := func() (backends.RouteDecision, bool) {
		if !defaultTarget.Available() {
			outcome := backends.RouteOutcomeNoRoute
			if defaultTarget.Backend != nil {
				outcome = backends.RouteOutcomeUnavailable
			}
			return backends.RouteDecision{Outcome: outcome}, false
		}
		return backends.RouteDecision{Target: defaultTarget, Outcome: backends.RouteOutcomeDefault}, true
	}
	if input.Username == "" || opts == nil || len(opts.Users) == 0 {
		return useDefault()
	}
	mapping, ok := opts.Users[input.Username]
	if !ok || mapping == nil {
		return useDefault()
	}
	target, ok := routes[input.Username]
	if !ok || !target.Available() {
		return useDefault()
	}
	decision := backends.RouteDecision{Target: target, Outcome: backends.RouteOutcomeSelected}
	if replaceAllowed && input.Authenticated &&
		(mapping.ToUser != "" || mapping.ToCredential != "") {
		decision.OutboundUsername = input.Username
		if mapping.ToUser != "" {
			decision.OutboundUsername = mapping.ToUser
		}
		decision.OutboundCredential = input.Credential
		if mapping.ToCredential != "" {
			decision.OutboundCredential = string(mapping.ToCredential)
		}
		decision.ReplaceCredentials = decision.OutboundCredential != ""
	}
	return decision, true
}

// SetRouterName identifies this resolver without exposing protocol-specific
// listener or connection objects through the shared routing contract.
func (h *Handler) SetRouterName(name string) {
	h.mu.Lock()
	h.routerName = name
	h.mu.Unlock()
}

func (h *Handler) SetAuthenticator(a at.Authenticator, enableReplaceCreds bool) {
	h.mu.Lock()
	h.authenticator = a
	h.enableReplaceCreds = enableReplaceCreds
	h.mu.Unlock()
}

func (h *Handler) SetDefaultHandler(h2 http.Handler) {
	h.mu.Lock()
	h.defaultHandler = h2
	h.mu.Unlock()
}

// SetDefaultTarget updates the protocol-neutral fallback backend.
func (h *Handler) SetDefaultTarget(target backends.RouteTarget) {
	h.mu.Lock()
	h.defaultTarget = target
	h.mu.Unlock()
}

func (h *Handler) SetNoRouteStatusCode(code int) {
	h.mu.Lock()
	h.noRouteStatusCode = code
	h.mu.Unlock()
}

func (h *Handler) SetUserRoutes(routes UserRoutes) {
	h.mu.Lock()
	h.userRoutes = maps.Clone(routes)
	h.mu.Unlock()
}

func (h *Handler) handleDefault(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	dh := h.defaultHandler
	code := h.noRouteStatusCode
	if code == 0 && h.options != nil {
		code = h.options.NoRouteStatusCode
	}
	h.mu.RUnlock()
	if dh == nil {
		if code < 100 || code >= 600 {
			code = http.StatusBadGateway
		}
		w.WriteHeader(code)
		return
	}
	dh.ServeHTTP(w, r)
}
