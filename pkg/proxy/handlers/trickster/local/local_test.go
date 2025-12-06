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

package local

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestHandleLocalResponse(t *testing.T) {

	HandleLocalResponse(nil, nil)
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	_, err := config.Load([]string{"-origin-url", "http://1.2.3.4", "-provider",
		providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	const expected = "trickster"
	expectedPtr := expected

	pc := &po.Options{
		ResponseCode:      418,
		ResponseBody:      &expectedPtr,
		ResponseBodyBytes: []byte(expected),
		ResponseHeaders:   map[string]string{headers.NameTricksterResult: "1234"},
	}

	logger.SetLogger(logging.ConsoleLogger(level.Error))

	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 418 OK and "pong"
	if resp.StatusCode != 418 {
		t.Errorf("expected 418 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) < 1 {
		t.Error("missing body in response")
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected %s got %s", expected, string(bodyBytes))
	}

	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Errorf("expected header valuef for %s", headers.NameTricksterResult)
	}

}

func TestHandleLocalResponseBadResponseCode(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	_, err := config.Load([]string{"-origin-url", "http://1.2.3.4", "-provider",
		providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	const expected = "trickster"
	expectedPtr := expected

	pc := &po.Options{
		ResponseCode:      0,
		ResponseBody:      &expectedPtr,
		ResponseBodyBytes: []byte(expected),
		ResponseHeaders:   map[string]string{headers.NameTricksterResult: "1234"},
	}

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 200 OK and because we passed 0
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected %s got %s", expected, string(bodyBytes))
	}

	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Errorf("expected header valuef for %s", headers.NameTricksterResult)
	}

}

func TestHandleLocalResponseNoPathConfig(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	_, err := config.Load([]string{"-origin-url", "http://1.2.3.4", "-provider",
		providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, nil, nil, nil, nil, nil)))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 200 OK and "pong"
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) > 0 {
		t.Errorf("body should be empty")
	}

}
