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

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const (
	URID   types.ID   = 5
	URName types.Name = "user_router"
)

type Handler struct {
	authenticator      at.Authenticator
	enableReplaceCreds bool
	options            *uropt.Options
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{
		ID: URID, Name: URName,
		ShortName: names.MechanismUR, New: New,
	}
}

func New(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	if o == nil || o.UserRouter == nil {
		return nil, errors.ErrInvalidOptions
	}
	out := &Handler{options: o.UserRouter}
	return out, nil
}

func (h *Handler) ID() types.ID {
	return URID
}

func (h *Handler) Name() types.Name {
	return names.MechanismUR
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var username, cred string
	var enableReplaceCreds bool

	rsc := request.GetResources(r)
	// this checks if an authenticator has already handled the request, and if
	// so, uses the Authenticator data. Otherwise, it asks the backend-provider-
	// default authenticator (usually Basic Auth) for the username.
	if rsc != nil && rsc.AuthResult != nil && rsc.AuthResult.Username != "" {
		username = rsc.AuthResult.Username
		enableReplaceCreds = h.enableReplaceCreds && rsc.AuthResult.Status == at.AuthSuccess
	} else if h.authenticator != nil {
		u, c, err := h.authenticator.ExtractCredentials(r)
		if err == nil && u != "" {
			username = u
			cred = c
			// enableReplaceCreds remains false since credentials were not verified
		}
	}
	// if the request doesn't have a username or there are 0 Users in the Router,
	// the default handler takes the request
	if username == "" || len(h.options.Users) == 0 {
		h.handleDefault(w, r)
		return
	}
	// if the User is found in the Router map, process their options
	if opts, ok := h.options.Users[username]; ok {
		// this handles when username or credential is configured to be remapped
		if enableReplaceCreds && (opts.ToUser != "" || opts.ToCredential != "") {
			// swap in the new user if configured
			if opts.ToUser != "" {
				username = opts.ToUser
			}
			// swap in the new credential if configured
			if opts.ToCredential != "" {
				cred = string(opts.ToCredential)
			}
			h.authenticator.SetCredentials(r, username, cred)
		}
		// this passes the request to a user-specific route handler, if set
		if opts.ToHandler != nil {
			opts.ToHandler.ServeHTTP(w, r)
			return
		}
	}
	// the default handler serves the request when the user doesn't have an entry
	// in the router map, or when a mapped user's entry doesn't have a ToHandler
	h.handleDefault(w, r)
}

func (h *Handler) SetAuthenticator(a at.Authenticator, enableReplaceCreds bool) {
	h.authenticator = a
	h.enableReplaceCreds = enableReplaceCreds
}

func (h *Handler) SetDefaultHandler(h2 http.Handler) {
	h.options.DefaultHandler = h2
}

func (h *Handler) handleDefault(w http.ResponseWriter, r *http.Request) {
	if h.options.DefaultHandler == nil {
		failures.HandleBadGateway(w, r)
	}
	h.options.DefaultHandler.ServeHTTP(w, r)
}

// stubs for unused interface functions
func (h *Handler) SetPool(_ pool.Pool) {}
func (h *Handler) StopPool()           {}
