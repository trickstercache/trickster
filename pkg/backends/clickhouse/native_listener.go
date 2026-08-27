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

package clickhouse

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	chnative "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	yamlencoding "github.com/trickstercache/trickster/v2/pkg/encoding/yaml"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware/bodyfilter"
)

type nativeListenerAdapter struct{}

// NativeListenerAdapter returns ClickHouse's shared native-listener adapter.
func NativeListenerAdapter() native.Adapter { return nativeListenerAdapter{} }

func (nativeListenerAdapter) SupportsHTTP() bool { return true }

func (nativeListenerAdapter) Protocol() string                        { return listenerconfig.ProtocolClickHouse }
func (nativeListenerAdapter) Configured(*listenerconfig.Options) bool { return false }
func (nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil ClickHouse listener options")
	}
	return nil
}

func (nativeListenerAdapter) ValidateBackend(o *bo.Options) error { return chnative.ValidateOptions(o) }

func (nativeListenerAdapter) ValidateUserRouter(*config.Config, string, *bo.Options) error {
	return errors.New("ClickHouse native user routing is not supported")
}

func (nativeListenerAdapter) RouteResolver(native.BuildRequest) backends.RouteResolver { return nil }

func nativeBackend(c *config.Config, name string) (string, *bo.Options, error) {
	if c == nil {
		return "", nil, errors.New("missing configuration")
	}
	var found string
	var options *bo.Options
	for backendName, o := range c.Backends {
		if !o.UsesListener(name) {
			continue
		}
		if options != nil {
			return "", nil, errors.New("native listener requires exactly one backend")
		}
		if !strings.EqualFold(o.Provider, providers.ClickHouse) {
			return "", nil, errors.New("native listener requires a ClickHouse backend")
		}
		found, options = backendName, o
	}
	if options == nil {
		return "", nil, errors.New("no mapped backend")
	}
	if options.RequireTLS && (options.TLS == nil || !options.TLS.ServeTLS) {
		return "", nil, errors.New("native require_tls needs a server certificate and key")
	}
	return found, options, nil
}

func (nativeListenerAdapter) Describe(c *config.Config, name string) (native.Descriptor, error) {
	backendName, o, err := nativeBackend(c, name)
	if err != nil {
		return native.Descriptor{}, err
	}
	identity := o.Clone()
	identity.ListenerName = ""
	identity.ListenerNames = nil
	data, err := yamlencoding.Marshal(identity)
	if err != nil {
		return native.Descriptor{}, err
	}
	return native.Descriptor{RestartKey: fmt.Sprintf("%s:%x", backendName, sha256.Sum256(data))}, nil
}

func (a nativeListenerAdapter) Build(r native.BuildRequest) (listener.ProtocolServer, error) {
	_, o, err := nativeBackend(r.Config, r.ListenerName)
	if err != nil {
		return nil, err
	}
	descriptor, err := a.Describe(r.Config, r.ListenerName)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := r.Config.TLSCertConfigForListener(r.ListenerName)
	if err != nil {
		return nil, err
	}
	h, err := a.Handler(r)
	if err != nil {
		return nil, err
	}

	return server.New(h, tlsConfig, o.RequireTLS, descriptor.RestartKey), nil
}

func (nativeListenerAdapter) Handler(r native.BuildRequest) (http.Handler, error) {
	name, _, err := nativeBackend(r.Config, r.ListenerName)
	if err != nil {
		return nil, err
	}
	client := r.BackendClients.Get(name)
	if client == nil || client.Router() == nil {
		return nil, errors.New("missing native backend router")
	}
	h := client.Router()
	if r.Listener != nil && r.Listener.MaxRequestBodySizeBytes != nil {
		h = bodyfilter.Handler(*r.Listener.MaxRequestBodySizeBytes, false, h)
	}
	return h, nil
}
