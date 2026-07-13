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

package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func TestNewObserverFromElasticsearchProvider(t *testing.T) {
	a, err := NewObserverFromProviderName(providers.Elasticsearch, map[string]any{
		"options": &options.Options{ObserveOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.SetBasicAuth("alice", "secret")
	result, err := a.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Username != "alice" || result.Status != types.AuthObserved {
		t.Fatalf("Authenticate() = %+v, want observed user alice", result)
	}
}
