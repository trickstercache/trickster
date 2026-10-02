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
package postgres

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

const (
	testBackendName  = "postgres-test"
	testProbeTimeout = 5 * time.Second
)

func TestPostgresBackendContract(t *testing.T) {
	o := bo.New()
	o.Name, o.Provider = testBackendName, providers.TimescaleDB
	o.OriginURL = "postgres://origin:origin-password@db.example/trickster"
	backend, err := NewClient(testBackendName, o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := backend.(*Client)
	if !ok {
		t.Fatalf("NewClient() returned %T", backend)
	}
	if backend.Name() != testBackendName || backend.Configuration() != o {
		t.Fatal("the client must retain its name and options")
	}
	// the backend is served by a postgres listener and has no HTTP surface
	client.RegisterHandlers(nil)
	if paths := client.DefaultPathConfigs(o); paths != nil {
		t.Fatalf("expected no HTTP paths, got %v", paths)
	}
}

func TestEngine(t *testing.T) {
	engine := Engine()
	if engine.Name() != providers.Postgres || engine.DefaultPort() != DefaultPort ||
		engine.Dialect() != providers.Postgres {
		t.Fatalf("unexpected engine %q %q %q", engine.Name(), engine.DefaultPort(), engine.Dialect())
	}
	if engine.Defaults() != (pgwire.EngineDefaults{}) || engine.TimeSemantics() != (pgwire.TimeSemantics{}) {
		t.Fatal("PostgreSQL needs no connection defaults and gives zone-less timestamps no zone")
	}
	if kind, ok := engine.TimeAxis(pgwire.OIDTimestampTZ); !ok || kind != pgwire.TimeAxisTimestampTZ {
		t.Fatalf("unexpected time axis %v %t", kind, ok)
	}
}

func TestHealthCheckProbe(t *testing.T) {
	o := bo.New()
	o.Name, o.Provider = testBackendName, providers.Postgres
	// nothing listens on the discard port, so the probe fails without naming the origin
	o.OriginURL = "postgres://origin:origin-password@127.0.0.1:9/trickster"
	backend, err := NewClient(testBackendName, o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backend.(*Client)
	if client.DefaultHealthCheckConfig() == nil {
		t.Fatal("expected protocol-neutral health check defaults")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testProbeTimeout)
	defer cancel()
	if err = client.HealthCheckProbe()(ctx); err == nil || strings.Contains(err.Error(), "origin-password") {
		t.Fatalf("expected a sanitized failure, got %v", err)
	}
	o.OriginURL = "://bad"
	if err = client.HealthCheckProbe()(ctx); !errors.Is(err, errProbeConfig) {
		t.Fatalf("expected a configuration error, got %v", err)
	}
}
