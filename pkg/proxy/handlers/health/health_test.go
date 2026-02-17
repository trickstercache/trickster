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

package health

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

var _ healthcheck.HealthChecker = (*mockHealthChecker)(nil)

type mockTarget struct {
	description string
	options     *ho.Options
	client      *http.Client
	status      *healthcheck.Status
}

type mockHealthChecker struct {
	// map of registered targets, with configurable status
	targets map[string]mockTarget
	// if configured, return status for all targets
	globalStatus *healthcheck.Status
	// list of subscribers to notify of activity
	subscribers []chan bool
}

func newMockHealthChecker() *mockHealthChecker {
	return &mockHealthChecker{
		targets: make(map[string]mockTarget),
	}
}

func (m *mockHealthChecker) Register(name string, description string, options *ho.Options, client *http.Client) (*healthcheck.Status, error) {
	target := mockTarget{
		description: description,
		options:     options,
		client:      client,
	}
	if m.globalStatus != nil {
		target.status = m.globalStatus
	} else {
		target.status = healthcheck.NewStatus(name, description, "initializing", 0, time.Time{}, nil)
	}
	m.targets[name] = target
	return &healthcheck.Status{}, nil
}

func (m *mockHealthChecker) Unregister(name string) {
	delete(m.targets, name)
}

func (m *mockHealthChecker) Status(name string) *healthcheck.Status {
	if m.globalStatus != nil {
		return m.globalStatus
	}

	if t, ok := m.targets[name]; ok {
		return t.status
	}
	return nil
}

func (m *mockHealthChecker) Subscribe(ch chan bool) {
	m.subscribers = append(m.subscribers, ch)
}

// no-op for mock
func (m *mockHealthChecker) Statuses() healthcheck.StatusLookup {
	lookup := make(healthcheck.StatusLookup, len(m.targets))
	for name, t := range m.targets {
		status := t.status
		if t.status == nil {
			if m.globalStatus == nil {
				continue // no status or global status, skip
			}
			t.status = m.globalStatus
		}
		lookup[name] = status
	}
	return lookup
}

func (m *mockHealthChecker) notify(status bool) {
	for _, ch := range m.subscribers {
		ch <- status
	}
}

func (m *mockHealthChecker) Shutdown() {
	m.notify(true)
	// no-op for mock, no background processes to stop
}

type mockBackend struct {
	name     string
	handlers handlers.Lookup
}

func (m *mockBackend) RegisterHandlers(hl handlers.Lookup) {
	maps.Copy(m.handlers, hl)
}

func (m *mockBackend) Handlers() handlers.Lookup {
	return m.handlers
}

func (m *mockBackend) DefaultPathConfigs(*bo.Options) po.List {
	return nil
}

func (m *mockBackend) Configuration() *bo.Options {
	return nil
}

func (m *mockBackend) Name() string {
	return m.name
}

func (m *mockBackend) HTTPClient() *http.Client {
	return nil
}
func (m *mockBackend) SetCache(cache.Cache) {}
func (m *mockBackend) Router() http.Handler {
	return nil
}

func (m *mockBackend) Cache() cache.Cache {
	return nil
}

func (m *mockBackend) BaseUpstreamURL() *url.URL {
	return nil
}
func (m *mockBackend) SetHealthCheckProbe(healthcheck.DemandProbe)      {}
func (m *mockBackend) HealthHandler(http.ResponseWriter, *http.Request) {}
func (m *mockBackend) DefaultHealthCheckConfig() *ho.Options {
	return nil
}

func (m *mockBackend) HealthCheckHTTPClient() *http.Client {
	return nil
}

func TestStatusHandler(t *testing.T) {
	const (
		name = "mock-backend"
	)

	hc := newMockHealthChecker()
	hc.globalStatus = healthcheck.NewStatus(name, "mock health checker", "mock detail", 1, time.Time{}, nil)
	backends := make(backends.Backends)
	backends[name] = &mockBackend{
		name: name,
	}
	status, err := hc.Register(name, "test backend", &ho.Options{}, &http.Client{})
	require.NoError(t, err)
	require.NotNil(t, status)

	sh := StatusHandler(func() time.Time {
		return time.Time{}
	}, hc, backends)
	require.NotNil(t, sh)

	req, err := http.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "application/json")
	require.NoError(t, err)

	w := httptest.NewRecorder()

	sh.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Result().StatusCode)
	expected := `{"title":"Trickster Backend Health Status","updateTime":"0001-01-01 00:00:00 UTC","available":[{"name":"mock-backend","provider":"mock health checker"}]}`
	require.Equal(t, expected, w.Body.String())
}
