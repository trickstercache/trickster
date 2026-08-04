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

package config

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

func TestConfigHandler(t *testing.T) {
	conf, err := config.Load([]string{
		"-origin-url", "http://1.2.3.4",
		"-provider", providers.Prometheus,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	configHandler := HandlerFunc(conf)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/config", nil)

	configHandler(w, r)
	resp := w.Result()

	// it should return 200 OK and "pong"
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) < 1 {
		t.Errorf("missing body in response")
	}

	lines := strings.Split(string(bodyBytes), "\n")

	if !strings.HasSuffix(lines[0], ":") {
		t.Errorf("response is not yaml format")
	}
}

func TestSanitizedConfigHandler(t *testing.T) {
	conf, err := config.Load([]string{
		"-origin-url", "http://private.example",
		"-provider", providers.Prometheus,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	configHandler := SanitizedHandlerFunc(conf)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/config/sanitized", nil)

	configHandler(w, r)
	resp := w.Result()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	body := string(bodyBytes)
	if !strings.Contains(body, "prom-1:") {
		t.Errorf("expected sanitized backend name in response")
	}
	if strings.Contains(body, "private.example") {
		t.Errorf("expected sanitized response not to contain private origin")
	}
}

func TestSanitizedHandlerPath(t *testing.T) {
	if got, want := SanitizedHandlerPath("/trickster/config"), "/trickster/config/sanitized"; got != want {
		t.Errorf("expected %s got %s", want, got)
	}
	if got, want := SanitizedHandlerPath("/trickster/config/"), "/trickster/config/sanitized"; got != want {
		t.Errorf("expected %s got %s", want, got)
	}
}
