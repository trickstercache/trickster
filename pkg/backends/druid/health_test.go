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

package druid

import (
	"net/http"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func TestDefaultHealthCheckConfig(t *testing.T) {
	o := bo.New()
	o.Scheme = "https"
	o.Host = "druid.example"
	o.PathPrefix = "/router"
	c, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := c.DefaultHealthCheckConfig()
	if h.Scheme != "https" || h.Host != "druid.example" ||
		h.Path != "/router/status/health" || h.Verb != http.MethodGet ||
		h.ExpectedBody != "true" {
		t.Fatalf("unexpected health config: %+v", h)
	}
}
