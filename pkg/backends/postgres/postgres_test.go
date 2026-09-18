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
	"net/http"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

const testBackendName = "postgres-test"

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
	if engine.Name() != providers.Postgres || engine.DefaultPort() != DefaultPort {
		t.Fatalf("unexpected engine %q %q", engine.Name(), engine.DefaultPort())
	}
}
