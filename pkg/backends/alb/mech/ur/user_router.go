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
	"net/http"
	"sync"

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

type Handler struct {
	// mu guards options, authenticator, enableReplaceCreds against concurrent
	// reads on the request path and writes during SIGHUP config reload via
	// ValidateAndStartPool -> SetAuthenticator / SetDefaultHandler.
	mu                 sync.RWMutex
	authenticator      at.Authenticator
	enableReplaceCreds bool
	options            *uropt.Options
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
	out := &Handler{options: o.UserRouter}
	return out, nil
}

func (h *Handler) Name() types.Name {
	return names.MechanismUR
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	opts := h.options
	auth := h.authenticator
	replaceAllowed := h.enableReplaceCreds
	h.mu.RUnlock()

	var username, cred string
	var enableReplaceCreds bool

	rsc := request.GetResources(r)
	// this checks if an authenticator has already handled the request, and if
	// so, uses the Authenticator data. Otherwise, it asks the backend-provider-
	// default authenticator (usually Basic Auth) for the username.
	if rsc != nil && rsc.AuthResult != nil && rsc.AuthResult.Username != "" {
		username = rsc.AuthResult.Username
		enableReplaceCreds = replaceAllowed && rsc.AuthResult.Status == at.AuthSuccess
	} else if auth != nil {
		u, c, err := auth.ExtractCredentials(r)
		if err == nil && u != "" {
			username = u
			cred = c
			// enableReplaceCreds remains false since credentials were not verified
		}
	}
	// if the request doesn't have a username or there are 0 Users in the Router,
	// the default handler takes the request
	if username == "" || len(opts.Users) == 0 {
		h.handleDefault(w, r)
		return
	}
	// if the User is found in the Router map, process their options
	if u, ok := opts.Users[username]; ok {
		// this handles when username or credential is configured to be remapped
		if enableReplaceCreds && (u.ToUser != "" || u.ToCredential != "") {
			// swap in the new user if configured
			if u.ToUser != "" {
				username = u.ToUser
			}
			// swap in the new credential if configured. When ToCredential is
			// empty, retain the inbound credential rather than overwriting with
			// an empty password (which would silently corrupt Basic auth on
			// the backend).
			if u.ToCredential != "" {
				cred = string(u.ToCredential)
			}
			// Don't write empty creds: callers using AuthResult-only auth
			// (SSO, etc.) have cred == "" with nothing to fall back on.
			// SetCredentials(r, user, "") emits Basic auth with an empty
			// password and collapses every such user into one cache key.
			if cred != "" {
				auth.Sanitize(r)
				if err := auth.SetCredentials(r, username, cred); err != nil {
					h.handleDefault(w, r)
					return
				}
			}
		}
		// this passes the request to a user-specific route handler, if set
		// and the routed backend is currently considered healthy. ToStatus
		// values below StatusUnchecked (Failing, Initializing) fall through
		// to the default handler instead of dispatching to a known-bad target.
		if u.ToHandler != nil {
			if u.ToStatus == nil || u.ToStatus.Get() >= 0 {
				u.ToHandler.ServeHTTP(w, r)
				return
			}
		}
	}
	// the default handler serves the request when the user doesn't have an entry
	// in the router map, or when a mapped user's entry doesn't have a ToHandler
	h.handleDefault(w, r)
}

func (h *Handler) SetAuthenticator(a at.Authenticator, enableReplaceCreds bool) {
	h.mu.Lock()
	h.authenticator = a
	h.enableReplaceCreds = enableReplaceCreds
	h.mu.Unlock()
}

func (h *Handler) SetDefaultHandler(h2 http.Handler) {
	h.mu.Lock()
	if h.options != nil {
		h.options.DefaultHandler = h2
	}
	h.mu.Unlock()
}

func (h *Handler) handleDefault(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	dh := h.options.DefaultHandler
	code := h.options.NoRouteStatusCode
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
