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

package ping

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

func TestPingHandler(t *testing.T) {

	conf, err := config.Load([]string{"-provider", providers.ReverseProxyCache,
		"-origin-url", "http://0/"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	pingHandler := HandlerFunc(conf)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/ping", nil)

	pingHandler(w, r)
	resp := w.Result()

	// it should return 200 OK and "pong"
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "pong" {
		t.Errorf("expected 'pong' got %s.", bodyBytes)
	}

}
