/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package handlers

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	tc "github.com/Comcast/trickster/internal/proxy/context"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/request"
	tl "github.com/Comcast/trickster/internal/util/log"
)

func TestHandleLocalResponse(t *testing.T) {

	_, _, err := config.Load("trickster-test", "test", []string{"-origin-url", "http://1.2.3.4", "-origin-type", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	pc := &config.PathConfig{
		ResponseCode:      418,
		ResponseBody:      "[test",
		ResponseBodyBytes: []byte("[test"),
		ResponseHeaders:   map[string]string{headers.NameTricksterResult: "1234"},
	}

	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(nil, pc, nil, nil, nil, tl.ConsoleLogger("error"))))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 418 OK and "pong"
	if resp.StatusCode != 418 {
		t.Errorf("expected 418 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) < 1 {
		t.Errorf("missing body in response")
	}

	if bodyBytes[0] != 91 {
		t.Errorf("response is not toml format")
	}

	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Errorf("expected header valuef for %s", headers.NameTricksterResult)
	}

}

func TestHandleLocalResponseBadResponseCode(t *testing.T) {

	_, _, err := config.Load("trickster-test", "test", []string{"-origin-url", "http://1.2.3.4", "-origin-type", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	pc := &config.PathConfig{
		ResponseCode:      0,
		ResponseBody:      "[test",
		ResponseBodyBytes: []byte("[test"),
		ResponseHeaders:   map[string]string{headers.NameTricksterResult: "1234"},
	}

	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(nil, pc, nil, nil, nil, tl.ConsoleLogger("error"))))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 200 OK and because we passed 0
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) < 1 {
		t.Errorf("missing body in response")
	}

	if bodyBytes[0] != 91 {
		t.Errorf("response is not toml format")
	}

	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Errorf("expected header valuef for %s", headers.NameTricksterResult)
	}

}

func TestHandleLocalResponseNoPathConfig(t *testing.T) {

	_, _, err := config.Load("trickster-test", "test", []string{"-origin-url", "http://1.2.3.4", "-origin-type", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)

	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(nil, nil, nil, nil, nil, tl.ConsoleLogger("error"))))

	HandleLocalResponse(w, r)
	resp := w.Result()

	// it should return 200 OK and "pong"
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if len(bodyBytes) > 0 {
		t.Errorf("body should be empty")
	}

}
