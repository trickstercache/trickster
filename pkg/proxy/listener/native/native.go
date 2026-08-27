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

// Package native defines the provider extension point for non-HTTP listeners.
package native

import (
	"net/http"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

// Descriptor contains the immutable identity needed to reconcile a listener.
type Descriptor struct {
	RestartKey string
}

// BuildRequest supplies runtime dependencies without making setup aware of a
// provider's protocol configuration.
type BuildRequest struct {
	Config         *config.Config
	ListenerName   string
	Listener       *listenerconfig.Options
	Tracers        tracing.Tracers
	BackendClients backends.Backends
}

// Adapter owns the provider-specific lifecycle of a native listener.
// Implementations validate configuration, describe restart identity, build the
// protocol server, and expose a reloadable route resolver when applicable.
type Adapter interface {
	Protocol() string
	SupportsHTTP() bool
	Configured(*listenerconfig.Options) bool
	ValidateListener(*listenerconfig.Options) error
	ValidateBackend(*bo.Options) error
	ValidateUserRouter(*config.Config, string, *bo.Options) error
	Describe(*config.Config, string) (Descriptor, error)
	Build(BuildRequest) (listener.ProtocolServer, error)
	RouteResolver(BuildRequest) backends.RouteResolver
}

// Registry maps listener protocols to their provider-owned adapters.
type Registry map[string]Adapter

// Get returns the adapter registered for protocol.
func (r Registry) Get(protocol string) Adapter {
	return r[protocol]
}

// ConfiguredProtocol returns the first, lexically ordered protocol whose
// provider-specific options are present, excluding except.
func (r Registry) ConfiguredProtocol(options *listenerconfig.Options, except string) string {
	protocols := make([]string, 0, len(r))
	for protocol := range r {
		protocols = append(protocols, protocol)
	}
	slices.Sort(protocols)
	for _, protocol := range protocols {
		adapter := r[protocol]
		if protocol != except && adapter.Configured(options) {
			return protocol
		}
	}
	return ""
}

// HTTPHandlerAdapter supplies reloadable HTTP routing for a protocol bridge.
type HTTPHandlerAdapter interface {
	Handler(BuildRequest) (http.Handler, error)
}
