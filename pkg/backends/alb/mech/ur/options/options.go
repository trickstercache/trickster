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

package options

import (
	"fmt"
	"maps"
	"net/http"

	te "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

// import ct "github.com/trickstercache/trickster/v2/pkg/config/types"

const UserRouterKey = "user_router"

type Options struct {
	// DefaultBackend is the name of the backend that will handle the request
	// when the inbound user does have an entry in the Users mapping. If no
	// DefaultBackend is configured, the user will receive a failure response
	// with the NoRouteStatusCode.
	DefaultBackend string `yaml:"default_backend"`
	// NoRouteStatusCode is the Status Code returned to the client when the
	// request can't be routed to a backend. Default is 401 (Unauthorized), but
	// can be overridden to something like 404 (Not Found) or 502 (Bad Gateway).
	NoRouteStatusCode int `yaml:"no_route_status_code,omitempty"`
	// Users is a map of usernames to user-specific mapping options
	Users UserMappingOptionsByUser `yaml:"users,omitempty"`
	// DefaultHandler is the the HTTP Handler for DefaultBackend
	DefaultHandler http.Handler `yaml:"-"`
	// TargetProvider is the Provider name (e.g., 'rpc' or 'clickhouse') that
	// the user router is handling. While a User Router can point to multiple
	// ALBs, Rules and Backends, all non-virtual Backends (non-rule, non-ALB)
	// must be of the same Provider type. During validation, requirement is
	// checked and the TargetProvider value is set based on the check results.
	TargetProvider string `yaml:"-"`
	albName        string
}

// UserMappingOptions holds per-user configurations that direct the User Router
type UserMappingOptions struct {
	// ToBackend is the name of the Backend where requests from this user are routed
	ToBackend string `yaml:"to_backend"`
	// ToUser is the User Name that will be substituted in the upstream request
	ToUser string `yaml:"to_user"`
	// ToCredential is the Credential that will be substituted in the upstream request
	ToCredential types.EnvString `yaml:"to_credential"`
	// ToHandler is the the HTTP Handler for the Backend in ToBackend
	ToHandler http.Handler `yaml:"-"`
}

type UserMappingOptionsByUser map[string]*UserMappingOptions

// InvalidUserRouterOptionsError is an error type for invalid User Router Options
type InvalidUserRouterOptionsError struct {
	error
}

// NewErrInvalidUserRouterOptions returns an invalid User Router Options error
func NewErrInvalidUserRouterOptions(backendName string) error {
	return &InvalidUserRouterOptionsError{
		error: fmt.Errorf("invalid user router options for backend [%s]",
			backendName),
	}
}

func (o *Options) Clone() *Options {
	return &Options{
		DefaultBackend:    o.DefaultBackend,
		NoRouteStatusCode: o.NoRouteStatusCode,
		Users:             maps.Clone(o.Users),
	}
}

// OverlayYAMLData extracts supported User Router Options values from the yaml
// map, and returns a new default Options overlaid with the extracted values
func OverlayYAMLData(albName string, options *Options,
	y yamlx.KeyLookup) (*Options, error) {
	if y == nil {
		return nil, te.ErrInvalidOptionsMetadata
	}
	if !y.IsDefined(providers.Backends, albName, providers.ALB, UserRouterKey) {
		return nil, nil
	}
	o := &Options{NoRouteStatusCode: http.StatusUnauthorized}
	o.albName = albName
	if y.IsDefined(providers.Backends, albName, providers.ALB, UserRouterKey, "default_backend") {
		o.DefaultBackend = options.DefaultBackend
	}
	if y.IsDefined(providers.Backends, albName, providers.ALB, UserRouterKey, "no_route_status_code") {
		o.NoRouteStatusCode = options.NoRouteStatusCode
	}
	if y.IsDefined(providers.Backends, albName, providers.ALB, UserRouterKey, "users") {
		o.Users = maps.Clone(options.Users)
	}
	return o, nil
}

func (o *Options) Validate(backendTypes map[string]string) error {
	found := sets.NewStringSet()
	if o.DefaultBackend != "" {
		t, ok := backendTypes[o.DefaultBackend]
		if !ok {
			return NewErrInvalidUserRouterOptions(o.albName)
		}
		found.Set(t)
	}
	for _, u := range o.Users {
		if u.ToBackend != "" {
			t, ok := backendTypes[u.ToBackend]
			if !ok {
				return NewErrInvalidUserRouterOptions(o.albName)
			}
			found.Set(t)
		}
	}

	return nil
}
